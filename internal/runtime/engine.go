package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/audit"
	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/platform/clock"
	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/router"
	"github.com/realblxckcodex/fylgja/internal/tools"
	"github.com/realblxckcodex/fylgja/internal/vault"
)

// Outbound liefert Ergebnisse an Kanäle (implementiert von channels.Hub).
type Outbound interface {
	Delta(ctx context.Context, run *Run, text string)
	Final(ctx context.Context, run *Run, text string) error
	ApprovalRequested(ctx context.Context, run *Run, a *policy.Approval)
	NotifyOwner(ctx context.Context, dot *Dot, text string)
}

// MemoryAPI ist die Sicht der Runtime auf das Memory.
type MemoryAPI interface {
	Core(ctx context.Context, dot uuid.UUID) ([]MemoryItem, error)
	Retrieve(ctx context.Context, dot uuid.UUID, query string) ([]MemoryItem, error)
}

// Publisher verteilt Live-Events (SSE).
type Publisher interface {
	Publish(ctx context.Context, topic string, payload any)
}

// Engine führt Runs aus.
type Engine struct {
	Store    *Store
	LLM      llm.Client
	Tools    *tools.Registry
	Reviewer policy.Reviewer
	Memory   MemoryAPI
	Redactor *vault.Redactor
	Audit    audit.Logger
	Out      Outbound
	Pub      Publisher
	Clock    clock.Clock
	Log      *slog.Logger
	Lanes    *Lanes
	Services any // an Tools durchgereicht
	// DefaultTiers: tier → logisches Modell, falls die Fylgja kein Profil hat.
	DefaultTiers  map[string]string
	MaxSteps      int
	ToolTimeout   time.Duration
	ReviewTimeout time.Duration
	ImageHosts    []string
	// OnFinish wird nach erfolgreichem Run aufgerufen (Memory-Extraktion, Usage-Reports).
	OnFinish func(ctx context.Context, run *Run, final string)
	// OnFail wird nach endgültigem Fehlschlag/Abbruch aufgerufen (Koordination).
	OnFail func(ctx context.Context, run *Run, reason string)

	mu     sync.Mutex
	active map[uuid.UUID]*activeRun
}

type activeRun struct {
	cancel context.CancelFunc
	steer  chan string
}

var (
	errParked   = errors.New("run wartet auf freigabe")
	errBudget   = errors.New("budget erreicht")
	errLoop     = errors.New("endlosschleife erkannt (identische tool-aufrufe)")
	errMaxSteps = errors.New("maximale schrittzahl erreicht")
)

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e *Engine) now() time.Time {
	if e.Clock != nil {
		return e.Clock.Now()
	}
	return time.Now()
}

func (e *Engine) publish(ctx context.Context, topic string, payload any) {
	if e.Pub != nil {
		e.Pub.Publish(ctx, topic, payload)
	}
}

// LaneKey bestimmt die Lane eines Runs.
func LaneKey(r *Run) string {
	switch {
	case r.ConversationID != nil:
		return "conv:" + r.ConversationID.String()
	case r.TaskID != nil:
		return "task:" + r.TaskID.String()
	case r.Kind == KindPulse:
		return "pulse:" + r.DotID.String()
	}
	return "run:" + r.ID.String()
}

// Submit legt einen Run an (falls nötig) und reiht ihn in seine Lane ein.
func (e *Engine) Submit(ctx context.Context, r *Run) error {
	if r.CreatedAt.IsZero() {
		if err := e.Store.CreateRun(ctx, r); err != nil {
			return err
		}
	}
	e.publish(ctx, "dot."+r.DotID.String()+".activity", map[string]any{"type": "run.queued", "run": r})
	e.Lanes.Enqueue(context.WithoutCancel(ctx), LaneKey(r), r.DotID.String(), func(c context.Context) {
		if err := e.Execute(c, r.ID); err != nil && !errors.Is(err, errParked) {
			e.log().Warn("run beendet mit fehler", "run", r.ID, "err", err)
		}
	})
	return nil
}

// Resume reiht einen bestehenden Run erneut ein (nach Approval oder Neustart).
func (e *Engine) Resume(ctx context.Context, id uuid.UUID) error {
	r, err := e.Store.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if r.Status.Terminal() {
		return nil
	}
	return e.Submit(ctx, r)
}

// ResumeAll nimmt nach einem Neustart alle unterbrochenen Runs wieder auf (8.8).
func (e *Engine) ResumeAll(ctx context.Context) (int, error) {
	runs, err := e.Store.ActiveRuns(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		if r.Kind == KindSubagent {
			continue // Subagents werden vom Eltern-Run neu gestartet
		}
		if err := e.Submit(ctx, r); err == nil {
			n++
		}
	}
	return n, nil
}

// Steer injiziert eine Nachricht in einen laufenden Run (8.4). false = kein aktiver Run.
func (e *Engine) Steer(id uuid.UUID, text string) bool {
	e.mu.Lock()
	a := e.active[id]
	e.mu.Unlock()
	if a == nil {
		return false
	}
	select {
	case a.steer <- text:
		return true
	default:
		return false
	}
}

// Cancel bricht einen Run kooperativ ab.
func (e *Engine) Cancel(ctx context.Context, id uuid.UUID) error {
	e.mu.Lock()
	a := e.active[id]
	e.mu.Unlock()
	if a != nil {
		a.cancel()
		return nil
	}
	return e.Store.SetStatus(ctx, id, Cancelled, "abgebrochen")
}

// ActiveRunFor liefert den aktiven Run einer Konversation (für Steer).
func (e *Engine) ActiveRunFor(ctx context.Context, conv uuid.UUID) (uuid.UUID, bool) {
	var id uuid.UUID
	err := e.Store.Pool.QueryRow(ctx, `SELECT id FROM runs WHERE conversation_id=$1 AND status='running' ORDER BY created_at DESC LIMIT 1`, conv).Scan(&id)
	if err != nil {
		return uuid.Nil, false
	}
	e.mu.Lock()
	_, ok := e.active[id]
	e.mu.Unlock()
	return id, ok
}

// callState rekonstruiert den Zustand eines Tool-Calls aus dem Journal.
type callState struct {
	call           llm.ToolCall
	journaled      bool
	decision       *policy.Decision
	approvalID     string
	approvalStatus string
	execStarted    bool
	done           bool
}

type state struct {
	extra       []llm.Message
	steps       int
	tainted     bool
	usedDomains []string
	pending     []*callState
	calls       map[string]*callState
	loaded      []string // per tools.search nachgeladene Tools
	hashes      map[string]int
	finished    bool
	final       string
	tier        string
	downgraded  bool
}

type modelResponsePayload struct {
	Message    llm.Message `json:"message"`
	Usage      llm.Usage   `json:"usage"`
	Deployment string      `json:"deployment"`
	Cost       int64       `json:"cost_micro_eur"`
	Degraded   bool        `json:"degraded"`
	Model      string      `json:"model"`
	Tier       string      `json:"tier"`
}

type toolCallPayload struct {
	CallID         string          `json:"call_id"`
	Tool           string          `json:"tool"`
	Args           json.RawMessage `json:"args"`
	IdempotencyKey string          `json:"idempotency_key"`
	Class          string          `json:"class"`
}

type toolResultPayload struct {
	CallID    string   `json:"call_id"`
	Tool      string   `json:"tool"`
	Content   string   `json:"content"`
	IsError   bool     `json:"is_error"`
	Untrusted bool     `json:"untrusted"`
	Source    string   `json:"source,omitempty"`
	Egress    []string `json:"egress,omitempty"`
	Loaded    []string `json:"loaded,omitempty"`
	Data      any      `json:"data,omitempty"`
}

func (e *Engine) replay(evs []Event, run *Run) *state {
	st := &state{calls: map[string]*callState{}, hashes: map[string]int{}, tainted: run.Tainted, tier: run.Tier}
	for _, ev := range evs {
		switch ev.Type {
		case EvModelResponse:
			var p modelResponsePayload
			_ = json.Unmarshal(ev.Payload, &p)
			st.steps++
			st.extra = append(st.extra, p.Message)
			st.pending = nil
			for _, tc := range p.Message.ToolCalls {
				cs := &callState{call: tc}
				st.calls[tc.ID] = cs
				st.pending = append(st.pending, cs)
				st.hashes[callHash(tc)]++
			}
			if p.Tier != "" {
				st.tier = p.Tier
			}
		case EvToolCall:
			var p toolCallPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if cs := st.calls[p.CallID]; cs != nil {
				cs.journaled = true
			}
		case EvPolicy:
			var p struct {
				CallID   string          `json:"call_id"`
				Decision policy.Decision `json:"decision"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			if cs := st.calls[p.CallID]; cs != nil {
				d := p.Decision
				cs.decision = &d
			}
		case EvApprovalRequested:
			var p struct {
				CallID     string `json:"call_id"`
				ApprovalID string `json:"approval_id"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			if cs := st.calls[p.CallID]; cs != nil {
				cs.approvalID = p.ApprovalID
			}
		case EvApprovalResolved:
			var p struct {
				CallID string `json:"call_id"`
				Status string `json:"status"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			if cs := st.calls[p.CallID]; cs != nil {
				cs.approvalStatus = p.Status
			}
		case EvToolExec:
			var p struct {
				CallID string `json:"call_id"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			if cs := st.calls[p.CallID]; cs != nil {
				cs.execStarted = true
			}
		case EvToolResult:
			var p toolResultPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if cs := st.calls[p.CallID]; cs != nil {
				cs.done = true
			}
			st.extra = append(st.extra, toolMessage(p))
			st.usedDomains = append(st.usedDomains, p.Egress...)
			st.loaded = append(st.loaded, p.Loaded...)
		case EvSteer:
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			st.extra = append(st.extra, llm.Message{Role: llm.User, Content: "[Neue Nachricht des Owners während der Arbeit]\n" + p.Text})
		case EvTaint:
			st.tainted = true
		case EvMessageOut:
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			st.finished, st.final = true, p.Text
		case EvBudgetStop:
			st.finished = true
		}
	}
	return st
}

func callHash(tc llm.ToolCall) string {
	var norm any
	_ = json.Unmarshal(tc.Arguments, &norm)
	b, _ := json.Marshal(norm)
	h := sha256.Sum256(append([]byte(tc.Name+"|"), b...))
	return hex.EncodeToString(h[:8])
}

const maxToolResultChars = 12000

func toolMessage(p toolResultPayload) llm.Message {
	content := p.Content
	if len(content) > maxToolResultChars {
		content = content[:maxToolResultChars] + fmt.Sprintf("\n…[%d Zeichen gekürzt; vollständig im Journal]", len(p.Content)-maxToolResultChars)
	}
	if p.Untrusted {
		kind := "tool"
		switch {
		case strings.HasPrefix(p.Tool, "web."):
			kind = "web"
		case strings.HasPrefix(p.Tool, "mail."):
			kind = "email"
		case strings.HasPrefix(p.Tool, "fs."):
			kind = "file"
		}
		content = WrapUntrusted(firstNonEmpty(p.Source, p.Tool), kind, content)
	}
	if p.IsError {
		content = "FEHLER: " + content
	}
	return llm.Message{Role: llm.Tool, ToolCallID: p.CallID, Name: p.Tool, Content: content}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func priorityFor(k Kind) llm.Priority {
	switch k {
	case KindChat:
		return llm.Interactive
	case KindReview:
		return llm.ReviewPrio
	case KindPulse, KindConsolidation, KindLearn:
		return llm.Background
	}
	return llm.TaskPrio
}

func (e *Engine) logicalModel(dot *Dot, tier string) string {
	if m := dot.Tiers[tier]; m != "" {
		return m
	}
	if m := e.DefaultTiers[tier]; m != "" {
		return m
	}
	return tier + "-default"
}

func downgrade(tier string) string {
	switch tier {
	case "planner":
		return "worker"
	case "worker":
		return "triage"
	}
	return tier
}

// Execute führt einen Run aus bzw. setzt ihn fort (idempotent über das Journal).
func (e *Engine) Execute(parent context.Context, id uuid.UUID) (err error) {
	run, err := e.Store.GetRun(parent, id)
	if err != nil {
		return err
	}
	if run.Status.Terminal() {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	if run.Input.Budget.WallClockS > 0 {
		ctx, cancel = context.WithTimeout(parent, time.Duration(run.Input.Budget.WallClockS)*time.Second)
	}
	defer cancel()
	ar := &activeRun{cancel: cancel, steer: make(chan string, 16)}
	e.mu.Lock()
	if e.active == nil {
		e.active = map[uuid.UUID]*activeRun{}
	}
	e.active[id] = ar
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.active, id)
		e.mu.Unlock()
		// Nicht zugestellte Steer-Nachrichten gehen nicht verloren: als Folge-Run einreihen.
		for {
			select {
			case txt := <-ar.steer:
				e.followUp(context.WithoutCancel(parent), run, txt)
			default:
				return
			}
		}
	}()

	dot, err := e.Store.GetDot(ctx, run.DotID)
	if err != nil {
		return err
	}
	if err := e.Store.SetStatus(ctx, id, Running, ""); err != nil {
		return err
	}
	e.publish(ctx, "run."+id.String(), map[string]any{"type": "status", "status": Running})
	err = e.loop(ctx, run, dot, ar)
	final := context.WithoutCancel(ctx)
	switch {
	case err == nil:
		_ = e.Store.SetStatus(final, id, Succeeded, "")
	case errors.Is(err, errParked):
		_ = e.Store.SetStatus(final, id, Waiting, "")
	case errors.Is(err, context.Canceled) && parent.Err() == nil:
		_, _ = e.Store.Append(final, id, EvError, map[string]string{"error": "abgebrochen"})
		_ = e.Store.SetStatus(final, id, Cancelled, "abgebrochen")
	case parent.Err() != nil:
		// Prozess fährt herunter: Run bleibt "running" und wird beim Neustart fortgesetzt.
		return err
	default:
		_, _ = e.Store.Append(final, id, EvError, map[string]string{"error": e.redact(err.Error())})
		_ = e.Store.SetStatus(final, id, Failed, e.redact(err.Error()))
		if e.OnFail != nil {
			e.OnFail(final, run, e.redact(err.Error()))
		}
		if run.Kind == KindChat || run.Kind == KindTaskStep {
			if e.Out != nil {
				_ = e.Out.Final(final, run, "⚠️ Das hat nicht geklappt: "+e.redact(err.Error()))
			}
		}
	}
	st, _ := e.Store.GetRun(final, id)
	if st != nil {
		e.publish(final, "run."+id.String(), map[string]any{"type": "status", "status": st.Status, "error": st.Error})
		e.publish(final, "dot."+run.DotID.String()+".activity", map[string]any{"type": "run." + string(st.Status), "run": st})
	}
	return err
}

func (e *Engine) followUp(ctx context.Context, run *Run, text string) {
	nr := &Run{DotID: run.DotID, ConversationID: run.ConversationID, TaskID: run.TaskID, Kind: run.Kind, Scope: run.Scope, Tier: run.Tier,
		Input: Input{Text: text, Trust: run.Input.Trust, Author: run.Input.Author, Channel: run.Input.Channel, Target: run.Input.Target}}
	_ = e.Submit(ctx, nr)
}

func (e *Engine) redact(s string) string {
	if e.Redactor == nil {
		return vault.NewRedactor().Redact(s)
	}
	return e.Redactor.Redact(s)
}

func (e *Engine) baseContext(ctx context.Context, run *Run, dot *Dot) (Assembled, error) {
	in := AssembleInput{
		Now:      e.now(),
		Identity: Identity{Name: dot.Name, Persona: dot.Persona, Charter: dot.Charter, Language: dot.Locale, Timezone: dot.Timezone, OwnerName: dot.OwnerName},
		Input:    run.Input.Text, InputImages: run.Input.Images, InputTrust: run.Input.Trust, InputAuthor: run.Input.Author,
		RunKind: string(run.Kind),
	}
	if run.Kind == KindSubagent || run.Kind == KindReview {
		// Subagents bekommen nur ihren Auftrag (kein Memory-Dump, 15.3).
		in.Identity.Persona, in.Identity.OwnerName = "", ""
		return Assemble(in), nil
	}
	if e.Memory != nil {
		if core, err := e.Memory.Core(ctx, dot.ID); err == nil {
			in.Core = core
		}
		if run.Input.Text != "" {
			if ret, err := e.Memory.Retrieve(ctx, dot.ID, run.Input.Text); err == nil {
				in.Retrieval = ret
			}
		}
	}
	in.RulesSummary = e.rulesSummary(ctx, dot)
	if run.ConversationID != nil {
		msgs, err := e.Store.RecentMessages(ctx, *run.ConversationID, 30, nil)
		if err != nil {
			return Assembled{}, err
		}
		for _, m := range msgs {
			if m.RunID != nil && *m.RunID == run.ID {
				continue // die Eingabe dieses Runs steht separat am Ende
			}
			role := llm.User
			if m.Role == "assistant" {
				role = llm.Assistant
			}
			in.History = append(in.History, HistoryMessage{Role: role, Content: m.Text, Trust: m.Trust, Author: m.Author, At: m.CreatedAt})
		}
		in.ConvSummary, _ = e.Store.ConversationSummary(ctx, *run.ConversationID)
	}
	return Assemble(in), nil
}

func (e *Engine) rulesSummary(ctx context.Context, dot *Dot) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Autonomiestufe L%d. ", dot.Autonomy)
	switch dot.Autonomy {
	case 0:
		sb.WriteString("Jede Aktion braucht Freigabe.\n")
	case 1:
		sb.WriteString("Lesen/interne Arbeit frei; externe Änderungen und Kommunikation brauchen Freigabe.\n")
	case 2:
		sb.WriteString("Externe Änderungen werden automatisch geprüft; Kommunikation braucht Freigabe.\n")
	case 3:
		sb.WriteString("Externe Änderungen und Kommunikation werden automatisch geprüft (nach Regeln).\n")
	}
	sb.WriteString("Löschen, Geld ausgeben und Laptop-Zugriffe brauchen immer Freigabe; Zugangs-/Sicherheitsänderungen bleiben beim Menschen.\n")
	rules, err := e.Store.Rules(ctx, dot.WorkspaceID)
	if err == nil {
		for _, r := range rules {
			if r.DotID == "" || r.DotID == dot.ID.String() {
				fmt.Fprintf(&sb, "- %s: %s\n", r.Name, r.Effect)
			}
		}
	}
	return sb.String()
}

func (e *Engine) visibleTools(run *Run, st *state) []llm.ToolDef {
	if run.Scope == policy.ScopeNone || run.Input.NoTools || e.Tools == nil {
		return nil
	}
	seen := map[string]bool{}
	var defs []llm.ToolDef
	add := func(t *tools.Tool) {
		if seen[t.Name] || !tools.Visible(t, run.Scope) {
			return
		}
		if len(run.Input.ToolAllowlist) > 0 && !policy.ScopeSubset([]string{t.Name}, run.Input.ToolAllowlist) && t.Name != "tools.search" {
			return
		}
		seen[t.Name] = true
		defs = append(defs, t.Def())
	}
	for _, t := range e.Tools.BaseSet(run.Scope) {
		add(t)
	}
	for _, n := range st.loaded {
		if t, ok := e.Tools.Get(n); ok {
			add(t)
		}
	}
	return defs
}

func (e *Engine) loop(ctx context.Context, run *Run, dot *Dot, ar *activeRun) error {
	evs, err := e.Store.Events(ctx, run.ID)
	if err != nil {
		return err
	}
	st := e.replay(evs, run)
	if st.finished {
		return nil
	}
	if !run.Tainted && run.Input.Trust != "" && run.Input.Trust != "owner" && run.Input.Trust != "system" {
		if err := e.taint(ctx, run, st, "eingabe von "+run.Input.Trust); err != nil {
			return err
		}
	}
	base, err := e.baseContext(ctx, run, dot)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		_, _ = e.Store.Append(ctx, run.ID, EvCheckpoint, map[string]any{"loaded_memories": base.Loaded, "prefix_hash": base.PrefixHash})
	}
	maxSteps := e.MaxSteps
	if run.Input.MaxSteps > 0 {
		maxSteps = run.Input.MaxSteps
	}
	if maxSteps <= 0 {
		maxSteps = 40
	}
	for {
		// Offene Tool-Calls der letzten Modellantwort abarbeiten (auch nach Resume).
		for _, cs := range st.pending {
			if cs.done {
				continue
			}
			if err := e.processCall(ctx, run, dot, st, cs); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Steer-Nachrichten zwischen zwei Schritten injizieren.
	drain:
		for {
			select {
			case txt := <-ar.steer:
				if _, err := e.Store.Append(ctx, run.ID, EvSteer, map[string]string{"text": txt}); err != nil {
					return err
				}
				st.extra = append(st.extra, llm.Message{Role: llm.User, Content: "[Neue Nachricht des Owners während der Arbeit]\n" + txt})
			default:
				break drain
			}
		}
		if st.steps >= maxSteps {
			return errMaxSteps
		}
		if err := e.checkBudget(ctx, run, dot, st); err != nil {
			return err
		}
		resp, err := e.callModel(ctx, run, dot, st, base)
		if err != nil {
			return err
		}
		st.steps++
		st.extra = append(st.extra, resp.Message)
		st.pending = nil
		if len(resp.Message.ToolCalls) == 0 {
			return e.finish(ctx, run, dot, st, resp.Message.Content)
		}
		for _, tc := range resp.Message.ToolCalls {
			h := callHash(tc)
			st.hashes[h]++
			if st.hashes[h] >= 3 {
				return errLoop
			}
			cs := &callState{call: tc}
			st.calls[tc.ID] = cs
			st.pending = append(st.pending, cs)
		}
	}
}

func (e *Engine) checkBudget(ctx context.Context, run *Run, dot *Dot, st *state) error {
	b := run.Input.Budget
	if b.Tokens > 0 || b.CostMicroEUR > 0 {
		tok, cost, err := e.Store.RunUsage(ctx, run.ID)
		if err == nil && ((b.Tokens > 0 && tok >= b.Tokens) || (b.CostMicroEUR > 0 && cost >= b.CostMicroEUR)) {
			_, _ = e.Store.Append(ctx, run.ID, EvBudgetStop, map[string]any{"tokens": tok, "cost_micro_eur": cost})
			return errBudget
		}
	}
	spent, limit, hard, err := e.Store.DotBudgetState(ctx, dot.ID)
	if err != nil || limit <= 0 {
		return nil
	}
	if spent >= limit && hard {
		_, _ = e.Store.Append(ctx, run.ID, EvBudgetStop, map[string]any{"spent": spent, "limit": limit})
		_, _ = e.Store.CreateProposal(ctx, dot.ID, "action", map[string]any{"kind": "budget_increase", "spent_micro_eur": spent, "limit_micro_eur": limit},
			[]map[string]any{{"run_id": run.ID}})
		if e.Out != nil {
			e.Out.NotifyOwner(ctx, dot, fmt.Sprintf("⛔ Tagesbudget von %s erreicht (%.2f €). Run gestoppt – Budget erhöhen?", dot.Name, float64(spent)/1e6))
		}
		return errBudget
	}
	if spent >= limit*8/10 && !st.downgraded {
		nt := downgrade(st.tier)
		if nt != st.tier {
			st.tier, st.downgraded = nt, true
			if e.Out != nil {
				e.Out.NotifyOwner(ctx, dot, fmt.Sprintf("ℹ️ %s hat 80 %% des Tagesbudgets verbraucht und nutzt jetzt ein günstigeres Modell.", dot.Name))
			}
		}
	}
	return nil
}

func (e *Engine) callModel(ctx context.Context, run *Run, dot *Dot, st *state, base Assembled) (*llm.Response, error) {
	msgs := append(append([]llm.Message(nil), base.Messages...), st.extra...)
	privacy := router.Strictest(llm.Privacy(dot.PrivacyMode), llm.Privacy(firstNonEmpty(run.Input.Privacy, dot.PrivacyMode)))
	req := llm.Request{
		Model:    e.logicalModel(dot, st.tier),
		Messages: msgs,
		Tools:    e.visibleTools(run, st),
		Meta: llm.Meta{RunID: run.ID.String(), DotID: dot.ID.String(), WorkspaceID: dot.WorkspaceID.String(), Tier: st.tier,
			Priority: priorityFor(run.Kind), Privacy: privacy, PrefixHash: base.PrefixHash, NoDegrade: run.Kind == KindReview},
	}
	if len(run.Input.OutputSchema) > 0 && len(req.Tools) == 0 {
		req.JSONMode = true
	}
	if _, err := e.Store.Append(ctx, run.ID, EvModelRequest, map[string]any{"model": req.Model, "tier": st.tier, "messages": len(msgs), "tools": len(req.Tools), "prefix_hash": base.PrefixHash, "privacy": privacy}); err != nil {
		return nil, err
	}
	var onDelta llm.DeltaFunc
	if run.Kind == KindChat && e.Out != nil {
		var buf strings.Builder
		onDelta = func(s string) {
			buf.WriteString(s)
			e.Out.Delta(ctx, run, e.redact(buf.String()))
			e.publish(ctx, "run."+run.ID.String(), map[string]any{"type": "delta", "text": s})
		}
	}
	var resp *llm.Response
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = e.LLM.Chat(ctx, req, onDelta)
		if err == nil || !llm.Retryable(err) || ctx.Err() != nil {
			break
		}
		_, _ = e.Store.Append(ctx, run.ID, EvError, map[string]any{"error": e.redact(err.Error()), "retry": attempt + 1})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(1<<attempt) * time.Second):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("modell: %w", err)
	}
	// Leere Tool-Call-IDs vermeiden (Journal-Schlüssel).
	for i := range resp.Message.ToolCalls {
		if resp.Message.ToolCalls[i].ID == "" {
			resp.Message.ToolCalls[i].ID = fmt.Sprintf("call_%d_%d", st.steps, i)
		}
	}
	if _, err := e.Store.Append(ctx, run.ID, EvModelResponse, modelResponsePayload{Message: resp.Message, Usage: resp.Usage, Deployment: resp.Deployment,
		Cost: resp.CostMicroEUR, Degraded: resp.Degraded, Model: req.Model, Tier: st.tier}); err != nil {
		return nil, err
	}
	_ = e.Store.RecordUsage(ctx, UsageEvent{DotID: dot.ID, RunID: run.ID, Model: req.Model, Tier: st.tier, Deployment: resp.Deployment,
		TokensIn: resp.Usage.In, TokensOut: resp.Usage.Out, TokensCached: resp.Usage.Cached, CostMicroEUR: resp.CostMicroEUR, LatencyMS: resp.LatencyMS})
	if resp.Degraded && st.tier == "planner" && e.Out != nil {
		e.Out.NotifyOwner(ctx, dot, "ℹ️ Hauptmodell nicht verfügbar – arbeite mit Ersatzmodell weiter.")
	}
	return resp, nil
}

func (e *Engine) taint(ctx context.Context, run *Run, st *state, source string) error {
	if st.tainted {
		return nil
	}
	st.tainted, run.Tainted = true, true
	if _, err := e.Store.Append(ctx, run.ID, EvTaint, map[string]string{"source": source}); err != nil {
		return err
	}
	return e.Store.MarkTainted(ctx, run.ID)
}

func (e *Engine) result(ctx context.Context, run *Run, st *state, cs *callState, p toolResultPayload) error {
	p.CallID, p.Tool = cs.call.ID, cs.call.Name
	p.Content = e.redact(p.Content)
	if _, err := e.Store.Append(ctx, run.ID, EvToolResult, p); err != nil {
		return err
	}
	cs.done = true
	st.extra = append(st.extra, toolMessage(p))
	st.usedDomains = append(st.usedDomains, p.Egress...)
	st.loaded = append(st.loaded, p.Loaded...)
	if p.Untrusted {
		if err := e.taint(ctx, run, st, p.Tool); err != nil {
			return err
		}
	}
	e.publish(ctx, "run."+run.ID.String(), map[string]any{"type": "tool_result", "tool": p.Tool, "is_error": p.IsError, "untrusted": p.Untrusted})
	return nil
}

func domainsOf(args map[string]any) []string {
	var out []string
	for _, k := range []string{"url", "endpoint", "href"} {
		if s, ok := args[k].(string); ok {
			if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
				out = append(out, strings.ToLower(u.Hostname()))
			}
		}
	}
	return out
}

func (e *Engine) processCall(ctx context.Context, run *Run, dot *Dot, st *state, cs *callState) error {
	tc := cs.call
	tool, known := e.Tools.Get(tc.Name)
	if !cs.journaled {
		class := ""
		if known {
			class = string(tool.Class)
		}
		if _, err := e.Store.Append(ctx, run.ID, EvToolCall, toolCallPayload{CallID: tc.ID, Tool: tc.Name, Args: json.RawMessage(e.redact(string(tc.Arguments))),
			IdempotencyKey: run.ID.String() + ":" + tc.ID, Class: class}); err != nil {
			return err
		}
		cs.journaled = true
	}
	if !known {
		return e.result(ctx, run, st, cs, toolResultPayload{Content: "unbekanntes tool " + tc.Name + " – nutze tools.search", IsError: true})
	}
	args, verr := tools.ValidateArgs(tool.Schema, tc.Arguments)
	if verr != nil {
		return e.result(ctx, run, st, cs, toolResultPayload{Content: "ungültige argumente: " + verr.Error(), IsError: true})
	}
	// Policy (einmalig, Entscheidung wird journalisiert).
	if cs.decision == nil {
		d := e.decide(ctx, run, dot, st, tool, args)
		cs.decision = &d
		if _, err := e.Store.Append(ctx, run.ID, EvPolicy, map[string]any{"call_id": tc.ID, "decision": d}); err != nil {
			return err
		}
		if tool.Class.SideEffect() && e.Audit != nil {
			_ = e.Audit.Log(ctx, audit.Entry{WorkspaceID: dot.WorkspaceID, Actor: "dot:" + dot.ID.String(), Action: "tool.decision", Target: tool.Name,
				Detail: map[string]any{"run": run.ID.String(), "verdict": d.Verdict, "class": tool.Class, "tainted": st.tainted, "reasons": d.Reasons}})
		}
	}
	d := cs.decision
	switch d.Verdict {
	case policy.Deny:
		return e.result(ctx, run, st, cs, toolResultPayload{Content: "verweigert durch policy: " + strings.Join(d.Reasons, "; ") + ". Versuche es nicht erneut.", IsError: true})
	case policy.HumanOnly:
		if e.Out != nil {
			e.Out.NotifyOwner(ctx, dot, fmt.Sprintf("🔐 %s braucht dich: „%s“ ist eine Sicherheits-/Zugangsänderung und bleibt immer beim Menschen. Bitte übernimm (Take-over).", dot.Name, tools.RenderPreview(tool.Preview, args)))
		}
		return e.result(ctx, run, st, cs, toolResultPayload{Content: "Diese Aktion bleibt immer beim Menschen. Der Owner wurde benachrichtigt und übernimmt. Führe sie nicht selbst aus.", IsError: true})
	case policy.Ask:
		switch cs.approvalStatus {
		case "":
			if cs.approvalID == "" {
				return e.requestApproval(ctx, run, dot, st, cs, tool, args, *d)
			}
			a, err := e.Store.GetApproval(ctx, uuid.MustParse(cs.approvalID))
			if err != nil {
				return err
			}
			if a.Status == policy.Pending {
				if a.Expire(e.now()) {
					_, _ = e.Store.SaveApproval(ctx, a)
				} else {
					return errParked
				}
			}
			cs.approvalStatus = string(a.Status)
			if _, err := e.Store.Append(ctx, run.ID, EvApprovalResolved, map[string]any{"call_id": tc.ID, "approval_id": a.ID, "status": a.Status}); err != nil {
				return err
			}
			if a.Status != policy.Approved {
				return e.result(ctx, run, st, cs, toolResultPayload{Content: a.ToolResultText(), IsError: true})
			}
		case string(policy.Approved):
		default:
			a := &policy.Approval{Status: policy.ApprovalStatus(cs.approvalStatus)}
			if cs.approvalID != "" {
				if got, err := e.Store.GetApproval(ctx, uuid.MustParse(cs.approvalID)); err == nil {
					a = got
				}
			}
			return e.result(ctx, run, st, cs, toolResultPayload{Content: a.ToolResultText(), IsError: true})
		}
	}
	return e.execute(ctx, run, dot, st, cs, tool)
}

func (e *Engine) decide(ctx context.Context, run *Run, dot *Dot, st *state, tool *tools.Tool, args map[string]any) policy.Decision {
	act := policy.Action{Tool: tool.Name, Class: tool.Class, Args: args, EgressDomains: domainsOf(args)}
	if tool.Extract != nil {
		act.Recipients, act.TargetDomain, act.AmountMicroEUR = tool.Extract(args)
	}
	var rules policy.RuleSet
	if rs, err := e.Store.Rules(ctx, dot.WorkspaceID); err == nil {
		rules, _ = policy.CompileAll(rs)
	}
	approved, _ := e.Store.ApprovedSameCount(ctx, dot.ID, tool.Name)
	trigger := string(run.Kind)
	pc := policy.Context{DotID: dot.ID.String(), Autonomy: dot.Autonomy, Tainted: st.tainted, Trigger: trigger, Scope: run.Scope,
		Time: e.now(), Channel: run.Input.Channel, ApprovedSameCount: approved, UsedEgressDomains: st.usedDomains, ToolAllowlist: run.Input.ToolAllowlist}
	d := (&policy.Engine{Rules: rules}).Evaluate(act, pc)
	if d.Verdict == policy.Review {
		timeout := e.ReviewTimeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		v, out := policy.RunReview(ctx, e.Reviewer, policy.ReviewInput{
			TrustedInstructions: e.trustedInstructions(run),
			Action:              policy.Action{Tool: act.Tool, Class: act.Class, Args: redactArgs(e, args), Recipients: act.Recipients, TargetDomain: act.TargetDomain},
			RulesSummary:        e.rulesSummary(ctx, dot), Autonomy: dot.Autonomy, Tainted: st.tainted, Charter: dot.Charter,
		}, timeout)
		d.Verdict = v
		d.Reasons = append(d.Reasons, out.Reasons...)
		if out.Risk != "" {
			d.Risk = out.Risk
		}
		d.Reasons = append(d.Reasons, "auto-review: "+out.Verdict)
	}
	return d
}

func redactArgs(e *Engine, args map[string]any) map[string]any {
	b, _ := json.Marshal(args)
	var out map[string]any
	_ = json.Unmarshal([]byte(e.redact(string(b))), &out)
	return out
}

// trustedInstructions: nur Owner-/System-Anweisungen des Runs, nie untrusted Inhalte (14.6).
func (e *Engine) trustedInstructions(run *Run) []string {
	if run.Input.Trust == "owner" || run.Input.Trust == "system" || run.Input.Trust == "" {
		return []string{run.Input.Text}
	}
	return []string{"(Auftrag stammt nicht vom Owner)"}
}

func (e *Engine) requestApproval(ctx context.Context, run *Run, dot *Dot, st *state, cs *callState, tool *tools.Tool, args map[string]any, d policy.Decision) error {
	reason := strings.Join(d.Reasons, "; ")
	if st.tainted {
		reason += "; Kontext enthält untrusted Inhalt"
	}
	a := &policy.Approval{
		RunID: run.ID.String(), DotID: dot.ID.String(), Tool: tool.Name, Class: tool.Class,
		ArgsRedacted: redactArgs(e, args),
		Preview:      map[string]any{"summary": e.redact(tools.RenderPreview(tool.Preview, args)), "step_up": d.StepUp, "rationale": e.lastAssistantText(st)},
		Risk:         d.Risk, Reason: reason, Status: policy.Pending, RequiredApprovals: d.RequiredApprovals, StepUp: d.StepUp,
		ExpiresAt: e.now().Add(policy.DefaultExpiry(tool.Class)),
	}
	if dot.Kind == "specialist" {
		a.ApproverGroup = "dot:" + dot.ID.String()
	}
	if err := e.Store.CreateApproval(ctx, a); err != nil {
		return err
	}
	cs.approvalID = a.ID
	if _, err := e.Store.Append(ctx, run.ID, EvApprovalRequested, map[string]any{"call_id": cs.call.ID, "approval_id": a.ID}); err != nil {
		return err
	}
	if e.Audit != nil {
		_ = e.Audit.Log(ctx, audit.Entry{WorkspaceID: dot.WorkspaceID, Actor: "dot:" + dot.ID.String(), Action: "approval.request", Target: a.ID,
			Detail: map[string]any{"tool": tool.Name, "class": tool.Class, "risk": a.Risk}})
	}
	if e.Out != nil {
		e.Out.ApprovalRequested(ctx, run, a)
	}
	e.publish(ctx, "approvals", map[string]any{"type": "approval.pending", "approval": a})
	return errParked
}

func (e *Engine) lastAssistantText(st *state) string {
	for i := len(st.extra) - 1; i >= 0; i-- {
		if st.extra[i].Role == llm.Assistant && st.extra[i].Content != "" {
			return e.redact(st.extra[i].Content)
		}
	}
	return ""
}

func (e *Engine) execute(ctx context.Context, run *Run, dot *Dot, st *state, cs *callState, tool *tools.Tool) error {
	if cs.execStarted {
		// Absturz während der Ausführung: nicht-idempotente Tools nie blind wiederholen (8.8).
		if !tool.Idempotent {
			return e.result(ctx, run, st, cs, toolResultPayload{
				Content: "Status unbekannt: Diese Aktion wurde vor einem Neustart begonnen und wurde möglicherweise bereits ausgeführt. " +
					"Prüfe zuerst den aktuellen Zustand (z. B. ob das Objekt schon existiert), bevor du sie wiederholst, oder frage den Owner.",
				IsError: true})
		}
	} else {
		if _, err := e.Store.Append(ctx, run.ID, EvToolExec, map[string]string{"call_id": cs.call.ID}); err != nil {
			return err
		}
		cs.execStarted = true
	}
	// Engine-interne Tools.
	switch tool.Name {
	case "tools.search":
		return e.toolSearch(ctx, run, st, cs)
	case "subagent.spawn", "quarantine.extract":
		return e.spawnSubagent(ctx, run, dot, st, cs, tool.Name)
	}
	timeout := e.ToolTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env := &tools.Env{WorkspaceID: dot.WorkspaceID.String(), DotID: dot.ID.String(), RunID: run.ID.String(), Tainted: st.tainted, Scope: run.Scope, Services: e.Services}
	if run.TaskID != nil {
		env.TaskID = run.TaskID.String()
	}
	res, err := tool.Handler(tctx, tools.Call{ID: cs.call.ID, Tool: tool.Name, Args: cs.call.Arguments, IdempotencyKey: run.ID.String() + ":" + cs.call.ID, Env: env})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return e.result(ctx, run, st, cs, toolResultPayload{Content: err.Error(), IsError: true})
	}
	if tool.Class.SideEffect() && e.Audit != nil {
		_ = e.Audit.Log(ctx, audit.Entry{WorkspaceID: dot.WorkspaceID, Actor: "dot:" + dot.ID.String(), Action: "tool.call", Target: tool.Name,
			Detail: map[string]any{"run": run.ID.String(), "class": tool.Class, "ok": !res.IsError}})
	}
	return e.result(ctx, run, st, cs, toolResultPayload{Content: res.Content, IsError: res.IsError, Untrusted: res.Untrusted, Source: res.Source, Egress: res.Egress, Data: res.Data})
}

func (e *Engine) toolSearch(ctx context.Context, run *Run, st *state, cs *callState) error {
	var a struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(cs.call.Arguments, &a)
	found := e.Tools.Search(a.Query, run.Scope, 8)
	var names []string
	var sb strings.Builder
	for _, t := range found {
		if len(run.Input.ToolAllowlist) > 0 && !policy.ScopeSubset([]string{t.Name}, run.Input.ToolAllowlist) {
			continue
		}
		names = append(names, t.Name)
		fmt.Fprintf(&sb, "- %s (%s): %s\n", t.Name, t.Class, t.Description)
	}
	if len(names) == 0 {
		sb.WriteString("keine passenden tools gefunden")
	} else {
		sb.WriteString("Diese Tools sind jetzt verfügbar.")
	}
	return e.result(ctx, run, st, cs, toolResultPayload{Content: sb.String(), Loaded: names})
}

// finish schließt einen Run mit einer finalen Antwort ab.
func (e *Engine) finish(ctx context.Context, run *Run, dot *Dot, st *state, text string) error {
	text = e.redact(text)
	text = policy.SanitizeOutbound(text, st.tainted, e.ImageHosts)
	payload := map[string]any{"text": text}
	if len(run.Input.OutputSchema) > 0 {
		data, err := tools.ValidateArgs(run.Input.OutputSchema, json.RawMessage(extractJSON(text)))
		if err != nil {
			return fmt.Errorf("ergebnis verletzt output_schema: %w", err)
		}
		payload["data"] = data
	}
	if _, err := e.Store.Append(ctx, run.ID, EvMessageOut, payload); err != nil {
		return err
	}
	st.finished, st.final = true, text
	if run.ConversationID != nil && run.Kind != KindSubagent {
		m := &StoredMessage{ConversationID: *run.ConversationID, RunID: &run.ID, Role: "assistant", Text: text, Trust: "system"}
		if err := e.Store.SaveMessage(ctx, m); err != nil {
			return err
		}
	}
	if e.Out != nil && (run.Kind == KindChat || run.Kind == KindTaskStep || run.Kind == KindRoutine) && text != "" {
		if err := e.Out.Final(ctx, run, text); err != nil {
			e.log().Warn("zustellung fehlgeschlagen", "run", run.ID, "err", err)
		}
	}
	e.publish(ctx, "run."+run.ID.String(), map[string]any{"type": "final", "text": text})
	if e.OnFinish != nil {
		e.OnFinish(context.WithoutCancel(ctx), run, text)
	}
	return nil
}

func extractJSON(s string) string {
	i := strings.IndexAny(s, "{[")
	j := strings.LastIndexAny(s, "}]")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

// Final liefert die finale Ausgabe eines Runs (für Subagents und Tests).
func (e *Engine) Final(ctx context.Context, id uuid.UUID) (string, any, error) {
	evs, err := e.Store.Events(ctx, id)
	if err != nil {
		return "", nil, err
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == EvMessageOut {
			var p struct {
				Text string `json:"text"`
				Data any    `json:"data"`
			}
			_ = json.Unmarshal(evs[i].Payload, &p)
			return p.Text, p.Data, nil
		}
	}
	return "", nil, ErrNotFound
}

// ResolveApproval wendet eine menschliche Entscheidung an und setzt den Run fort.
func (e *Engine) ResolveApproval(ctx context.Context, approvalID uuid.UUID, r policy.Resolution) (*policy.Approval, error) {
	a, err := e.Store.GetApproval(ctx, approvalID)
	if err != nil {
		return nil, err
	}
	if r.Now.IsZero() {
		r.Now = e.now()
	}
	final, rerr := a.Resolve(r)
	if rerr != nil && !final {
		return a, rerr
	}
	ok, err := e.Store.SaveApproval(ctx, a)
	if err != nil {
		return a, err
	}
	if !ok {
		return a, policy.ErrNotPending
	}
	dot, _ := e.Store.GetDot(ctx, uuid.MustParse(a.DotID))
	if dot != nil && e.Audit != nil {
		_ = e.Audit.Log(ctx, audit.Entry{WorkspaceID: dot.WorkspaceID, Actor: "user:" + r.UserID, Action: "approval.resolve", Target: a.ID,
			Detail: map[string]any{"status": a.Status, "via": r.Via, "tool": a.Tool, "approvals_given": len(a.ApprovalsGiven)}})
	}
	e.publish(ctx, "approvals", map[string]any{"type": "approval." + string(a.Status), "approval": a})
	if final && a.RunID != "" {
		if err := e.Resume(context.WithoutCancel(ctx), uuid.MustParse(a.RunID)); err != nil {
			return a, err
		}
	}
	return a, rerr
}

// ExpireApprovals läuft periodisch und weckt Runs mit abgelaufenen Approvals.
func (e *Engine) ExpireApprovals(ctx context.Context) int {
	list, err := e.Store.ExpiredApprovals(ctx, e.now())
	if err != nil {
		return 0
	}
	n := 0
	for _, a := range list {
		if a.Expire(e.now()) {
			if ok, _ := e.Store.SaveApproval(ctx, a); ok {
				n++
				e.publish(ctx, "approvals", map[string]any{"type": "approval.expired", "approval": a})
				if a.RunID != "" {
					_ = e.Resume(ctx, uuid.MustParse(a.RunID))
				}
			}
		}
	}
	return n
}

// contains ist ein kleiner Helfer.
func contains(list []string, s string) bool { return slices.Contains(list, s) }
