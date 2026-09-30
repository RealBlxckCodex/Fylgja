package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/policy"
)

func timeNow() time.Time { return time.Now() }

type ruleIn struct {
	Name             string `json:"name"`
	DotID            string `json:"dot_id"`
	Expr             string `json:"expr"`
	Effect           string `json:"effect"`
	Priority         int    `json:"priority"`
	Enabled          *bool  `json:"enabled"`
	AllowWhenTainted bool   `json:"allow_when_tainted"`
	FourEyes         bool   `json:"four_eyes"`
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Pool.Query(r.Context(), `SELECT id, name, coalesce(dot_id::text,''), expr, effect, priority, enabled, allow_when_tainted, four_eyes, version, created_at
		FROM rules WHERE workspace_id=$1 ORDER BY priority DESC, created_at`, principal(r).WorkspaceID)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var name, dot, expr, effect string
		var prio, ver int
		var en, awt, fe bool
		var at time.Time
		_ = rows.Scan(&id, &name, &dot, &expr, &effect, &prio, &en, &awt, &fe, &ver, &at)
		out = append(out, map[string]any{"id": id, "name": name, "dot_id": dot, "expr": expr, "effect": effect, "priority": prio, "enabled": en,
			"allow_when_tainted": awt, "four_eyes": fe, "version": ver, "created_at": at})
	}
	writeJSON(w, 200, map[string]any{"rules": out, "matrix": policy.Matrix, "classes": policy.AllClasses})
}

// broadAllow: allow-Regeln ohne Tool-Bezug oder mit allow_when_tainted gelten als breit → Step-up.
func broadAllow(in ruleIn) bool {
	return in.Effect == "allow" && (in.AllowWhenTainted || !containsTool(in.Expr))
}

func containsTool(expr string) bool {
	return strings.Contains(expr, "action.tool ==") || strings.Contains(expr, "action.tool.startsWith")
}

func (s *Server) saveRule(w http.ResponseWriter, r *http.Request) {
	var in ruleIn
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	enabled := in.Enabled == nil || *in.Enabled
	if _, err := policy.Compile(policy.Rule{Name: in.Name, Expr: in.Expr, Effect: policy.Effect(in.Effect)}); err != nil {
		problem(w, 400, err.Error())
		return
	}
	p := principal(r)
	if broadAllow(in) && !p.SteppedUp(timeNow()) {
		w.Header().Set("X-Fylgja-Step-Up", "required")
		problem(w, 428, "breite allow-regel: step-up erforderlich")
		return
	}
	var dot any
	if in.DotID != "" {
		d, err := uuid.Parse(in.DotID)
		if err != nil || s.dotInWorkspace(r, d) != nil {
			problem(w, 400, "dot_id ungültig")
			return
		}
		dot = d
	}
	id := chiParam(r, "id")
	var err error
	if id == "" {
		nid := ids.New()
		_, err = s.Pool.Exec(r.Context(), `INSERT INTO rules (id, workspace_id, dot_id, name, expr, effect, priority, enabled, allow_when_tainted, four_eyes, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, nid, p.WorkspaceID, dot, in.Name, in.Expr, in.Effect, in.Priority, enabled, in.AllowWhenTainted, in.FourEyes, p.UserID)
		id = nid.String()
	} else {
		_, err = s.Pool.Exec(r.Context(), `UPDATE rules SET dot_id=$3, name=$4, expr=$5, effect=$6, priority=$7, enabled=$8, allow_when_tainted=$9, four_eyes=$10, version=version+1
			WHERE id=$1 AND workspace_id=$2`, id, p.WorkspaceID, dot, in.Name, in.Expr, in.Effect, in.Priority, enabled, in.AllowWhenTainted, in.FourEyes)
	}
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.audit(r, "rule.save", id, map[string]any{"name": in.Name, "expr": in.Expr, "effect": in.Effect, "allow_when_tainted": in.AllowWhenTainted, "four_eyes": in.FourEyes})
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	tag, err := s.Pool.Exec(r.Context(), `DELETE FROM rules WHERE id=$1 AND workspace_id=$2`, chiParam(r, "id"), principal(r).WorkspaceID)
	if err != nil || tag.RowsAffected() == 0 {
		problem(w, 404, "regel nicht gefunden")
		return
	}
	s.audit(r, "rule.delete", chiParam(r, "id"), nil)
	w.WriteHeader(204)
}

// simulate: "Was wäre passiert?" – neues Regelwerk gegen die letzten Tool-Entscheidungen.
func (s *Server) simulateRules(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rules []policy.Rule `json:"rules"`
		Days  int           `json:"days"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	for i := range in.Rules {
		if in.Rules[i].ID == "" {
			in.Rules[i].ID = in.Rules[i].Name
		}
	}
	set, err := policy.CompileAll(in.Rules)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	days := in.Days
	if days <= 0 || days > 90 {
		days = 14
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT e.run_id, e.seq, e.payload, p.payload, ru.tainted, d.autonomy_level, ru.kind, ru.tool_scope, d.id
		FROM run_events e
		JOIN run_events p ON p.run_id=e.run_id AND p.type='policy' AND p.payload->>'call_id' = e.payload->>'call_id'
		JOIN runs ru ON ru.id=e.run_id JOIN dots d ON d.id=ru.dot_id
		WHERE e.type='tool_call' AND d.workspace_id=$1 AND e.created_at > now() - make_interval(days => $2)
		ORDER BY e.created_at DESC LIMIT 500`, principal(r).WorkspaceID, days)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	var hist []policy.HistoricalAction
	for rows.Next() {
		var run, dot uuid.UUID
		var seq, autonomy int
		var call, pol []byte
		var tainted bool
		var kind, scope string
		_ = rows.Scan(&run, &seq, &call, &pol, &tainted, &autonomy, &kind, &scope, &dot)
		var c struct {
			Tool  string          `json:"tool"`
			Class string          `json:"class"`
			Args  json.RawMessage `json:"args"`
		}
		var p struct {
			Decision policy.Decision `json:"decision"`
		}
		_ = json.Unmarshal(call, &c)
		_ = json.Unmarshal(pol, &p)
		var args map[string]any
		_ = json.Unmarshal(c.Args, &args)
		hist = append(hist, policy.HistoricalAction{ID: run.String() + ":" + itoa(seq),
			Action:   policy.Action{Tool: c.Tool, Class: policy.Class(c.Class), Args: args},
			Context:  policy.Context{DotID: dot.String(), Autonomy: autonomy, Tainted: tainted, Trigger: kind, Scope: policy.Scope(scope)},
			Previous: p.Decision.Verdict})
	}
	rows.Close()
	res := policy.Simulate(set, hist)
	changed := 0
	for _, x := range res {
		if x.Changed {
			changed++
		}
	}
	writeJSON(w, 200, map[string]any{"results": res, "total": len(res), "changed": changed})
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// ---- Proposals (Lernen-Inbox) ----

func (s *Server) listProposals(w http.ResponseWriter, r *http.Request) {
	status := firstNonEmpty(r.URL.Query().Get("status"), "open")
	rows, err := s.Pool.Query(r.Context(), `SELECT p.id, p.dot_id, d.name, p.type, p.payload, p.evidence, p.status, p.created_at FROM proposals p JOIN dots d ON d.id=p.dot_id
		WHERE d.workspace_id=$1 AND p.status=$2 ORDER BY p.created_at DESC LIMIT 200`, principal(r).WorkspaceID, status)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, dot uuid.UUID
		var name, typ, st string
		var payload, evidence []byte
		var at time.Time
		_ = rows.Scan(&id, &dot, &name, &typ, &payload, &evidence, &st, &at)
		out = append(out, map[string]any{"id": id, "dot_id": dot, "dot_name": name, "type": typ, "payload": json.RawMessage(payload), "evidence": json.RawMessage(evidence), "status": st, "created_at": at})
	}
	var acc, total int
	_ = s.Pool.QueryRow(r.Context(), `SELECT count(*) FILTER (WHERE p.status='accepted'), count(*) FILTER (WHERE p.status IN ('accepted','rejected'))
		FROM proposals p JOIN dots d ON d.id=p.dot_id WHERE d.workspace_id=$1`, principal(r).WorkspaceID).Scan(&acc, &total)
	rate := 0.0
	if total > 0 {
		rate = float64(acc) / float64(total)
	}
	writeJSON(w, 200, map[string]any{"proposals": out, "acceptance_rate": rate})
}

// decideProposal: akzeptierte Proposals werden versioniert angewendet (Regel, Memory, Skill).
func (s *Server) decideProposal(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathUUID(r, "id")
		if err != nil {
			problem(w, 404, "nicht gefunden")
			return
		}
		ctx := r.Context()
		p := principal(r)
		var dot uuid.UUID
		var typ string
		var payload []byte
		err = s.Pool.QueryRow(ctx, `SELECT p.dot_id, p.type, p.payload FROM proposals p JOIN dots d ON d.id=p.dot_id WHERE p.id=$1 AND d.workspace_id=$2 AND p.status='open'`,
			id, p.WorkspaceID).Scan(&dot, &typ, &payload)
		if err != nil {
			problem(w, 404, "proposal nicht gefunden oder bereits entschieden")
			return
		}
		var pl map[string]any
		_ = json.Unmarshal(payload, &pl)
		if accept {
			switch typ {
			case "rule":
				in := ruleIn{Name: str(pl["name"]), Expr: str(pl["expr"]), Effect: firstNonEmpty(str(pl["effect"]), "allow"), DotID: dot.String()}
				if broadAllow(in) && !p.SteppedUp(timeNow()) {
					w.Header().Set("X-Fylgja-Step-Up", "required")
					problem(w, 428, "step-up erforderlich")
					return
				}
				if _, err := policy.Compile(policy.Rule{Name: in.Name, Expr: in.Expr, Effect: policy.Effect(in.Effect)}); err != nil {
					problem(w, 400, "regel ungültig: "+err.Error())
					return
				}
				_, err = s.Pool.Exec(ctx, `INSERT INTO rules (id, workspace_id, dot_id, name, expr, effect, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
					ids.New(), p.WorkspaceID, dot, in.Name, in.Expr, in.Effect, p.UserID)
			case "memory_edit", "preference":
				if str(pl["action"]) == "forget" {
					_, err = s.Memory.ForgetTopic(ctx, dot, str(pl["topic"]))
				} else if c := firstNonEmpty(str(pl["new"]), str(pl["text"])); c != "" {
					tier := "core"
					if typ == "preference" {
						tier = "procedural"
					}
					_, err = s.Pool.Exec(ctx, `INSERT INTO memories (id, dot_id, tier, content, origin, pinned, source) VALUES ($1,$2,$3,$4,'owner',$5,$6)`,
						ids.New(), dot, tier, c, tier == "core", map[string]any{"proposal": id})
					if oid := str(pl["old_id"]); oid != "" && err == nil {
						_, err = s.Pool.Exec(ctx, `UPDATE memories SET valid_to=now() WHERE id=$1 AND dot_id=$2`, oid, dot)
					}
				}
			case "skill":
				_, err = s.Pool.Exec(ctx, `INSERT INTO skills (id, workspace_id, dot_id, name, description, body_md, manifest, status, origin)
					VALUES ($1,$2,$3,$4,$5,$6,$7,'draft','dot_authored')`, ids.New(), p.WorkspaceID, dot, str(pl["name"]), str(pl["description"]), str(pl["body_md"]),
					map[string]any{"tools": pl["tools"], "egress": pl["egress"]})
			}
			if err != nil {
				problem(w, 400, err.Error())
				return
			}
		}
		status := map[bool]string{true: "accepted", false: "rejected"}[accept]
		_, _ = s.Pool.Exec(ctx, `UPDATE proposals SET status=$2 WHERE id=$1`, id, status)
		_, _ = s.Pool.Exec(ctx, `INSERT INTO feedback (id, dot_id, kind, payload) VALUES ($1,$2,$3,$4)`, ids.New(), dot,
			map[bool]string{true: "approval_always", false: "approval_denied"}[accept], map[string]any{"proposal": id, "type": typ})
		s.audit(r, "proposal."+status, id.String(), map[string]any{"type": typ})
		writeJSON(w, 200, map[string]any{"status": status})
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
