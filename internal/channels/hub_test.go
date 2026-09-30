package channels

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/runtime"
	"github.com/realblxckcodex/fylgja/internal/store/testdb"
	"github.com/realblxckcodex/fylgja/internal/tools"
)

type fakeChan struct {
	mu        sync.Mutex
	sent      []string
	approvals []ApprovalCard
	answers   []string
}

func (f *fakeChan) Platform() string { return "telegram" }
func (f *fakeChan) ID() string       { return "bot1" }
func (f *fakeChan) Capabilities() Capabilities {
	return Capabilities{MaxLen: 4096, Edits: true, Buttons: true, EditInterval: time.Hour}
}
func (f *fakeChan) Start(context.Context, InboundHandler) error { return nil }
func (f *fakeChan) Send(_ context.Context, _ Target, m RichMessage) (MessageRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, RenderPlain(m))
	return MessageRef{MessageID: "m1"}, nil
}
func (f *fakeChan) Edit(_ context.Context, _ MessageRef, m RichMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, "EDIT:"+RenderPlain(m))
	return nil
}
func (f *fakeChan) React(context.Context, MessageRef, string) error { return nil }
func (f *fakeChan) Typing(context.Context, Target) error            { return nil }
func (f *fakeChan) SendApproval(_ context.Context, _ Target, a ApprovalCard) (MessageRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approvals = append(f.approvals, a)
	return MessageRef{MessageID: "ap1"}, nil
}
func (f *fakeChan) UpdateApproval(context.Context, MessageRef, ApprovalCard) error { return nil }
func (f *fakeChan) AnswerCallback(_ context.Context, _ string, text string) error {
	f.mu.Lock()
	f.answers = append(f.answers, text)
	f.mu.Unlock()
	return nil
}
func (f *fakeChan) Download(context.Context, Attachment) ([]byte, error) { return nil, nil }
func (f *fakeChan) Health() Health                                       { return Health{OK: true} }

func (f *fakeChan) all() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.sent, "\n---\n")
}

func TestHubEndToEnd(t *testing.T) {
	pool, _ := testdb.New(t)
	ctx := context.Background()
	ws, user, dot := uuid.New(), uuid.New(), uuid.New()
	pool.Exec(ctx, `INSERT INTO workspaces (id,name) VALUES ($1,'w')`, ws)
	pool.Exec(ctx, `INSERT INTO users (id,email,display_name) VALUES ($1,'s@x.de','Sam')`, user)
	pool.Exec(ctx, `INSERT INTO memberships (workspace_id,user_id,role) VALUES ($1,$2,'owner')`, ws, user)
	pool.Exec(ctx, `INSERT INTO dots (id,workspace_id,name,kind,owner_user_id,autonomy_level) VALUES ($1,$2,'Hugin','personal',$3,1)`, dot, ws, user)

	reg := tools.NewRegistry()
	runtime.RegisterEngineTools(reg)
	reg.MustRegister(&tools.Tool{Name: "mail.send", Class: policy.Communicate, Base: true, Preview: "Mail an {to}",
		Handler: func(context.Context, tools.Call) (tools.Result, error) { return tools.Result{Content: "ok"}, nil }})
	sl := &llm.Scripted{Func: func(req llm.Request) (*llm.Response, error) {
		last := req.Messages[len(req.Messages)-1]
		if strings.Contains(last.Content, "schick") {
			r := llm.Call("c1", "mail.send", map[string]any{"to": "anna@firma.de"})
			return &r, nil
		}
		r := llm.Text("Hallo Sam! (Antwort auf: " + last.Content + ")")
		return &r, nil
	}}
	eng := &runtime.Engine{Store: &runtime.Store{Pool: pool}, LLM: sl, Tools: reg, Lanes: runtime.NewLanes(4), MaxSteps: 5}
	fc := &fakeChan{}
	hub := &Hub{Pool: pool, Engine: eng, Signer: Signer{Key: []byte("k")}, BaseURL: "https://fylgja.local"}
	eng.Out = hub
	hub.Register(fc, dot)

	dm := func(msgID, text string) InboundEvent {
		return InboundEvent{Platform: "telegram", BotID: "bot1", ChatID: "42", MessageID: msgID, IsDM: true, Text: text, Sender: Sender{PlatformUserID: "42", Display: "Sam"}}
	}
	// 1. Unbekannter Absender: keine Antwort, kein Run.
	hub.Handle(ctx, dm("1", "hallo"))
	var runs int
	pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&runs)
	if runs != 0 || fc.all() != "" {
		t.Fatal("unbekannter absender bekam antwort")
	}
	// 2. Pairing.
	code, _, err := NewPairingCode(ctx, pool, ws, user, dot)
	if err != nil {
		t.Fatal(err)
	}
	hub.Handle(ctx, dm("2", strings.ToLower(code)))
	if !strings.Contains(fc.all(), "Verknüpft") {
		t.Fatalf("pairing: %s", fc.all())
	}
	hub.Handle(ctx, dm("3", code)) // Code ist verbraucht
	if !strings.Contains(fc.all(), "ungültig") {
		t.Fatal("code doppelt nutzbar")
	}
	// 3. Nachricht → Run → Outbox → Kanal. Duplikat wird ignoriert.
	hub.Handle(ctx, dm("4", "Wie geht's?"))
	hub.Handle(ctx, dm("4", "Wie geht's?"))
	eng.Lanes.Wait()
	// Die Antwort wird gestreamt (Edit-in-place) – die Outbox bleibt leer.
	if _, err := hub.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fc.all(), "Hallo Sam!") {
		t.Fatalf("antwort fehlt: %s", fc.all())
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&runs)
	if runs != 1 {
		t.Fatalf("dedup: %d runs", runs)
	}
	// 4. Gruppe ohne Erwähnung → ignoriert.
	g := dm("5", "irgendwas")
	g.IsDM, g.ChatID = false, "-100"
	hub.Handle(ctx, g)
	eng.Lanes.Wait()
	pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&runs)
	if runs != 1 {
		t.Fatal("gruppennachricht ohne erwähnung verarbeitet")
	}
	// 5. Approval per Button.
	hub.Handle(ctx, dm("6", "schick Anna eine Mail"))
	eng.Lanes.Wait()
	if len(fc.approvals) != 1 {
		t.Fatal("keine approval-karte")
	}
	card := fc.approvals[0]
	bad := dm("7", "")
	bad.Callback, bad.CallbackID = card.ApproveData[:len(card.ApproveData)-2]+"xx", "cb"
	hub.Handle(ctx, bad)
	if !strings.Contains(strings.Join(fc.answers, ","), "Ungültige") {
		t.Fatal("gefälschte signatur akzeptiert")
	}
	ok := dm("8", "")
	ok.Callback, ok.CallbackID = card.ApproveData, "cb2"
	hub.Handle(ctx, ok)
	eng.Lanes.Wait()
	if !strings.Contains(strings.Join(fc.answers, ","), "Freigegeben") {
		t.Fatalf("answers %v", fc.answers)
	}
	var status string
	pool.QueryRow(ctx, `SELECT status FROM approvals WHERE id=$1`, card.ID).Scan(&status)
	if status != "approved" {
		t.Fatal(status)
	}
	// 6. /pause
	p := dm("9", "/pause")
	p.Command = "pause"
	hub.Handle(ctx, p)
	hub.Handle(ctx, dm("10", "noch da?"))
	if !strings.Contains(fc.all(), "pausiert") {
		t.Fatal("pause")
	}
	// 7. Owner-Benachrichtigung über die Outbox.
	d, _ := eng.Store.GetDot(ctx, dot)
	hub.NotifyOwner(ctx, d, "Budget fast erreicht")
	if n, err := hub.DeliverOnce(ctx); err != nil || n != 1 || !strings.Contains(fc.all(), "Budget fast erreicht") {
		t.Fatalf("outbox: %d %v", n, err)
	}
	_ = json.Marshal
}
