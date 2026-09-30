package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/fleet"
	"github.com/realblxckcodex/fylgja/internal/router"
)

// agentRow: "Welcher Agent läuft wo?" (18.5 D).
type agentRow struct {
	RunID       uuid.UUID      `json:"run_id"`
	DotID       uuid.UUID      `json:"dot_id"`
	DotName     string         `json:"dot_name"`
	Kind        string         `json:"kind"`
	Role        string         `json:"role"`
	Status      string         `json:"status"`
	Task        string         `json:"task"`
	Graph       string         `json:"graph,omitempty"`
	SandboxHost string         `json:"sandbox_host"`
	Deployments map[string]int `json:"deployments"` // letzte 5 min: Deployment → Aufrufe
	TokensMin   float64        `json:"tokens_per_min"`
	CostToday   int64          `json:"cost_today_micro_eur"`
	StartedAt   *time.Time     `json:"started_at"`
}

func (s *Server) fleetAgents(r *http.Request) []agentRow {
	ctx := r.Context()
	rows, err := s.Pool.Query(ctx, `SELECT ru.id, ru.dot_id, d.name, ru.kind, ru.status, left(coalesce(ru.input->>'text',''), 120), coalesce(ru.input->>'graph',''),
			coalesce(sh.name, CASE WHEN sb.state IS NOT NULL THEN 'lokal' ELSE '' END), ru.started_at,
			(SELECT coalesce(sum(cost_micro_eur),0) FROM usage_events u WHERE u.dot_id=ru.dot_id AND u.created_at >= date_trunc('day', now()))
		FROM runs ru JOIN dots d ON d.id=ru.dot_id
		LEFT JOIN sandboxes sb ON sb.dot_id=ru.dot_id LEFT JOIN sandbox_hosts sh ON sh.id=sb.host_id
		WHERE d.workspace_id=$1 AND ru.status IN ('running','waiting','queued') ORDER BY ru.created_at DESC LIMIT 200`, principal(r).WorkspaceID)
	if err != nil {
		return nil
	}
	var out []agentRow
	for rows.Next() {
		var a agentRow
		_ = rows.Scan(&a.RunID, &a.DotID, &a.DotName, &a.Kind, &a.Status, &a.Task, &a.Graph, &a.SandboxHost, &a.StartedAt, &a.CostToday)
		a.Role = map[string]string{"subagent": "subagent", "task_step": "worker/teammate", "pulse": "pulse", "chat": "chat"}[a.Kind]
		a.Deployments = map[string]int{}
		out = append(out, a)
	}
	rows.Close()
	for i := range out {
		dr, err := s.Pool.Query(ctx, `SELECT deployment, count(*), coalesce(sum(tokens_in+tokens_out),0) FROM usage_events WHERE run_id=$1 AND created_at > now()-interval '5 minutes' GROUP BY deployment`, out[i].RunID)
		if err != nil {
			continue
		}
		var tok int64
		for dr.Next() {
			var dep string
			var n int
			var t int64
			_ = dr.Scan(&dep, &n, &t)
			out[i].Deployments[firstNonEmpty(dep, "?")] = n
			tok += t
		}
		dr.Close()
		out[i].TokensMin = float64(tok) / 5
	}
	return out
}

func (s *Server) fleetOverview(w http.ResponseWriter, r *http.Request) {
	var snap router.Snapshot
	if s.Router != nil {
		snap = s.Router.Snapshot()
	}
	var nodes []fleet.Node
	var spend int64
	var events []fleet.ScaleEvent
	var policies []fleet.Policy
	if s.Fleet != nil {
		nodes, spend, events, policies = s.Fleet.Nodes(), s.Fleet.SpendToday(), s.Fleet.Events(), s.Fleet.Policies()
	}
	if nodes == nil {
		nodes = []fleet.Node{}
	}
	if events == nil {
		events = []fleet.ScaleEvent{}
	}
	if policies == nil {
		policies = []fleet.Policy{}
	}
	agents := s.fleetAgents(r)
	if agents == nil {
		agents = []agentRow{}
	}
	ctx := r.Context()
	var costToday, attributedGPU int64
	_ = s.Pool.QueryRow(ctx, `SELECT coalesce(sum(cost_micro_eur),0) FROM usage_events WHERE created_at >= date_trunc('day', now())`).Scan(&costToday)
	_ = s.Pool.QueryRow(ctx, `SELECT coalesce(sum(u.cost_micro_eur),0) FROM usage_events u JOIN deployments d ON d.name=u.deployment
		WHERE d.provider IN ('runpod','local') AND u.created_at >= date_trunc('day', now())`).Scan(&attributedGPU)
	var gpuUtil float64
	ready := 0
	for _, n := range nodes {
		if n.State == fleet.Ready {
			gpuUtil += n.Metrics.GPUUtil
			ready++
		}
	}
	if ready > 0 {
		gpuUtil /= float64(ready)
	}
	var waitP95 float64
	for _, q := range snap.Queues {
		waitP95 = max(waitP95, q.WaitP95)
	}
	hosts := []map[string]any{}
	hr, err := s.Pool.Query(ctx, `SELECT h.name, h.provider, h.status, h.capacity,
		(SELECT count(*) FROM sandboxes s WHERE s.host_id=h.id AND s.state='running'), (SELECT count(*) FROM sandboxes s WHERE s.host_id=h.id AND s.state='sleeping') FROM sandbox_hosts h`)
	if err == nil {
		for hr.Next() {
			var name, prov, st string
			var cap []byte
			var running, sleeping int
			_ = hr.Scan(&name, &prov, &st, &cap, &running, &sleeping)
			hosts = append(hosts, map[string]any{"name": name, "provider": prov, "status": st, "running": running, "sleeping": sleeping})
		}
		hr.Close()
	}
	var sbRunning, sbSleeping int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state='running'), count(*) FILTER (WHERE state='sleeping') FROM sandboxes`).Scan(&sbRunning, &sbSleeping)
	writeJSON(w, 200, map[string]any{
		"summary": map[string]any{"gpu_util": gpuUtil, "nodes_ready": ready, "nodes_total": len(nodes), "queue_wait_p95_ms": waitP95,
			"cost_today_micro_eur": costToday, "fleet_spend_today_micro_eur": spend, "fleet_overhead_micro_eur": max(spend-attributedGPU, 0),
			"sandboxes_running": sbRunning, "sandboxes_sleeping": sbSleeping},
		"router": snap, "nodes": nodes, "agents": agents, "scale_events": events, "policies": policies, "sandbox_hosts": hosts,
	})
}

func (s *Server) nodeAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Fleet == nil {
			problem(w, 501, "flotte nicht aktiv")
			return
		}
		id := chiParam(r, "id")
		var err error
		switch action {
		case "drain":
			err = s.Fleet.Drain(id)
		case "terminate":
			err = s.Fleet.Terminate(r.Context(), id)
		}
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
		s.audit(r, "fleet."+action, id, nil)
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

func (s *Server) provision(w http.ResponseWriter, r *http.Request) {
	if s.Fleet == nil {
		problem(w, 501, "flotte nicht aktiv")
		return
	}
	n, err := s.Fleet.Provision(r.Context(), chiParam(r, "pool"))
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.audit(r, "fleet.provision", n.ID, map[string]any{"pool": n.Pool})
	writeJSON(w, 201, n)
}

func (s *Server) setPolicy(w http.ResponseWriter, r *http.Request) {
	if s.Fleet == nil {
		problem(w, 501, "flotte nicht aktiv")
		return
	}
	var p fleet.Policy
	if err := decode(r, &p); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if p.Name == "" || p.MaxNodes < p.MinNodes {
		problem(w, 400, "name fehlt oder max_nodes < min_nodes")
		return
	}
	s.Fleet.SetPolicy(p)
	_, _ = s.Pool.Exec(r.Context(), `INSERT INTO fleet_policies (id, name, min_nodes, max_nodes, scale_up, scale_down, daily_budget_micro_eur, hard, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (name) DO UPDATE SET min_nodes=EXCLUDED.min_nodes, max_nodes=EXCLUDED.max_nodes, scale_up=EXCLUDED.scale_up,
		scale_down=EXCLUDED.scale_down, daily_budget_micro_eur=EXCLUDED.daily_budget_micro_eur, hard=EXCLUDED.hard, enabled=EXCLUDED.enabled`,
		uuid.Must(uuid.NewV7()), p.Name, p.MinNodes, p.MaxNodes, map[string]any{"up": p.Up, "spec": p.Spec, "model": p.Model, "cooldown_s": p.Cooldown.Seconds()}, p.Down, p.DailyBudget, p.Hard, p.Enabled)
	s.audit(r, "fleet.policy", p.Name, map[string]any{"min": p.MinNodes, "max": p.MaxNodes, "budget": p.DailyBudget})
	writeJSON(w, 200, p)
}

// costs: Kosten je Fylgja/Modell/Deployment, Zeitreihe (18.10, 18.5 E).
func (s *Server) costs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := principal(r).WorkspaceID
	group := func(q string) []map[string]any {
		rows, err := s.Pool.Query(ctx, q, ws)
		if err != nil {
			return nil
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var k string
			var cost, tin, tout, cached int64
			var calls int
			_ = rows.Scan(&k, &cost, &tin, &tout, &cached, &calls)
			out = append(out, map[string]any{"key": k, "cost_micro_eur": cost, "tokens_in": tin, "tokens_out": tout, "tokens_cached": cached, "calls": calls})
		}
		return out
	}
	base := `FROM usage_events u LEFT JOIN dots d ON d.id=u.dot_id WHERE (d.workspace_id=$1 OR u.dot_id IS NULL) AND u.created_at >= date_trunc('month', now())`
	agg := `coalesce(sum(u.cost_micro_eur),0), coalesce(sum(u.tokens_in),0), coalesce(sum(u.tokens_out),0), coalesce(sum(u.tokens_cached),0), count(*)`
	daily := []map[string]any{}
	rows, err := s.Pool.Query(ctx, `SELECT date_trunc('day', u.created_at)::date, coalesce(sum(u.cost_micro_eur),0) `+base+` GROUP BY 1 ORDER BY 1`, ws)
	if err == nil {
		for rows.Next() {
			var day time.Time
			var c int64
			_ = rows.Scan(&day, &c)
			daily = append(daily, map[string]any{"day": day.Format("2006-01-02"), "cost_micro_eur": c})
		}
		rows.Close()
	}
	budgets := []map[string]any{}
	br, err := s.Pool.Query(ctx, `SELECT b.id, b.scope, b.scope_id, coalesce(d.name,''), b.period, b.limit_micro_eur, b.hard FROM budgets b LEFT JOIN dots d ON d.id=b.scope_id`)
	if err == nil {
		for br.Next() {
			var id, sid uuid.UUID
			var scope, name, period string
			var limit int64
			var hard bool
			_ = br.Scan(&id, &scope, &sid, &name, &period, &limit, &hard)
			budgets = append(budgets, map[string]any{"id": id, "scope": scope, "scope_id": sid, "name": name, "period": period, "limit_micro_eur": limit, "hard": hard})
		}
		br.Close()
	}
	byDot := group(`SELECT coalesce(d.name,'(system)'), ` + agg + ` ` + base + ` GROUP BY 1 ORDER BY 2 DESC`)
	sort.SliceStable(byDot, func(i, j int) bool { return byDot[i]["cost_micro_eur"].(int64) > byDot[j]["cost_micro_eur"].(int64) })
	writeJSON(w, 200, map[string]any{
		"by_dot":        byDot,
		"by_model":      group(`SELECT u.model, ` + agg + ` ` + base + ` GROUP BY 1 ORDER BY 2 DESC`),
		"by_deployment": group(`SELECT coalesce(nullif(u.deployment,''),'?'), ` + agg + ` ` + base + ` GROUP BY 1 ORDER BY 2 DESC`),
		"daily":         daily, "budgets": budgets,
	})
}

func (s *Server) putBudget(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope   string    `json:"scope"`
		ScopeID uuid.UUID `json:"scope_id"`
		Period  string    `json:"period"`
		Limit   int64     `json:"limit_micro_eur"`
		Hard    bool      `json:"hard"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if in.Scope == "workspace" {
		in.ScopeID = principal(r).WorkspaceID
	} else if in.Scope == "dot" && s.dotInWorkspace(r, in.ScopeID) != nil {
		problem(w, 400, "scope_id ungültig")
		return
	}
	_, err := s.Pool.Exec(r.Context(), `INSERT INTO budgets (id, scope, scope_id, period, limit_micro_eur, hard) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (scope, scope_id, period) DO UPDATE SET limit_micro_eur=EXCLUDED.limit_micro_eur, hard=EXCLUDED.hard`,
		uuid.Must(uuid.NewV7()), in.Scope, in.ScopeID, in.Period, in.Limit, in.Hard)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.audit(r, "budget.set", in.ScopeID.String(), map[string]any{"scope": in.Scope, "period": in.Period, "limit": in.Limit, "hard": in.Hard})
	writeJSON(w, 200, in)
}
