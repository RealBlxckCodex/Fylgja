package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/audit"
	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/pulse"
	"github.com/realblxckcodex/fylgja/internal/skills"
)

func (s *Server) routes(r chi.Router) {
	r.Post("/auth/logout", s.logout)
	r.Get("/auth/me", s.me)
	r.Post("/auth/stepup/password", s.stepUpPassword)
	r.Post("/auth/passkey/register/begin", s.passkeyRegisterBegin)
	r.Post("/auth/passkey/register/finish", s.passkeyRegisterFinish)
	r.Post("/auth/passkey/stepup/begin", s.passkeyStepUpBegin)
	r.Post("/auth/passkey/stepup/finish", s.passkeyStepUpFinish)
	r.Post("/auth/tokens", s.createToken)

	r.Get("/stream", s.stream)
	r.Get("/dots", s.listDots)
	r.With(need("manage")).Post("/dots", s.createDot)
	r.Get("/dots/{id}", s.getDot)
	r.With(need("manage")).Patch("/dots/{id}", s.patchDot)
	r.With(need("work")).Post("/dots/{id}/pair", s.pairDot)
	r.With(need("work")).Post("/dots/{id}/chat", s.chat)
	r.Get("/dots/{id}/messages", s.messages)
	r.Get("/dots/{id}/runs", s.listRuns)
	r.With(need("work")).Post("/messages/{mid}/feedback", s.messageFeedback)
	r.Get("/dots/{id}/memory", s.listMemory)
	r.With(need("work")).Post("/dots/{id}/memory", s.addMemory)
	r.Get("/dots/{id}/memory/export", s.exportMemory)
	r.With(need("work")).Patch("/memory/{mid}", s.patchMemory)
	r.With(need("work")).Delete("/memory/{mid}", s.deleteMemory)
	r.Get("/dots/{id}/computer", s.computerSession)
	r.Handle("/dots/{id}/computer/vnc/*", s.vncProxy())
	r.Get("/dots/{id}/links", s.listLinks)
	r.With(need("work"), stepUp).Post("/dots/{id}/links", s.createLink)
	r.With(need("work")).Delete("/links/{id}", s.revokeLink)
	r.Get("/runs/{id}", s.getRun)
	r.With(need("work")).Post("/runs/{id}/steer", s.steerRun)
	r.With(need("work")).Post("/runs/{id}/cancel", s.cancelRun)

	r.Get("/approvals", s.listApprovals)
	r.With(need("work")).Post("/approvals/{id}/resolve", s.resolveApproval)
	r.Get("/rules", s.listRules)
	r.With(need("manage")).Post("/rules", s.saveRule)
	r.With(need("manage")).Put("/rules/{id}", s.saveRule)
	r.With(need("manage")).Delete("/rules/{id}", s.deleteRule)
	r.With(need("manage")).Post("/rules/simulate", s.simulateRules)
	r.Get("/proposals", s.listProposals)
	r.With(need("work")).Post("/proposals/{id}/accept", s.decideProposal(true))
	r.With(need("work")).Post("/proposals/{id}/reject", s.decideProposal(false))

	r.Get("/tasks", s.listTasks)
	r.Get("/teams", s.listTeams)
	r.With(need("manage")).Post("/teams", s.createTeam)
	r.Get("/graphs", s.listGraphs)
	r.With(need("work")).Post("/graphs", s.planGraph)
	r.Get("/graphs/{id}", s.getGraph)
	r.With(need("work")).Post("/graphs/{id}/start", s.graphAction("start"))
	r.With(need("work")).Post("/graphs/{id}/cancel", s.graphAction("cancel"))
	r.With(need("work")).Post("/graphs/{id}/nodes/{node}/cancel", s.graphAction("cancel"))
	r.With(need("work")).Post("/graphs/{id}/nodes/{node}/retry", s.graphAction("retry"))
	r.With(need("work")).Post("/graphs/{id}/nodes/{node}/reassign", s.graphAction("reassign"))
	r.With(need("work")).Post("/graphs/{id}/nodes/{node}/steer", s.graphAction("steer"))
	r.With(need("work")).Post("/graphs/{id}/nodes/{node}/complete", s.graphAction("complete"))

	r.Get("/fleet/overview", s.fleetOverview)
	r.With(need("manage"), stepUp).Post("/fleet/nodes/{id}/drain", s.nodeAction("drain"))
	r.With(need("own"), stepUp).Post("/fleet/nodes/{id}/terminate", s.nodeAction("terminate"))
	r.With(need("own"), stepUp).Post("/fleet/pools/{pool}/provision", s.provision)
	r.With(need("own"), stepUp).Post("/fleet/nodes", s.addStaticNode)
	r.With(need("own"), stepUp).Put("/fleet/policies", s.setPolicy)
	r.Get("/usage", s.costs)
	r.With(need("own")).Put("/budgets", s.putBudget)

	r.Get("/skills", s.listSkills)
	r.With(need("manage"), stepUp).Post("/skills/{id}/activate", s.activateSkill)
	r.With(need("manage")).Post("/skills/{id}/disable", s.disableSkill)
	r.Get("/tools", s.listTools)
	r.Get("/channels", s.channelsHealth)
	r.With(need("audit")).Get("/audit", s.listAudit)
	r.With(need("audit")).Get("/audit/verify", s.verifyAudit)
	r.With(need("own")).Post("/emergency/pause-all", s.pauseAll)
	r.With(need("own")).Post("/emergency/resume-all", s.resumeAll)
	r.With(need("work")).Post("/pulse/{id}/run", s.pulseNow)
}

// ---- SSE ----

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok || s.Bus == nil {
		problem(w, 500, "streaming nicht unterstützt")
		return
	}
	topics := strings.Split(r.URL.Query().Get("topics"), ",")
	p := principal(r)
	// Nur Topics erlauben, die zum Workspace gehören (Dots/Runs werden beim Zustellen geprüft).
	var allowed []string
	for _, t := range topics {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "dot.") || strings.HasPrefix(t, "run.") || strings.HasPrefix(t, "graph.") {
			if s.topicAllowed(r.Context(), p.WorkspaceID, t) {
				allowed = append(allowed, t)
			}
			continue
		}
		switch t {
		case "approvals", "proposals", "alerts", "fleet.overview", "router.queues":
			allowed = append(allowed, t)
		}
	}
	if len(allowed) == 0 {
		problem(w, 400, "keine gültigen topics")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	sub := s.Bus.Subscribe(allowed...)
	defer s.Bus.Unsubscribe(sub)
	if last, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil && last > 0 {
		if evs, err := s.Bus.Replay(r.Context(), last, allowed); err == nil {
			for _, e := range evs {
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Topic, e.Payload)
			}
		}
	}
	fmt.Fprint(w, ": verbunden\n\n")
	fl.Flush()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	fleetTick := time.NewTicker(time.Second)
	defer fleetTick.Stop()
	wantFleet := false
	for _, t := range allowed {
		if t == "fleet.overview" || t == "router.queues" {
			wantFleet = true
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-sub.C:
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Topic, e.Payload)
			fl.Flush()
		case <-fleetTick.C:
			if wantFleet && s.Router != nil {
				b, _ := json.Marshal(map[string]any{"router": s.Router.Snapshot(), "at": time.Now()})
				fmt.Fprintf(w, "event: router.queues\ndata: %s\n\n", b)
				fl.Flush()
			}
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) topicAllowed(ctx context.Context, ws uuid.UUID, topic string) bool {
	parts := strings.SplitN(topic, ".", 3)
	if len(parts) < 2 {
		return false
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return false
	}
	var n int
	switch parts[0] {
	case "dot":
		_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM dots WHERE id=$1 AND workspace_id=$2`, id, ws).Scan(&n)
	case "run":
		_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM runs r JOIN dots d ON d.id=r.dot_id WHERE r.id=$1 AND d.workspace_id=$2`, id, ws).Scan(&n)
	case "graph":
		_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM work_graphs g JOIN dots d ON d.id=g.lead_dot_id WHERE g.id=$1 AND d.workspace_id=$2`, id, ws).Scan(&n)
	}
	return n > 0
}

// ---- Audit ----

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	list, err := s.Audit.List(r.Context(), 200, before)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	ws := principal(r).WorkspaceID
	out := []map[string]any{}
	for _, e := range list {
		if e.WorkspaceID != ws {
			continue
		}
		out = append(out, map[string]any{"id": e.ID, "actor": e.Actor, "action": e.Action, "target": e.Target, "detail": e.Detail, "at": e.CreatedAt, "hash": hex.EncodeToString(e.Hash)})
	}
	writeJSON(w, 200, out)
}

func (s *Server) verifyAudit(w http.ResponseWriter, r *http.Request) {
	n, err := s.Audit.VerifyAll(r.Context())
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "entries": n, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "entries": n})
}

// ---- Notbremse (18.3) ----

func (s *Server) pauseAll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hard bool `json:"hard"`
	}
	_ = decode(r, &in)
	ctx := r.Context()
	ws := principal(r).WorkspaceID
	_, _ = s.Pool.Exec(ctx, `UPDATE dots SET status='paused' WHERE workspace_id=$1 AND status='active'`, ws)
	stopped := 0
	if in.Hard {
		rows, err := s.Pool.Query(ctx, `SELECT r.id FROM runs r JOIN dots d ON d.id=r.dot_id WHERE d.workspace_id=$1 AND r.status IN ('running','queued','waiting')`, ws)
		if err == nil {
			var ids []uuid.UUID
			for rows.Next() {
				var id uuid.UUID
				_ = rows.Scan(&id)
				ids = append(ids, id)
			}
			rows.Close()
			for _, id := range ids {
				if s.Runtime.Cancel(ctx, id) == nil {
					stopped++
				}
			}
		}
	}
	s.audit(r, "emergency.pause_all", ws.String(), map[string]any{"hard": in.Hard, "stopped_runs": stopped})
	writeJSON(w, 200, map[string]any{"paused": true, "stopped_runs": stopped})
}

func (s *Server) resumeAll(w http.ResponseWriter, r *http.Request) {
	ws := principal(r).WorkspaceID
	_, _ = s.Pool.Exec(r.Context(), `UPDATE dots SET status='active' WHERE workspace_id=$1 AND status='paused'`, ws)
	s.audit(r, "emergency.resume_all", ws.String(), nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) pulseNow(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil || s.Pulse == nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	d, err := s.Runtime.Store.GetDot(r.Context(), id)
	if err != nil {
		problem(w, 404, err.Error())
		return
	}
	run, err := s.Pulse.StartPulse(r.Context(), d)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	if run == nil {
		writeJSON(w, 200, map[string]any{"run_id": nil, "hint": "keine neuen signale"})
		return
	}
	writeJSON(w, 202, map[string]any{"run_id": run.ID})
}

// ---- Webhooks (Signale, HMAC + Replay-Schutz) ----

// HookSecret leitet das Webhook-Secret einer Fylgja ab (in der UI anzeigbar).
func HookSecret(key []byte, dot uuid.UUID, source string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("hook|" + dot.String() + "|" + source))
	return hex.EncodeToString(m.Sum(nil))[:40]
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	dot, err := uuid.Parse(chi.URLParam(r, "dot"))
	source := chi.URLParam(r, "source")
	if err != nil || s.Pulse == nil {
		problem(w, 404, "unbekannt")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		problem(w, 400, "body")
		return
	}
	ts := r.Header.Get("X-Fylgja-Timestamp")
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(t, 0)).Abs() > 5*time.Minute {
		problem(w, 401, "timestamp fehlt oder zu alt (replay-schutz)")
		return
	}
	m := hmac.New(sha256.New, []byte(HookSecret(s.HookKey, dot, source)))
	m.Write([]byte(ts + "." + string(body)))
	want := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Fylgja-Signature"))) {
		problem(w, 401, "signatur ungültig")
		return
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		payload = map[string]any{"raw": string(body)}
	}
	key := r.Header.Get("X-Fylgja-Delivery")
	if key == "" {
		sum := sha256.Sum256(body)
		key = hex.EncodeToString(sum[:8])
	}
	ok, err := s.Pulse.AddSignal(r.Context(), dot, pulse.Signal{Source: "hook:" + source, Kind: "webhook", DedupKey: key, Payload: payload, Score: 0.6, Untrusted: true})
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"accepted": ok})
}

// ---- Router-Gateway (/router/v1, OpenAI-kompatibel, optional) ----

func (s *Server) routerGateway() http.Handler {
	r := chi.NewRouter()
	r.Post("/chat/completions", func(w http.ResponseWriter, req *http.Request) {
		if s.RouterToken == "" || s.Router == nil {
			problem(w, 404, "router-gateway deaktiviert")
			return
		}
		if !hmac.Equal([]byte(req.Header.Get("Authorization")), []byte("Bearer "+s.RouterToken)) {
			problem(w, 401, "token ungültig")
			return
		}
		var in struct {
			Model     string        `json:"model"`
			Messages  []llm.Message `json:"messages"`
			Stream    bool          `json:"stream"`
			MaxTokens int           `json:"max_tokens"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 8<<20)).Decode(&in); err != nil {
			problem(w, 400, err.Error())
			return
		}
		lr := llm.Request{Model: in.Model, Messages: in.Messages, MaxTokens: in.MaxTokens, Meta: llm.Meta{Priority: llm.TaskPrio, Privacy: llm.AnyPrivacy, Tier: "gateway"}}
		id := "chatcmpl-" + uuid.NewString()
		if !in.Stream {
			resp, err := s.Router.Chat(req.Context(), lr, nil)
			if err != nil {
				problem(w, 502, err.Error())
				return
			}
			writeJSON(w, 200, map[string]any{"id": id, "object": "chat.completion", "model": in.Model, "created": time.Now().Unix(),
				"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": resp.Message.Content}, "finish_reason": resp.FinishReason}},
				"usage":   map[string]int{"prompt_tokens": resp.Usage.In, "completion_tokens": resp.Usage.Out, "total_tokens": resp.Usage.In + resp.Usage.Out}})
			return
		}
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := s.Router.Chat(req.Context(), lr, func(d string) {
			b, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion.chunk", "model": in.Model, "choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": d}}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		})
		if err != nil {
			fmt.Fprintf(w, "data: {\"error\":%q}\n\n", err.Error())
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	return r
}

// ---- Computer (Live-View-Proxy, 13.5) ----

func (s *Server) computerSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	if s.Sandbox == nil || s.Sandbox.Provider == nil {
		writeJSON(w, 200, map[string]any{"available": false, "hint": "kein sandbox-provider konfiguriert (sandbox.provider)"})
		return
	}
	inst, _, err := s.Sandbox.Session(r.Context(), id)
	if err != nil {
		problem(w, 503, err.Error())
		return
	}
	s.audit(r, "computer.view", id.String(), nil)
	files, _ := s.Sandbox.List(r.Context(), id, ".")
	writeJSON(w, 200, map[string]any{"available": true, "vnc": inst.VNC != "", "vnc_path": "/api/v1/dots/" + id.String() + "/computer/vnc/websockify", "files": files})
}

func (s *Server) vncProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := pathUUID(r, "id")
		if err != nil || s.dotInWorkspace(r, id) != nil || s.Sandbox == nil {
			problem(w, 404, "nicht gefunden")
			return
		}
		inst, _, err := s.Sandbox.Session(r.Context(), id)
		if err != nil || inst.VNC == "" {
			problem(w, 503, "live-view nicht verfügbar")
			return
		}
		target, _ := url.Parse(inst.VNC)
		rp := httputil.NewSingleHostReverseProxy(target)
		rp.Director = func(req *http.Request) {
			req.URL.Scheme, req.URL.Host = target.Scheme, target.Host
			req.URL.Path = "/" + chi.URLParam(r, "*")
			req.Host = target.Host
			req.Header.Del("Cookie")
		}
		rp.ServeHTTP(w, r)
	})
}

// ---- Skills, Tools, Kanäle ----

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Pool.Query(r.Context(), `SELECT id, coalesce(dot_id::text,''), name, version, description, body_md, manifest, status, origin, signature IS NOT NULL, created_at
		FROM skills WHERE workspace_id=$1 ORDER BY name, version DESC`, principal(r).WorkspaceID)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var dot, name, desc, body, status, origin string
		var ver int
		var man []byte
		var signed bool
		var at time.Time
		_ = rows.Scan(&id, &dot, &name, &ver, &desc, &body, &man, &status, &origin, &signed, &at)
		out = append(out, map[string]any{"id": id, "dot_id": dot, "name": name, "version": ver, "description": desc, "body_md": body, "manifest": json.RawMessage(man),
			"status": status, "origin": origin, "signed": signed, "findings": skills.Scan(body, nil), "created_at": at})
	}
	writeJSON(w, 200, out)
}

func (s *Server) activateSkill(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	var ws uuid.UUID
	if s.Pool.QueryRow(r.Context(), `SELECT workspace_id FROM skills WHERE id=$1`, id).Scan(&ws) != nil || ws != principal(r).WorkspaceID {
		problem(w, 404, "nicht gefunden")
		return
	}
	st := &skills.Store{Pool: s.Pool, Master: s.SkillKey}
	findings, err := st.Activate(r.Context(), id)
	if err != nil {
		writeJSON(w, 422, map[string]any{"error": err.Error(), "findings": findings})
		return
	}
	s.audit(r, "skill.activate", id.String(), nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) disableSkill(w http.ResponseWriter, r *http.Request) {
	tag, err := s.Pool.Exec(r.Context(), `UPDATE skills SET status='disabled' WHERE id=$1 AND workspace_id=$2`, chiParam(r, "id"), principal(r).WorkspaceID)
	if err != nil || tag.RowsAffected() == 0 {
		problem(w, 404, "nicht gefunden")
		return
	}
	s.audit(r, "skill.disable", chiParam(r, "id"), nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, t := range s.Tools.All() {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "class": t.Class, "idempotent": t.Idempotent, "source": t.Source, "base": t.Base})
	}
	writeJSON(w, 200, out)
}

func (s *Server) channelsHealth(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	if s.Hub != nil {
		for k, h := range s.Hub.Channels() {
			out[k] = h
		}
	}
	writeJSON(w, 200, out)
}

// ---- Metriken (Prometheus-Textformat, 20.1) ----

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	ctx := r.Context()
	g := func(name, help string, v float64, labels string) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s%s %g\n", name, help, name, name, labels, v)
	}
	var running, waiting, queued, failed int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='running'), count(*) FILTER (WHERE status='waiting'), count(*) FILTER (WHERE status='queued'),
		count(*) FILTER (WHERE status='failed' AND finished_at > now()-interval '1 hour') FROM runs`).Scan(&running, &waiting, &queued, &failed)
	g("fylgja_runs_running", "Laufende Runs", float64(running), "")
	g("fylgja_runs_waiting", "Runs, die auf Freigaben warten", float64(waiting), "")
	g("fylgja_runs_queued", "Eingereihte Runs", float64(queued), "")
	g("fylgja_runs_failed_1h", "Fehlgeschlagene Runs (1 h)", float64(failed), "")
	var pending, outbox int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE status='pending'`).Scan(&pending)
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE status='pending'`).Scan(&outbox)
	g("fylgja_approvals_pending", "Offene Freigaben", float64(pending), "")
	g("fylgja_outbox_pending", "Ausstehende Kanal-Nachrichten", float64(outbox), "")
	var cost int64
	_ = s.Pool.QueryRow(ctx, `SELECT coalesce(sum(cost_micro_eur),0) FROM usage_events WHERE created_at >= date_trunc('day', now())`).Scan(&cost)
	g("fylgja_cost_today_eur", "Kosten heute (EUR)", float64(cost)/1e6, "")
	if s.Router != nil {
		snap := s.Router.Snapshot()
		for _, q := range snap.Queues {
			g("fylgja_router_queue_depth", "Warteschlange je Klasse", float64(q.Depth), fmt.Sprintf(`{class=%q}`, q.Class))
			g("fylgja_router_wait_p95_ms", "Wartezeit p95", q.WaitP95, fmt.Sprintf(`{class=%q}`, q.Class))
		}
		for _, d := range snap.Deployments {
			l := fmt.Sprintf(`{deployment=%q,model=%q}`, d.Name, d.Model)
			g("fylgja_deployment_inflight", "Laufende Anfragen", float64(d.Inflight), l)
			g("fylgja_deployment_latency_p95_ms", "Latenz p95", d.LatencyP95, l)
			g("fylgja_deployment_breaker_open", "Circuit Breaker offen", map[bool]float64{true: 1}[d.Breaker != "closed"], l)
		}
	}
	if s.Fleet != nil {
		for _, n := range s.Fleet.Nodes() {
			l := fmt.Sprintf(`{node=%q,state=%q}`, n.Name, n.State)
			g("fylgja_node_gpu_util", "GPU-Auslastung", n.Metrics.GPUUtil, l)
			g("fylgja_node_kv_cache_util", "KV-Cache-Auslastung", n.Metrics.KVCacheUtil, l)
		}
		g("fylgja_fleet_spend_today_eur", "Flottenkosten heute", float64(s.Fleet.SpendToday())/1e6, "")
	}
}

var _ = audit.Entry{}

// ---- Laptop-Link ----

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT id, name, capabilities, last_seen, revoked_at, created_at FROM links WHERE dot_id=$1 ORDER BY created_at DESC`, id)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var lid uuid.UUID
		var name string
		var caps []byte
		var seen, revoked *time.Time
		var created time.Time
		_ = rows.Scan(&lid, &name, &caps, &seen, &revoked, &created)
		online := seen != nil && time.Since(*seen) < 45*time.Second && revoked == nil
		out = append(out, map[string]any{"id": lid, "name": name, "capabilities": json.RawMessage(caps), "last_seen": seen, "revoked_at": revoked, "online": online, "created_at": created})
	}
	writeJSON(w, 200, out)
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil || s.Links == nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	_ = decode(r, &in)
	p := principal(r)
	lid, tok, err := s.Links.Create(r.Context(), p.WorkspaceID, p.UserID, id, firstNonEmpty(in.Name, "Laptop"))
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.audit(r, "link.create", lid.String(), map[string]any{"dot": id})
	url := strings.Replace(strings.Replace(s.BaseURL, "https://", "wss://", 1), "http://", "ws://", 1) + "/api/v1/link/tunnel"
	writeJSON(w, 201, map[string]any{"id": lid, "token": tok, "server": url,
		"command": fmt.Sprintf("fylgja-link -server %s -id %s -token %s", url, lid, tok), "hint": "Token wird nur einmal angezeigt."})
}

func (s *Server) revokeLink(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.Links == nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	var dot uuid.UUID
	if s.Pool.QueryRow(r.Context(), `SELECT dot_id FROM links WHERE id=$1`, id).Scan(&dot) != nil || s.dotInWorkspace(r, dot) != nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	if err := s.Links.Revoke(r.Context(), id); err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.audit(r, "link.revoke", id.String(), nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}
