package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/audit"
	"github.com/realblxckcodex/fylgja/internal/runtime"
)

// command behandelt Slash-Commands. true = erledigt (kein Chat-Run).
func (h *Hub) command(ctx context.Context, ev InboundEvent, id *identity, dot uuid.UUID, trust string) bool {
	st := h.Engine.Store
	d, err := st.GetDot(ctx, dot)
	if err != nil {
		return true
	}
	ownerOnly := func() bool {
		if trust != "owner" {
			h.reply(ctx, ev, "Nur der Owner darf das.")
			return false
		}
		return true
	}
	switch ev.Command {
	case "start", "help", "hilfe":
		h.reply(ctx, ev, fmt.Sprintf("Ich bin %s. Schreib mir einfach, was ich tun soll.\nBefehle: /status /tasks /approve /stop /queue /remember /forget /rules /autonomy /pause /resume /computer /digest", d.Name))
	case "status":
		var running, waiting, pending int
		_ = h.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='running'), count(*) FILTER (WHERE status='waiting'),
			(SELECT count(*) FROM approvals WHERE dot_id=$1 AND status='pending') FROM runs WHERE dot_id=$1`, dot).Scan(&running, &waiting, &pending)
		var spent int64
		_ = h.Pool.QueryRow(ctx, `SELECT coalesce(sum(cost_micro_eur),0) FROM usage_events WHERE dot_id=$1 AND created_at >= date_trunc('day', now())`, dot).Scan(&spent)
		h.reply(ctx, ev, fmt.Sprintf("%s · %s · Autonomie L%d\nLäufe aktiv: %d · wartend: %d · offene Freigaben: %d\nKosten heute: %.2f €",
			d.Name, d.Status, d.Autonomy, running, waiting, pending, float64(spent)/1e6))
	case "tasks":
		rows, err := h.Pool.Query(ctx, `SELECT title, status FROM tasks WHERE dot_id=$1 AND status NOT IN ('done','failed','cancelled') ORDER BY priority DESC, created_at LIMIT 15`, dot)
		if err != nil {
			return true
		}
		var lines []string
		for rows.Next() {
			var t, s string
			_ = rows.Scan(&t, &s)
			lines = append(lines, "• "+t+" ("+s+")")
		}
		rows.Close()
		if len(lines) == 0 {
			lines = []string{"Keine offenen Aufgaben."}
		}
		h.reply(ctx, ev, strings.Join(lines, "\n"))
	case "approve":
		list, _ := st.ListApprovals(ctx, d.WorkspaceID, "pending", 10)
		n := 0
		ch := h.channelFor(ev.Platform, ev.BotID, dot)
		for _, a := range list {
			if a.DotID != dot.String() || ch == nil {
				continue
			}
			ref, err := ch.SendApproval(ctx, Target{Platform: ev.Platform, ChatID: ev.ChatID, ThreadID: ev.ThreadID}, h.card(d, a))
			if err == nil {
				h.mu.Lock()
				h.init()
				h.cards[a.ID] = append(h.cards[a.ID], ref)
				h.mu.Unlock()
				n++
			}
		}
		if n == 0 {
			h.reply(ctx, ev, "Keine offenen Freigaben.")
		}
	case "stop":
		conv, err := st.EnsureConversation(ctx, dot, ev.Platform, ev.ChatID, ev.ThreadID, convKind(ev))
		if err == nil {
			if runID, ok := h.Engine.ActiveRunFor(ctx, conv); ok {
				_ = h.Engine.Cancel(ctx, runID)
				h.reply(ctx, ev, "⏹️ Gestoppt.")
				return true
			}
		}
		h.reply(ctx, ev, "Gerade läuft nichts.")
	case "queue":
		if strings.TrimSpace(ev.Args) == "" {
			h.reply(ctx, ev, "Nutzung: /queue <nachricht>")
			return true
		}
		// Als eigener Run nach dem aktuellen einreihen (Lane ist seriell).
		ev.Text, ev.Command = ev.Args, ""
		conv, err := st.EnsureConversation(ctx, dot, ev.Platform, ev.ChatID, ev.ThreadID, convKind(ev))
		if err != nil {
			return true
		}
		target, _ := json.Marshal(Target{Platform: ev.Platform, ChatID: ev.ChatID, ThreadID: ev.ThreadID})
		run := &runtime.Run{DotID: dot, ConversationID: &conv, Kind: runtime.KindChat, Tier: "worker",
			Input: runtime.Input{Text: ev.Args, Trust: trust, Author: ev.Sender.Display, Channel: ev.Platform, Target: target}}
		if st.CreateRun(ctx, run) == nil {
			_ = st.SaveMessage(ctx, &runtime.StoredMessage{ConversationID: conv, RunID: &run.ID, Role: "user", Text: ev.Args, Trust: trust, Author: ev.Sender.Display, AuthorIdentityID: &id.ID})
			_ = h.Engine.Submit(ctx, run)
			h.reply(ctx, ev, "📥 Eingereiht.")
		}
	case "remember":
		if !ownerOnly() || h.Memory == nil {
			return true
		}
		if err := h.Memory.Remember(ctx, dot, ev.Args); err != nil {
			h.reply(ctx, ev, "Konnte ich nicht speichern: "+err.Error())
			return true
		}
		h.reply(ctx, ev, "🧠 Gemerkt.")
	case "forget":
		if !ownerOnly() || h.Memory == nil {
			return true
		}
		n, err := h.Memory.Forget(ctx, dot, ev.Args)
		if err != nil {
			h.reply(ctx, ev, "Fehler: "+err.Error())
			return true
		}
		if h.Audit != nil {
			_ = h.Audit.Log(ctx, audit.Entry{WorkspaceID: d.WorkspaceID, Actor: "user:" + id.UserID.String(), Action: "memory.forget", Target: dot.String(), Detail: map[string]any{"deleted": n, "via": ev.Platform}})
		}
		h.reply(ctx, ev, fmt.Sprintf("🗑️ %d Einträge gelöscht.", n))
	case "rules":
		rules, _ := st.Rules(ctx, d.WorkspaceID)
		var lines []string
		for _, r := range rules {
			if r.DotID == "" || r.DotID == dot.String() {
				lines = append(lines, fmt.Sprintf("• %s → %s", r.Name, r.Effect))
			}
		}
		if len(lines) == 0 {
			lines = []string{"Keine eigenen Regeln – es gilt die Autonomie-Matrix."}
		}
		h.reply(ctx, ev, strings.Join(lines, "\n")+"\nBearbeiten: "+strings.TrimRight(h.BaseURL, "/")+"/rules")
	case "autonomy":
		h.reply(ctx, ev, fmt.Sprintf("Autonomiestufe: L%d. Ändern nur in der Web-UI (Step-up): %s/dots/%s", d.Autonomy, strings.TrimRight(h.BaseURL, "/"), dot))
	case "pause", "resume":
		if !ownerOnly() {
			return true
		}
		status := map[string]string{"pause": "paused", "resume": "active"}[ev.Command]
		_, _ = h.Pool.Exec(ctx, `UPDATE dots SET status=$2 WHERE id=$1`, dot, status)
		if h.Audit != nil {
			_ = h.Audit.Log(ctx, audit.Entry{WorkspaceID: d.WorkspaceID, Actor: "user:" + id.UserID.String(), Action: "dot." + ev.Command, Target: dot.String()})
		}
		h.reply(ctx, ev, map[string]string{"pause": "⏸️ Pausiert.", "resume": "▶️ Läuft wieder."}[ev.Command])
	case "computer":
		h.reply(ctx, ev, "🖥️ "+strings.TrimRight(h.BaseURL, "/")+"/computer/"+dot.String())
	case "digest":
		ev.Text = "Erstelle mir jetzt eine kurze Zusammenfassung: Was ist seit dem letzten Digest passiert, was wartet auf mich, was steht an?"
		ev.Command = ""
		h.message(ctx, ev, id, dot, trust)
	case "team", "graph":
		h.reply(ctx, ev, "📊 "+strings.TrimRight(h.BaseURL, "/")+"/teams")
	default:
		return false // unbekannte Commands als normale Nachricht behandeln
	}
	return true
}
