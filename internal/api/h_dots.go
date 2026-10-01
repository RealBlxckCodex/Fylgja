package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/channels"
	"github.com/realblxckcodex/fylgja/internal/memory"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/runtime"
)

func (s *Server) listDots(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	rows, err := s.Pool.Query(r.Context(), `SELECT d.id FROM dots d WHERE d.workspace_id=$1 AND d.status <> 'archived' ORDER BY d.created_at`, p.WorkspaceID)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := []map[string]any{}
	for _, id := range ids {
		d, err := s.Runtime.Store.GetDot(r.Context(), id)
		if err != nil {
			continue
		}
		out = append(out, s.dotView(r.Context(), d))
	}
	writeJSON(w, 200, out)
}

func (s *Server) dotView(ctx context.Context, d *runtime.Dot) map[string]any {
	var running, waiting, pending int
	var spent int64
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='running'), count(*) FILTER (WHERE status='waiting') FROM runs WHERE dot_id=$1`, d.ID).Scan(&running, &waiting)
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE dot_id=$1 AND status='pending'`, d.ID).Scan(&pending)
	_ = s.Pool.QueryRow(ctx, `SELECT coalesce(sum(cost_micro_eur),0) FROM usage_events WHERE dot_id=$1 AND created_at >= date_trunc('day', now())`, d.ID).Scan(&spent)
	var sandboxState string
	_ = s.Pool.QueryRow(ctx, `SELECT state FROM sandboxes WHERE dot_id=$1`, d.ID).Scan(&sandboxState)
	return map[string]any{"dot": d, "running": running, "waiting": waiting, "pending_approvals": pending, "cost_today_micro_eur": spent, "sandbox": sandboxState}
}

func (s *Server) createDot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Persona  string `json:"persona"`
		Charter  string `json:"charter"`
		Autonomy int    `json:"autonomy_level"`
		Privacy  string `json:"privacy_mode"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	p := principal(r)
	if in.Name == "" {
		problem(w, 400, "name fehlt")
		return
	}
	kind := firstNonEmpty(in.Kind, "personal")
	if kind != "personal" && kind != "specialist" {
		problem(w, 400, "kind: personal|specialist")
		return
	}
	id := ids.New()
	var owner any = p.UserID
	auto := min(max(in.Autonomy, 0), 1) // höhere Stufen nur per PATCH mit Step-up
	if kind == "specialist" {
		owner, auto = nil, 0 // Specialists starten im Shadow-Mode (15.4)
	}
	_, err := s.Pool.Exec(r.Context(), `INSERT INTO dots (id, workspace_id, name, kind, owner_user_id, persona, charter, autonomy_level, privacy_mode, pulse_config, quiet_hours)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'{"interval_min":30}','{"start":"22:00","end":"07:00"}')`,
		id, p.WorkspaceID, in.Name, kind, owner, in.Persona, in.Charter, auto, firstNonEmpty(in.Privacy, "self_hosted_only"))
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.audit(r, "dot.create", id.String(), map[string]any{"name": in.Name, "kind": kind})
	d, _ := s.Runtime.Store.GetDot(r.Context(), id)
	writeJSON(w, 201, d)
}

func (s *Server) getDot(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	d, err := s.Runtime.Store.GetDot(r.Context(), id)
	if err != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	writeJSON(w, 200, s.dotView(r.Context(), d))
}

// patchDot: Autonomie-Erhöhung und Privacy-Lockerung erfordern Step-up.
func (s *Server) patchDot(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	var in struct {
		Name        *string           `json:"name"`
		Persona     *string           `json:"persona"`
		Charter     *string           `json:"charter"`
		Autonomy    *int              `json:"autonomy_level"`
		Privacy     *string           `json:"privacy_mode"`
		PulseConfig *json.RawMessage  `json:"pulse_config"`
		QuietHours  *json.RawMessage  `json:"quiet_hours"`
		Status      *string           `json:"status"`
		Tiers       map[string]string `json:"tiers"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	cur, err := s.Runtime.Store.GetDot(r.Context(), id)
	if err != nil {
		problem(w, 404, err.Error())
		return
	}
	p := principal(r)
	loosen := (in.Autonomy != nil && *in.Autonomy > cur.Autonomy) || (in.Privacy != nil && privRank(*in.Privacy) < privRank(cur.PrivacyMode))
	if loosen && !stepped(p) {
		w.Header().Set("X-Fylgja-Step-Up", "required")
		problem(w, 428, "step-up erforderlich (mehr autonomie / weniger privacy)")
		return
	}
	ctx := r.Context()
	upd := func(col string, v any) {
		if _, e := s.Pool.Exec(ctx, `UPDATE dots SET `+col+`=$2 WHERE id=$1`, id, v); e != nil && err == nil {
			err = e
		}
	}
	err = nil
	if in.Name != nil {
		upd("name", *in.Name)
	}
	if in.Persona != nil {
		upd("persona", *in.Persona)
	}
	if in.Charter != nil {
		upd("charter", *in.Charter)
	}
	if in.Autonomy != nil {
		upd("autonomy_level", min(max(*in.Autonomy, 0), 3))
	}
	if in.Privacy != nil {
		upd("privacy_mode", *in.Privacy)
	}
	if in.PulseConfig != nil {
		upd("pulse_config", []byte(*in.PulseConfig))
	}
	if in.QuietHours != nil {
		upd("quiet_hours", []byte(*in.QuietHours))
	}
	if in.Status != nil {
		upd("status", *in.Status)
	}
	if in.Tiers != nil {
		tb, _ := json.Marshal(in.Tiers)
		var mp uuid.UUID
		if cur2 := s.Pool.QueryRow(ctx, `SELECT model_profile_id FROM dots WHERE id=$1 AND model_profile_id IS NOT NULL`, id).Scan(&mp); cur2 != nil {
			mp = ids.New()
			_, _ = s.Pool.Exec(ctx, `INSERT INTO model_profiles (id, workspace_id, name, tiers) VALUES ($1,$2,$3,$4)`, mp, p.WorkspaceID, cur.Name, tb)
			upd("model_profile_id", mp)
		} else {
			_, _ = s.Pool.Exec(ctx, `UPDATE model_profiles SET tiers=$2 WHERE id=$1`, mp, tb)
		}
	}
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	b, _ := json.Marshal(in)
	var detail map[string]any
	_ = json.Unmarshal(b, &detail)
	s.audit(r, "dot.update", id.String(), detail)
	d, _ := s.Runtime.Store.GetDot(ctx, id)
	writeJSON(w, 200, d)
}

func privRank(p string) int {
	switch p {
	case "any":
		return 0
	case "eu_only":
		return 1
	}
	return 2
}

func (s *Server) pairDot(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	p := principal(r)
	code, exp, err := channels.NewPairingCode(r.Context(), s.Pool, p.WorkspaceID, p.UserID, id)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.audit(r, "channel.pairing_code", id.String(), nil)
	writeJSON(w, 201, map[string]any{"code": code, "expires_at": exp, "hint": "Sende diesen Code per DM an den Bot (Telegram oder Discord)."})
}

// chat: Web-Kanal. Antwort kommt per SSE (Topic run.<id>).
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	var in struct {
		Text   string   `json:"text"`
		Images []string `json:"images"`
		Tier   string   `json:"tier"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
		problem(w, 400, "text fehlt")
		return
	}
	p := principal(r)
	if !p.Can("work") {
		problem(w, 403, "keine berechtigung")
		return
	}
	ctx := r.Context()
	d, _ := s.Runtime.Store.GetDot(ctx, id)
	trust := "member"
	if d != nil && d.OwnerUserID != nil && *d.OwnerUserID == p.UserID || (d != nil && d.OwnerUserID == nil && p.Can("manage")) {
		trust = "owner"
	}
	conv, err := s.Runtime.Store.EnsureConversation(ctx, id, "web", p.UserID.String(), "", "web")
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	if trust == "owner" {
		if runID, ok := s.Runtime.ActiveRunFor(ctx, conv); ok && s.Runtime.Steer(runID, in.Text) {
			_ = s.Runtime.Store.SaveMessage(ctx, &runtime.StoredMessage{ConversationID: conv, RunID: &runID, Role: "user", Text: in.Text, Trust: trust, Author: p.DisplayName})
			writeJSON(w, 202, map[string]any{"run_id": runID, "steered": true})
			return
		}
	}
	tier := firstNonEmpty(in.Tier, "worker")
	run := &runtime.Run{DotID: id, ConversationID: &conv, Kind: runtime.KindChat, Tier: tier,
		Input: runtime.Input{Text: in.Text, Images: in.Images, Trust: trust, Author: p.DisplayName, Channel: "web"}}
	if err := s.Runtime.Store.CreateRun(ctx, run); err != nil {
		problem(w, 500, err.Error())
		return
	}
	_ = s.Runtime.Store.SaveMessage(ctx, &runtime.StoredMessage{ConversationID: conv, RunID: &run.ID, Role: "user", Text: in.Text, Trust: trust, Author: p.DisplayName})
	if err := s.Runtime.Submit(ctx, run); err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"run_id": run.ID, "conversation_id": conv})
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	p := principal(r)
	conv, err := s.Runtime.Store.EnsureConversation(r.Context(), id, "web", p.UserID.String(), "", "web")
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	msgs, err := s.Runtime.Store.RecentMessages(r.Context(), conv, 100, nil)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	// Je Assistenten-Nachricht: ausgeführte Schritte (Tool-Namen) und Gesamtdauer des Runs.
	type stepInfo struct {
		Tools      []string `json:"tools"`
		DurationMS int64    `json:"duration_ms"`
		Tainted    bool     `json:"tainted"`
		Model      string   `json:"model"`
	}
	steps := map[string]stepInfo{}
	for _, m := range msgs {
		if m.RunID == nil || m.Role != "assistant" {
			continue
		}
		si := stepInfo{Tools: []string{}}
		rows, err := s.Pool.Query(r.Context(), `SELECT payload->>'tool' FROM run_events WHERE run_id=$1 AND type='tool_call' ORDER BY seq`, *m.RunID)
		if err == nil {
			for rows.Next() {
				var t string
				_ = rows.Scan(&t)
				si.Tools = append(si.Tools, t)
			}
			rows.Close()
		}
		_ = s.Pool.QueryRow(r.Context(), `SELECT coalesce(extract(epoch FROM (finished_at - coalesce(started_at, created_at)))*1000, 0)::bigint, tainted FROM runs WHERE id=$1`, *m.RunID).Scan(&si.DurationMS, &si.Tainted)
		_ = s.Pool.QueryRow(r.Context(), `SELECT coalesce(payload->>'model','') FROM run_events WHERE run_id=$1 AND type='model_request' ORDER BY seq DESC LIMIT 1`, *m.RunID).Scan(&si.Model)
		steps[m.ID.String()] = si
	}
	var fb []struct {
		MessageID string `json:"message_id"`
		Kind      string `json:"kind"`
	}
	if frows, err := s.Pool.Query(r.Context(), `SELECT message_id::text, kind FROM feedback WHERE message_id IN (SELECT id FROM messages WHERE conversation_id=$1) AND kind IN ('thumb_up','thumb_down')`, conv); err == nil {
		for frows.Next() {
			var x struct {
				MessageID string `json:"message_id"`
				Kind      string `json:"kind"`
			}
			_ = frows.Scan(&x.MessageID, &x.Kind)
			fb = append(fb, x)
		}
		frows.Close()
	}
	writeJSON(w, 200, map[string]any{"conversation_id": conv, "messages": msgs, "steps": steps, "feedback": fb})
}

// messageFeedback speichert 👍/👎 auf einer Antwort (Lernschleife, 11.5).
func (s *Server) messageFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "mid")
	if err != nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	var in struct {
		Kind    string `json:"kind"`
		Comment string `json:"comment"`
	}
	if err := decode(r, &in); err != nil || (in.Kind != "thumb_up" && in.Kind != "thumb_down" && in.Kind != "none") {
		problem(w, 400, "kind: thumb_up|thumb_down|none")
		return
	}
	var dot uuid.UUID
	var run *uuid.UUID
	if s.Pool.QueryRow(r.Context(), `SELECT c.dot_id, m.run_id FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE m.id=$1`, id).Scan(&dot, &run) != nil || s.dotInWorkspace(r, dot) != nil {
		problem(w, 404, "nachricht nicht gefunden")
		return
	}
	_, _ = s.Pool.Exec(r.Context(), `DELETE FROM feedback WHERE message_id=$1 AND kind IN ('thumb_up','thumb_down')`, id)
	if in.Kind != "none" {
		_, err = s.Pool.Exec(r.Context(), `INSERT INTO feedback (id, dot_id, run_id, message_id, kind, payload) VALUES ($1,$2,$3,$4,$5,$6)`,
			uuid.Must(uuid.NewV7()), dot, run, id, in.Kind, map[string]any{"via": "web", "comment": in.Comment})
		if err != nil {
			problem(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.Runtime.Store.ListRuns(r.Context(), id, limit)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	type view struct {
		*runtime.Run
		Tokens int64 `json:"tokens"`
		Cost   int64 `json:"cost_micro_eur"`
	}
	out := []view{}
	for _, run := range runs {
		t, c, _ := s.Runtime.Store.RunUsage(r.Context(), run.ID)
		run.Input.Images = nil
		out = append(out, view{run, t, c})
	}
	writeJSON(w, 200, out)
}

func (s *Server) runOf(r *http.Request) (*runtime.Run, error) {
	id, err := pathUUID(r, "id")
	if err != nil {
		return nil, err
	}
	run, err := s.Runtime.Store.GetRun(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if s.dotInWorkspace(r, run.DotID) != nil {
		return nil, errForbidden
	}
	return run, nil
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.runOf(r)
	if err != nil {
		problem(w, 404, "run nicht gefunden")
		return
	}
	evs, _ := s.Runtime.Store.Events(r.Context(), run.ID)
	var decisions []map[string]any
	rows, err := s.Pool.Query(r.Context(), `SELECT logical_model, coalesce(d.name,''), reason, queue_wait_ms, rd.created_at FROM routing_decisions rd LEFT JOIN deployments d ON d.id=rd.deployment_id WHERE run_id=$1 ORDER BY rd.id`, run.ID)
	if err == nil {
		for rows.Next() {
			var model, dep, reason string
			var wait int
			var at any
			_ = rows.Scan(&model, &dep, &reason, &wait, &at)
			decisions = append(decisions, map[string]any{"model": model, "deployment": dep, "reason": reason, "queue_wait_ms": wait, "at": at})
		}
		rows.Close()
	}
	t, c, _ := s.Runtime.Store.RunUsage(r.Context(), run.ID)
	writeJSON(w, 200, map[string]any{"run": run, "events": evs, "routing": decisions, "tokens": t, "cost_micro_eur": c})
}

func (s *Server) steerRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.runOf(r)
	if err != nil {
		problem(w, 404, "run nicht gefunden")
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if !s.Runtime.Steer(run.ID, in.Text) {
		problem(w, 409, "run ist nicht aktiv")
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true})
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.runOf(r)
	if err != nil {
		problem(w, 404, "run nicht gefunden")
		return
	}
	if err := s.Runtime.Cancel(r.Context(), run.ID); err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.audit(r, "run.cancel", run.ID.String(), nil)
	writeJSON(w, 202, map[string]any{"ok": true})
}

// ---- Memory ----

func (s *Server) listMemory(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	q := r.URL.Query()
	var list []*memory.Memory
	if query := q.Get("q"); query != "" {
		list, err = s.Memory.Search(r.Context(), id, query, memory.SearchOptions{K: 30})
	} else {
		list, err = s.Memory.List(r.Context(), id, memory.Tier(q.Get("tier")), 200)
	}
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []*memory.Memory{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) addMemory(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	var in struct {
		Tier        string  `json:"tier"`
		Content     string  `json:"content"`
		Importance  float64 `json:"importance"`
		Sensitivity string  `json:"sensitivity"`
		Pinned      bool    `json:"pinned"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	m, err := s.Memory.Write(r.Context(), memory.WriteRequest{DotID: id, Tier: memory.Tier(in.Tier), Content: in.Content, Importance: in.Importance,
		Sensitivity: in.Sensitivity, Origin: memory.FromOwner, Direct: true, Pinned: in.Pinned, Source: map[string]any{"via": "web", "user": principal(r).UserID}})
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.audit(r, "memory.write", m.ID.String(), map[string]any{"tier": in.Tier})
	writeJSON(w, 201, m)
}

func (s *Server) memoryOf(r *http.Request) (dot, id uuid.UUID, err error) {
	id, err = pathUUID(r, "mid")
	if err != nil {
		return
	}
	err = s.Pool.QueryRow(r.Context(), `SELECT dot_id FROM memories WHERE id=$1`, id).Scan(&dot)
	if err == nil {
		err = s.dotInWorkspace(r, dot)
	}
	return
}

func (s *Server) patchMemory(w http.ResponseWriter, r *http.Request) {
	dot, id, err := s.memoryOf(r)
	if err != nil {
		problem(w, 404, "eintrag nicht gefunden")
		return
	}
	var in struct {
		Content    *string  `json:"content"`
		Pinned     *bool    `json:"pinned"`
		Importance *float64 `json:"importance"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	m, err := s.Memory.Update(r.Context(), dot, id, in.Content, in.Pinned, in.Importance)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.audit(r, "memory.update", id.String(), nil)
	writeJSON(w, 200, m)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	dot, id, err := s.memoryOf(r)
	if err != nil {
		problem(w, 404, "eintrag nicht gefunden")
		return
	}
	n, err := s.Memory.Forget(r.Context(), dot, []uuid.UUID{id})
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.audit(r, "memory.delete", id.String(), map[string]any{"deleted": n}) // Tombstone ohne Inhalt
	writeJSON(w, 200, map[string]any{"deleted": n})
}

func (s *Server) exportMemory(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	files, err := s.Memory.Export(r.Context(), id)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, files)
}

// ---- Approvals ----

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	list, err := s.Runtime.Store.ListApprovals(r.Context(), principal(r).WorkspaceID, r.URL.Query().Get("status"), 200)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []*policy.Approval{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	a, err := s.Runtime.Store.GetApproval(r.Context(), id)
	if err != nil || s.dotInWorkspace(r, uuid.MustParse(a.DotID)) != nil {
		problem(w, 404, "nicht gefunden")
		return
	}
	var in struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	p := principal(r)
	if !p.Can("work") || (s.Hub != nil && !s.canApprove(r, p.UserID, a)) {
		problem(w, 403, "keine freigabeberechtigung")
		return
	}
	res := policy.Resolution{UserID: p.UserID.String(), Via: "web", Approve: in.Approve, Reason: in.Reason, StepUpDone: stepped(p)}
	got, err := s.Runtime.ResolveApproval(r.Context(), id, res)
	switch err {
	case nil:
	case policy.ErrStepUp:
		w.Header().Set("X-Fylgja-Step-Up", "required")
		problem(w, 428, "step-up erforderlich")
		return
	case policy.ErrSameUser:
		problem(w, 409, "vier-augen-prinzip: zweite person muss freigeben")
		return
	case policy.ErrNotPending:
		problem(w, 409, "bereits entschieden oder abgelaufen")
		return
	default:
		problem(w, 500, err.Error())
		return
	}
	if s.Hub != nil {
		s.Hub.ApprovalResolved(r.Context(), got)
	}
	writeJSON(w, 200, got)
}

func (s *Server) canApprove(r *http.Request, user uuid.UUID, a *policy.Approval) bool {
	var owner *uuid.UUID
	_ = s.Pool.QueryRow(r.Context(), `SELECT owner_user_id FROM dots WHERE id=$1`, a.DotID).Scan(&owner)
	if owner != nil && *owner == user {
		return true
	}
	var n int
	_ = s.Pool.QueryRow(r.Context(), `SELECT count(*) FROM dot_grants WHERE dot_id=$1 AND user_id=$2 AND grant_name=$3`, a.DotID, user, "approve:"+string(a.Class)).Scan(&n)
	if n > 0 {
		return true
	}
	return owner == nil && principal(r).Can("manage")
}
