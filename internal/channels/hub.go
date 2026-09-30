package channels

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/audit"
	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/runtime"
)

// STT transkribiert Sprachnachrichten.
type STT interface {
	Transcribe(ctx context.Context, audio []byte, mime string) (string, error)
}

// MemoryOps für /remember und /forget.
type MemoryOps interface {
	Remember(ctx context.Context, dot uuid.UUID, text string) error
	Forget(ctx context.Context, dot uuid.UUID, topic string) (int, error)
}

// Hub verbindet Kanäle mit der Runtime (Inbound-Pipeline + runtime.Outbound).
type Hub struct {
	Pool    *pgxpool.Pool
	Engine  *runtime.Engine
	Signer  Signer
	STT     STT
	Memory  MemoryOps
	Audit   audit.Logger
	Log     *slog.Logger
	BaseURL string
	// DefaultDot: Fylgja für Shared-Bots ohne eigene Bindung.
	DefaultDot uuid.UUID

	mu       sync.RWMutex
	chans    map[string]Channel    // platform:botID → Channel
	dotChans map[string]Channel    // dot:platform → Channel
	botDot   map[string]uuid.UUID  // platform:botID → Dot
	shared   map[string]Channel    // platform → Shared-Bot
	streams  map[uuid.UUID]*stream // run → Streaming-Nachricht
	cards    map[string][]MessageRef
}

type stream struct {
	ref      MessageRef
	ch       Channel
	lastEdit time.Time
	text     string
	mu       sync.Mutex
}

func (h *Hub) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

func (h *Hub) init() {
	if h.chans == nil {
		h.chans, h.dotChans, h.botDot, h.shared = map[string]Channel{}, map[string]Channel{}, map[string]uuid.UUID{}, map[string]Channel{}
		h.streams, h.cards = map[uuid.UUID]*stream{}, map[string][]MessageRef{}
	}
}

// Register bindet einen gestarteten Kanal an eine Fylgja (uuid.Nil = Shared-Bot).
func (h *Hub) Register(ch Channel, dot uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.chans[ch.Platform()+":"+ch.ID()] = ch
	if dot != uuid.Nil {
		h.dotChans[dot.String()+":"+ch.Platform()] = ch
		h.botDot[ch.Platform()+":"+ch.ID()] = dot
	} else {
		h.shared[ch.Platform()] = ch
	}
}

// Run startet einen Kanal und registriert ihn, sobald die Bot-ID bekannt ist.
func (h *Hub) Run(ctx context.Context, ch Channel, dot uuid.UUID) {
	go func() {
		for i := 0; i < 600 && ch.ID() == ""; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		h.Register(ch, dot)
	}()
	for ctx.Err() == nil {
		if err := ch.Start(ctx, h.Handle); err != nil && ctx.Err() == nil {
			h.log().Error("kanal gestoppt, neustart", "platform", ch.Platform(), "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
		}
	}
}

// Channels liefert alle Kanäle mit Health (UI).
func (h *Hub) Channels() map[string]Health {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := map[string]Health{}
	for k, c := range h.chans {
		out[k] = c.Health()
	}
	return out
}

func (h *Hub) channelFor(platform, botID string, dot uuid.UUID) Channel {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if botID != "" {
		if c := h.chans[platform+":"+botID]; c != nil {
			return c
		}
	}
	if c := h.dotChans[dot.String()+":"+platform]; c != nil {
		return c
	}
	return h.shared[platform]
}

// SetDefaultDot setzt die Fylgja für Shared-Bots.
func (h *Hub) SetDefaultDot(d uuid.UUID) {
	h.mu.Lock()
	h.DefaultDot = d
	h.mu.Unlock()
}

// DefaultDotID liefert die Fylgja für Shared-Bots.
func (h *Hub) DefaultDotID() uuid.UUID {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.DefaultDot
}

func (h *Hub) dotFor(ev InboundEvent) uuid.UUID {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if d, ok := h.botDot[ev.Platform+":"+ev.BotID]; ok {
		return d
	}
	return h.DefaultDot
}

// ---- Inbound ----

func (h *Hub) dedup(ctx context.Context, ev InboundEvent) bool {
	tag, err := h.Pool.Exec(ctx, `INSERT INTO inbound_dedup (platform, chat_id, message_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
		ev.Platform, ev.ChatID+"/"+ev.BotID, ev.MessageID)
	return err == nil && tag.RowsAffected() == 1
}

type identity struct {
	ID     uuid.UUID
	UserID uuid.UUID
}

func (h *Hub) lookupIdentity(ctx context.Context, platform, puid string) (*identity, error) {
	var id identity
	err := h.Pool.QueryRow(ctx, `SELECT id, user_id FROM channel_identities WHERE platform=$1 AND platform_user_id=$2 AND verified_at IS NOT NULL`, platform, puid).Scan(&id.ID, &id.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &id, err
}

// access bestimmt die Rechte eines Nutzers bei einer Fylgja: owner|member|"".
func (h *Hub) access(ctx context.Context, user, dot uuid.UUID, grant string) string {
	var owner *uuid.UUID
	var ws uuid.UUID
	if err := h.Pool.QueryRow(ctx, `SELECT owner_user_id, workspace_id FROM dots WHERE id=$1`, dot).Scan(&owner, &ws); err != nil {
		return ""
	}
	if owner != nil && *owner == user {
		return "owner"
	}
	var n int
	_ = h.Pool.QueryRow(ctx, `SELECT count(*) FROM dot_grants WHERE dot_id=$1 AND user_id=$2 AND grant_name=$3`, dot, user, grant).Scan(&n)
	if n > 0 {
		return "member"
	}
	var role string
	_ = h.Pool.QueryRow(ctx, `SELECT role FROM memberships WHERE workspace_id=$1 AND user_id=$2`, ws, user).Scan(&role)
	if owner == nil && (role == "owner" || role == "admin") {
		return "owner" // Specialist ohne Owner: Admins steuern
	}
	if role == "owner" || role == "admin" {
		return "member"
	}
	return ""
}

func (h *Hub) reply(ctx context.Context, ev InboundEvent, text string) {
	ch := h.channelFor(ev.Platform, ev.BotID, uuid.Nil)
	if ch == nil {
		return
	}
	if ev.CallbackID != "" && ev.Callback != "" {
		_ = ch.AnswerCallback(ctx, ev.CallbackID, text)
		return
	}
	_, _ = ch.Send(ctx, Target{Platform: ev.Platform, ChatID: ev.ChatID, ThreadID: ev.ThreadID, ReplyTo: ev.MessageID}, Text(text))
}

var pairingRe = func(s string) (string, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if len(s) == 9 && s[4] == '-' {
		return s, true
	}
	return "", false
}

// Handle ist der InboundHandler aller Kanäle.
func (h *Hub) Handle(ctx context.Context, ev InboundEvent) {
	if ev.Sender.IsBot {
		return
	}
	if !h.dedup(ctx, ev) {
		return
	}
	dot := h.dotFor(ev)
	// Pairing (nur im DM).
	if ev.IsDM {
		code, ok := pairingRe(ev.Text)
		if ev.Command == "pair" || ev.Command == "start" && ev.Args != "" {
			code, ok = pairingRe(ev.Args)
		}
		if ok {
			h.pair(ctx, ev, code)
			return
		}
	}
	id, err := h.lookupIdentity(ctx, ev.Platform, ev.Sender.PlatformUserID)
	if err != nil {
		h.log().Error("identität", "err", err)
		return
	}
	if id == nil || dot == uuid.Nil {
		// Allowlist-by-default: Unbekannte bekommen keine Antwort (nur Logging).
		h.log().Info("nachricht von unbekanntem absender ignoriert", "platform", ev.Platform, "user", ev.Sender.PlatformUserID, "dm", ev.IsDM)
		return
	}
	if ev.Callback != "" {
		h.handleCallback(ctx, ev, id, dot)
		return
	}
	if ev.Reaction != "" {
		h.handleReaction(ctx, ev, dot)
		return
	}
	// Gruppen: nur Erwähnungen, Replies an den Bot und Commands.
	if !ev.IsDM && !ev.MentionsBot && !ev.ReplyToBot && ev.Command == "" {
		return
	}
	trust := h.access(ctx, id.UserID, dot, "chat")
	if trust == "" {
		h.log().Info("nicht berechtigter nutzer ignoriert", "user", id.UserID, "dot", dot)
		return
	}
	if ev.Command != "" && h.command(ctx, ev, id, dot, trust) {
		return
	}
	h.message(ctx, ev, id, dot, trust)
}

func (h *Hub) pair(ctx context.Context, ev InboundEvent, code string) {
	var ws, user, dot uuid.UUID
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE pairing_codes SET used_at=now() WHERE code=$1 AND used_at IS NULL AND expires_at > now() RETURNING workspace_id, user_id, dot_id`, code).Scan(&ws, &user, &dot)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO channel_identities (id, user_id, platform, platform_user_id, display, verified_at) VALUES ($1,$2,$3,$4,$5,now())
			ON CONFLICT (platform, platform_user_id) DO UPDATE SET user_id=EXCLUDED.user_id, display=EXCLUDED.display, verified_at=now()`,
			uuid.Must(uuid.NewV7()), user, ev.Platform, ev.Sender.PlatformUserID, ev.Sender.Display)
		return err
	})
	if err != nil {
		h.log().Info("pairing fehlgeschlagen", "platform", ev.Platform, "err", err)
		h.reply(ctx, ev, "Dieser Code ist ungültig oder abgelaufen. Erzeuge in der Web-UI einen neuen.")
		return
	}
	if h.Audit != nil {
		_ = h.Audit.Log(ctx, audit.Entry{WorkspaceID: ws, Actor: "user:" + user.String(), Action: "channel.pair", Target: ev.Platform,
			Detail: map[string]any{"platform_user_id": ev.Sender.PlatformUserID, "dot": dot.String()}})
	}
	var name string
	_ = h.Pool.QueryRow(ctx, `SELECT name FROM dots WHERE id=$1`, dot).Scan(&name)
	h.reply(ctx, ev, fmt.Sprintf("✅ Verknüpft. Ich bin %s – schreib mir einfach.", name))
}

// NewPairingCode erzeugt einen Code (Format ABCD-1234, 15 min gültig).
func NewPairingCode(ctx context.Context, pool *pgxpool.Pool, ws, user, dot uuid.UUID) (string, time.Time, error) {
	const letters = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	var sb strings.Builder
	for i := 0; i < 4; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		sb.WriteByte(letters[n.Int64()])
	}
	sb.WriteByte('-')
	for i := 0; i < 4; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(10))
		sb.WriteByte(byte('0' + n.Int64()))
	}
	exp := time.Now().Add(15 * time.Minute)
	_, err := pool.Exec(ctx, `INSERT INTO pairing_codes (code, workspace_id, user_id, dot_id, expires_at) VALUES ($1,$2,$3,$4,$5)`, sb.String(), ws, user, dot, exp)
	return sb.String(), exp, err
}

func convKind(ev InboundEvent) string {
	switch {
	case ev.IsDM:
		return "dm"
	case ev.ThreadID != "":
		return "thread"
	}
	return "group"
}

func (h *Hub) message(ctx context.Context, ev InboundEvent, id *identity, dot uuid.UUID, trust string) {
	st := h.Engine.Store
	d, err := st.GetDot(ctx, dot)
	if err != nil {
		return
	}
	if d.Status == "paused" {
		h.reply(ctx, ev, "⏸️ "+d.Name+" ist pausiert. /resume zum Fortsetzen.")
		return
	}
	text := ev.Text
	var images []string
	ch := h.channelFor(ev.Platform, ev.BotID, dot)
	for _, a := range ev.Attachments {
		switch a.Kind {
		case "voice", "audio":
			if h.STT == nil || ch == nil {
				text += "\n[Sprachnachricht – Transkription nicht konfiguriert]"
				continue
			}
			data, err := ch.Download(ctx, a)
			if err == nil {
				var t string
				if t, err = h.STT.Transcribe(ctx, data, a.Mime); err == nil {
					text = strings.TrimSpace(text + "\n" + t)
					continue
				}
			}
			text += "\n[Sprachnachricht konnte nicht transkribiert werden]"
		case "image":
			if ch == nil || a.Size > 5<<20 {
				continue
			}
			if data, err := ch.Download(ctx, a); err == nil {
				images = append(images, "data:"+firstNonEmpty(a.Mime, "image/jpeg")+";base64,"+base64.StdEncoding.EncodeToString(data))
			}
		case "file":
			text += fmt.Sprintf("\n[Datei angehängt: %s (%s, %d KB)]", a.Name, a.Mime, a.Size/1024)
		}
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return
	}
	conv, err := st.EnsureConversation(ctx, dot, ev.Platform, ev.ChatID, ev.ThreadID, convKind(ev))
	if err != nil {
		h.log().Error("konversation", "err", err)
		return
	}
	// Steer: läuft bereits ein Run in dieser Lane, wird die Nachricht injiziert (8.4).
	if trust == "owner" {
		if runID, ok := h.Engine.ActiveRunFor(ctx, conv); ok && h.Engine.Steer(runID, text) {
			_ = st.SaveMessage(ctx, &runtime.StoredMessage{ConversationID: conv, RunID: &runID, Role: "user", Text: text, Trust: trust,
				Author: ev.Sender.Display, AuthorIdentityID: &id.ID, PlatformMessageID: ev.MessageID})
			return
		}
	}
	target, _ := json.Marshal(Target{Platform: ev.Platform, ChatID: ev.ChatID, ThreadID: ev.ThreadID, ReplyTo: replyTo(ev)})
	run := &runtime.Run{DotID: dot, ConversationID: &conv, Kind: runtime.KindChat, Tier: "worker",
		Input: runtime.Input{Text: text, Images: images, Trust: trust, Author: ev.Sender.Display, Channel: ev.Platform, Target: target}}
	if err := st.CreateRun(ctx, run); err != nil {
		h.log().Error("run anlegen", "err", err)
		return
	}
	if err := st.SaveMessage(ctx, &runtime.StoredMessage{ConversationID: conv, RunID: &run.ID, Role: "user", Text: text, Trust: trust,
		Author: ev.Sender.Display, AuthorIdentityID: &id.ID, PlatformMessageID: ev.MessageID}); err != nil {
		h.log().Error("nachricht speichern", "err", err)
		return
	}
	if ch != nil {
		_ = ch.Typing(ctx, Target{Platform: ev.Platform, ChatID: ev.ChatID, ThreadID: ev.ThreadID})
	}
	if err := h.Engine.Submit(ctx, run); err != nil {
		h.log().Error("run einreihen", "err", err)
	}
}

func replyTo(ev InboundEvent) string {
	if ev.IsDM {
		return ""
	}
	return ev.MessageID
}

func (h *Hub) handleReaction(ctx context.Context, ev InboundEvent, dot uuid.UUID) {
	kind := ""
	switch ev.Reaction {
	case "👍", "❤", "❤️", "🔥":
		kind = "thumb_up"
	case "👎":
		kind = "thumb_down"
	default:
		return
	}
	var msgID *uuid.UUID
	var runID *uuid.UUID
	_ = h.Pool.QueryRow(ctx, `SELECT m.id, m.run_id FROM messages m JOIN conversations c ON c.id=m.conversation_id
		WHERE c.dot_id=$1 AND c.platform=$2 AND m.platform_message_id=$3 LIMIT 1`, dot, ev.Platform, ev.ReplyTo).Scan(&msgID, &runID)
	_, _ = h.Pool.Exec(ctx, `INSERT INTO feedback (id, dot_id, run_id, message_id, kind, payload) VALUES ($1,$2,$3,$4,$5,$6)`,
		uuid.Must(uuid.NewV7()), dot, runID, msgID, kind, map[string]any{"platform": ev.Platform, "platform_message_id": ev.ReplyTo})
}

func (h *Hub) handleCallback(ctx context.Context, ev InboundEvent, id *identity, dot uuid.UUID) {
	apID, action, err := h.Signer.Verify(ev.Callback)
	if err != nil {
		h.reply(ctx, ev, "Ungültige Aktion.")
		return
	}
	a, err := h.Engine.Store.GetApproval(ctx, apID)
	if err != nil {
		h.reply(ctx, ev, "Freigabe nicht gefunden.")
		return
	}
	// Nur berechtigte Approver (Plattform-ID-Prüfung, 9.3).
	if h.access(ctx, id.UserID, uuid.MustParse(a.DotID), "approve:"+string(a.Class)) == "" {
		h.reply(ctx, ev, "Du darfst diese Aktion nicht freigeben.")
		return
	}
	res := policy.Resolution{UserID: id.UserID.String(), Via: ev.Platform, Approve: action != "d"}
	if action == "d" {
		res.Reason = "abgelehnt über " + ev.Platform
	}
	got, err := h.Engine.ResolveApproval(ctx, apID, res)
	switch {
	case errors.Is(err, policy.ErrStepUp):
		h.reply(ctx, ev, "Diese Aktion braucht eine Passkey-Bestätigung in der Web-UI.")
		return
	case errors.Is(err, policy.ErrSameUser):
		h.reply(ctx, ev, "Vier-Augen-Prinzip: eine zweite Person muss freigeben.")
		return
	case errors.Is(err, policy.ErrNotPending):
		h.reply(ctx, ev, "Bereits entschieden.")
		return
	case err != nil:
		h.reply(ctx, ev, "Fehler: "+err.Error())
		return
	}
	if action == "w" && got.Status == policy.Approved {
		// "Immer für …" erzeugt einen eng gescopten Regel-Proposal, nie eine globale Regel (9.6).
		expr := fmt.Sprintf(`action.tool == %q`, got.Tool)
		if to, ok := got.ArgsRedacted["to"].(string); ok && strings.Contains(to, "@") {
			expr += fmt.Sprintf(` && action.target.recipients.all(r, r.endsWith(%q))`, "@"+strings.SplitN(to, "@", 2)[1])
		}
		_, _ = h.Engine.Store.CreateProposal(ctx, uuid.MustParse(got.DotID), "rule",
			map[string]any{"name": "Immer erlauben: " + got.Tool, "expr": expr, "effect": "allow", "source_approval": got.ID},
			[]map[string]any{{"approval_id": got.ID}})
	}
	h.reply(ctx, ev, map[bool]string{true: "✅ Freigegeben", false: "❌ Abgelehnt"}[got.Status == policy.Approved])
	h.updateCards(ctx, got)
}

// ---- runtime.Outbound ----

func (h *Hub) targetOf(run *runtime.Run) (Target, bool) {
	if len(run.Input.Target) == 0 {
		return Target{}, false
	}
	var t Target
	if json.Unmarshal(run.Input.Target, &t) != nil || t.Platform == "" || t.Platform == "web" {
		return Target{}, false
	}
	return t, true
}

// Delta aktualisiert die Streaming-Nachricht (Edit-in-place, rate-limit-bewusst).
func (h *Hub) Delta(ctx context.Context, run *runtime.Run, text string) {
	t, ok := h.targetOf(run)
	if !ok {
		return
	}
	ch := h.channelFor(t.Platform, "", run.DotID)
	if ch == nil || !ch.Capabilities().Edits {
		return
	}
	h.mu.Lock()
	h.init()
	s := h.streams[run.ID]
	if s == nil {
		s = &stream{ch: ch}
		h.streams[run.ID] = s
	}
	h.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text = text
	if time.Since(s.lastEdit) < ch.Capabilities().EditInterval {
		return
	}
	preview := Split(text+" ▌", ch.Capabilities().MaxLen-10)[0]
	if s.ref.MessageID == "" {
		ref, err := ch.Send(ctx, t, Text(preview))
		if err == nil {
			s.ref = ref
		}
	} else {
		_ = ch.Edit(ctx, s.ref, Text(preview))
	}
	s.lastEdit = time.Now()
}

// Final liefert die endgültige Antwort (Edit der Streaming-Nachricht bzw. Outbox).
func (h *Hub) Final(ctx context.Context, run *runtime.Run, text string) error {
	t, ok := h.targetOf(run)
	h.mu.Lock()
	h.init()
	s := h.streams[run.ID]
	delete(h.streams, run.ID)
	h.mu.Unlock()
	if !ok && (run.Kind == runtime.KindRoutine || run.Kind == runtime.KindTaskStep) {
		t, ok = h.ownerTarget(ctx, run.DotID)
	}
	if !ok {
		return nil // Web-Kanal: Zustellung über SSE
	}
	if s != nil && s.ref.MessageID != "" {
		parts := Split(RenderPlainLen(text), s.ch.Capabilities().MaxLen-50)
		if len(parts) == 1 {
			if err := s.ch.Edit(ctx, s.ref, Text(text)); err == nil {
				h.rememberPlatformID(ctx, run, s.ref.MessageID)
				return nil
			}
		}
		// Länger als eine Nachricht: Streaming-Nachricht ersetzen und Rest per Outbox.
		_ = s.ch.Edit(ctx, s.ref, Text("⬇️"))
	}
	return h.Enqueue(ctx, run.DotID, t, Text(text), LaneKey(run), run.ID)
}

// RenderPlainLen ist die Längenbasis für das Splitten (vor dem Rendern).
func RenderPlainLen(s string) string { return s }

// LaneKey für die Outbox-Reihenfolge.
func LaneKey(run *runtime.Run) string {
	if run.ConversationID != nil {
		return run.ConversationID.String()
	}
	return run.DotID.String()
}

func (h *Hub) rememberPlatformID(ctx context.Context, run *runtime.Run, pmid string) {
	_, _ = h.Pool.Exec(ctx, `UPDATE messages SET platform_message_id=$2 WHERE run_id=$1 AND role='assistant' AND platform_message_id=''`, run.ID, pmid)
}

// ApprovalRequested sendet eine Approval-Karte.
func (h *Hub) ApprovalRequested(ctx context.Context, run *runtime.Run, a *policy.Approval) {
	dot, err := h.Engine.Store.GetDot(ctx, run.DotID)
	if err != nil {
		return
	}
	card := h.card(dot, a)
	t, ok := h.targetOf(run)
	if !ok {
		t, ok = h.ownerTarget(ctx, run.DotID)
	}
	if !ok {
		return // nur Web-UI
	}
	ch := h.channelFor(t.Platform, "", run.DotID)
	if ch == nil {
		return
	}
	ref, err := ch.SendApproval(ctx, Target{Platform: t.Platform, ChatID: t.ChatID, ThreadID: t.ThreadID}, card)
	if err != nil {
		h.log().Warn("approval-karte", "err", err)
		return
	}
	h.mu.Lock()
	h.init()
	h.cards[a.ID] = append(h.cards[a.ID], ref)
	h.mu.Unlock()
}

func (h *Hub) card(dot *runtime.Dot, a *policy.Approval) ApprovalCard {
	id := uuid.MustParse(a.ID)
	summary, _ := a.Preview["summary"].(string)
	rationale, _ := a.Preview["rationale"].(string)
	prev, _ := json.MarshalIndent(a.ArgsRedacted, "", "  ")
	c := ApprovalCard{ID: a.ID, DotName: dot.Name, Tool: a.Tool, Summary: summary, Class: string(a.Class), Risk: a.Risk, Reason: a.Reason,
		Rationale: rationale, Preview: string(prev), ExpiresAt: a.ExpiresAt, StepUp: a.StepUp,
		DeepLink:    strings.TrimRight(h.BaseURL, "/") + "/approvals/" + a.ID,
		ApproveData: h.Signer.Sign(id, "a"), DenyData: h.Signer.Sign(id, "d"), AlwaysData: h.Signer.Sign(id, "w")}
	if a.Class == policy.Spend || a.Class == policy.Destructive || a.Class == policy.Credential {
		c.AlwaysData = "" // für riskante Klassen kein "Immer erlauben"
	}
	return c
}

func (h *Hub) updateCards(ctx context.Context, a *policy.Approval) {
	h.mu.Lock()
	refs := h.cards[a.ID]
	delete(h.cards, a.ID)
	h.mu.Unlock()
	dot, err := h.Engine.Store.GetDot(ctx, uuid.MustParse(a.DotID))
	if err != nil {
		return
	}
	card := h.card(dot, a)
	card.Resolved = fmt.Sprintf("%s via %s um %s", map[bool]string{true: "✅ freigegeben", false: "❌ " + string(a.Status)}[a.Status == policy.Approved],
		a.ResolvedVia, a.ResolvedAt.Format("15:04"))
	for _, ref := range refs {
		if ch := h.channelFor(ref.Target.Platform, "", dot.ID); ch != nil {
			_ = ch.UpdateApproval(ctx, ref, card)
		}
	}
}

// ApprovalResolved wird von der API gerufen, wenn in der Web-UI entschieden wurde.
func (h *Hub) ApprovalResolved(ctx context.Context, a *policy.Approval) { h.updateCards(ctx, a) }

// ownerTarget: bevorzugter Kanal des Owners (zuletzt genutzte DM).
func (h *Hub) ownerTarget(ctx context.Context, dot uuid.UUID) (Target, bool) {
	var t Target
	err := h.Pool.QueryRow(ctx, `SELECT c.platform, c.platform_chat_id, c.platform_thread_id FROM conversations c
		WHERE c.dot_id=$1 AND c.kind='dm' AND c.platform IN ('telegram','discord')
		ORDER BY (SELECT max(created_at) FROM messages m WHERE m.conversation_id=c.id) DESC NULLS LAST LIMIT 1`, dot).Scan(&t.Platform, &t.ChatID, &t.ThreadID)
	return t, err == nil
}

// NotifyOwner sendet eine Nachricht an den Owner (Outbox).
func (h *Hub) NotifyOwner(ctx context.Context, dot *runtime.Dot, text string) {
	if t, ok := h.ownerTarget(ctx, dot.ID); ok {
		_ = h.Enqueue(ctx, dot.ID, t, Text(text), dot.ID.String(), uuid.Nil)
	}
	if h.Engine.Pub != nil {
		h.Engine.Pub.Publish(ctx, "alerts", map[string]any{"dot": dot.ID, "text": text})
	}
}

// ---- Outbox (9.2) ----

type outboxMsg struct {
	RunID uuid.UUID   `json:"run_id,omitempty"`
	Msg   RichMessage `json:"msg"`
}

// Enqueue schreibt eine ausgehende Nachricht transaktional in die Outbox.
func (h *Hub) Enqueue(ctx context.Context, dot uuid.UUID, t Target, m RichMessage, lane string, run uuid.UUID) error {
	tb, _ := json.Marshal(t)
	mb, _ := json.Marshal(outboxMsg{RunID: run, Msg: m})
	var d any
	if dot != uuid.Nil {
		d = dot
	}
	_, err := h.Pool.Exec(ctx, `INSERT INTO outbox (dot_id, platform, target, message, lane) VALUES ($1,$2,$3,$4,$5)`, d, t.Platform, tb, mb, lane)
	if err == nil {
		_, _ = h.Pool.Exec(ctx, `SELECT pg_notify('fylgja_outbox', '')`)
	}
	return err
}

// DeliverOnce stellt fällige Outbox-Einträge zu. Reihenfolge pro Lane bleibt erhalten:
// Scheitert ein Eintrag, warten spätere Einträge derselben Lane.
func (h *Hub) DeliverOnce(ctx context.Context) (int, error) {
	rows, err := h.Pool.Query(ctx, `SELECT id, coalesce(dot_id::text,''), platform, target, message, lane, attempts FROM outbox
		WHERE status='pending' ORDER BY id LIMIT 100`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id                  int64
		dot, platform, lane string
		target, msg         []byte
		attempts            int
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.dot, &it.platform, &it.target, &it.msg, &it.lane, &it.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	blocked := map[string]bool{}
	sent := 0
	now := time.Now()
	for _, it := range items {
		if blocked[it.lane] {
			continue
		}
		var next time.Time
		_ = h.Pool.QueryRow(ctx, `SELECT next_attempt_at FROM outbox WHERE id=$1`, it.id).Scan(&next)
		if next.After(now) {
			blocked[it.lane] = true
			continue
		}
		var t Target
		var om outboxMsg
		_ = json.Unmarshal(it.target, &t)
		_ = json.Unmarshal(it.msg, &om)
		dot, _ := uuid.Parse(it.dot)
		ch := h.channelFor(t.Platform, "", dot)
		var ref MessageRef
		err := errors.New("kein kanal für " + t.Platform)
		if ch != nil {
			ref, err = ch.Send(ctx, t, om.Msg)
		}
		if err != nil {
			blocked[it.lane] = true
			backoff := time.Duration(1<<min(it.attempts, 8)) * time.Second
			status := "pending"
			if it.attempts+1 >= 10 {
				status = "failed"
			}
			_, _ = h.Pool.Exec(ctx, `UPDATE outbox SET attempts=attempts+1, next_attempt_at=now()+$2::interval, error=$3, status=$4 WHERE id=$1`,
				it.id, fmt.Sprintf("%d seconds", int(backoff.Seconds())), err.Error(), status)
			continue
		}
		_, _ = h.Pool.Exec(ctx, `UPDATE outbox SET status='sent', platform_ref=$2 WHERE id=$1`, it.id, ref.MessageID)
		if om.RunID != uuid.Nil {
			_, _ = h.Pool.Exec(ctx, `UPDATE messages SET platform_message_id=$2 WHERE run_id=$1 AND role='assistant' AND platform_message_id=''`, om.RunID, ref.MessageID)
		}
		sent++
	}
	return sent, nil
}

// RunOutbox stellt kontinuierlich zu (LISTEN/NOTIFY + Polling-Fallback).
func (h *Hub) RunOutbox(ctx context.Context) {
	wake := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			conn, err := h.Pool.Acquire(ctx)
			if err != nil {
				time.Sleep(time.Second)
				continue
			}
			_, err = conn.Exec(ctx, "LISTEN fylgja_outbox")
			for err == nil {
				_, err = conn.Conn().WaitForNotification(ctx)
				if err == nil {
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}
			conn.Release()
		}
	}()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if _, err := h.DeliverOnce(ctx); err != nil && ctx.Err() == nil {
			h.log().Warn("outbox", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
