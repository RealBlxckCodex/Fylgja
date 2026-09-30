package pulse

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/memory"
	"github.com/realblxckcodex/fylgja/internal/runtime"
	"github.com/realblxckcodex/fylgja/internal/store/testdb"
	"github.com/realblxckcodex/fylgja/internal/tools"
	"github.com/realblxckcodex/fylgja/internal/vault"
)

type notes struct {
	mu   sync.Mutex
	msgs []string
}

func (n *notes) NotifyOwner(_ context.Context, _ *runtime.Dot, t string) {
	n.mu.Lock()
	n.msgs = append(n.msgs, t)
	n.mu.Unlock()
}

func TestPulseEndToEnd(t *testing.T) {
	pool, _ := testdb.New(t)
	ctx := context.Background()
	ws, dot, user := uuid.New(), uuid.New(), uuid.New()
	pool.Exec(ctx, `INSERT INTO workspaces (id,name) VALUES ($1,'w')`, ws)
	pool.Exec(ctx, `INSERT INTO users (id,email,timezone) VALUES ($1,'a@b.de','UTC')`, user)
	pool.Exec(ctx, `INSERT INTO dots (id,workspace_id,name,kind,owner_user_id,quiet_hours,pulse_config) VALUES ($1,$2,'Hugin','personal',$3,'{"start":"23:00","end":"06:00"}','{"interval_min":30,"digest_times":[]}')`, dot, ws, user)
	pool.Exec(ctx, `INSERT INTO tasks (id,dot_id,title,status,due_at) VALUES ($1,$2,'Steuererklärung abgeben','queued',now()+interval '3 hours')`, uuid.New(), dot)

	sl := &llm.Scripted{Func: func(req llm.Request) (*llm.Response, error) {
		if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "Steuererklärung") {
			t.Errorf("signal fehlt im pulse-prompt")
		}
		for _, td := range req.Tools {
			if td.Name == "shell.run" || td.Name == "message.send" {
				t.Errorf("schreibendes tool %s im pulse sichtbar", td.Name)
			}
		}
		r := llm.Text(`{"items":[{"type":"nudge","urgency":"high","text":"Steuererklärung ist heute fällig"},{"type":"note","text":"Owner hat Steuerfrist heute"}]}`)
		return &r, nil
	}}
	reg := tools.NewRegistry()
	runtime.RegisterEngineTools(reg)
	mem := &memory.Service{Pool: pool, Embedder: llm.HashEmbedder{}, Redactor: vault.NewRedactor()}
	eng := &runtime.Engine{Store: &runtime.Store{Pool: pool}, LLM: sl, Tools: reg, Lanes: runtime.NewLanes(2), MaxSteps: 5}
	n := &notes{}
	pe := &Engine{Pool: pool, Runtime: eng, Memory: mem, Notify: n, Now: func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }}
	eng.OnFinish = func(ctx context.Context, r *runtime.Run, _ string) { pe.AfterRun(ctx, r) }
	pe.Tick(ctx)
	eng.Lanes.Wait()
	var kind, status, scope string
	pool.QueryRow(ctx, `SELECT kind, status, tool_scope FROM runs`).Scan(&kind, &status, &scope)
	if kind != "pulse" || status != "succeeded" || scope != "readonly" {
		t.Fatalf("run %s %s %s", kind, status, scope)
	}
	if len(n.msgs) != 1 || !strings.Contains(n.msgs[0], "Steuererklärung") {
		t.Fatalf("nudge: %v", n.msgs)
	}
	notesList, _ := mem.List(ctx, dot, memory.Note, 10)
	if len(notesList) != 1 {
		t.Fatal("note nicht gespeichert")
	}
	// Zweiter Tick direkt danach: kein neuer Pulse (Takt), kein doppeltes Signal.
	pe.Tick(ctx)
	eng.Lanes.Wait()
	var runs int
	pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&runs)
	if runs != 1 {
		t.Fatalf("%d runs", runs)
	}
}

func TestRoutineScheduling(t *testing.T) {
	pool, _ := testdb.New(t)
	ctx := context.Background()
	ws, dot := uuid.New(), uuid.New()
	pool.Exec(ctx, `INSERT INTO workspaces (id,name) VALUES ($1,'w')`, ws)
	pool.Exec(ctx, `INSERT INTO dots (id,workspace_id,name,kind,pulse_config) VALUES ($1,$2,'H','personal','{"interval_min":0}')`, dot, ws)
	pool.Exec(ctx, `INSERT INTO schedules (id,dot_id,kind,cron,prompt,tool_scope) VALUES ($1,$2,'routine','0 8 * * 1','Wochenvorschau','readonly')`, uuid.New(), dot)
	sl := &llm.Scripted{Func: func(llm.Request) (*llm.Response, error) { r := llm.Text("Vorschau"); return &r, nil }}
	eng := &runtime.Engine{Store: &runtime.Store{Pool: pool}, LLM: sl, Tools: tools.NewRegistry(), Lanes: runtime.NewLanes(2)}
	now := time.Date(2026, 10, 5, 5, 59, 0, 0, time.UTC) // 07:59 Europe/Berlin (Default-Zeitzone)
	pe := &Engine{Pool: pool, Runtime: eng, Now: func() time.Time { return now }}
	pe.Tick(ctx) // initialisiert next_run_at
	now = now.Add(2 * time.Minute)
	pe.Tick(ctx)
	pe.Tick(ctx)
	eng.Lanes.Wait()
	var runs int
	pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE kind='routine' AND tool_scope='readonly'`).Scan(&runs)
	if runs != 1 {
		t.Fatalf("routine-runs: %d", runs)
	}
}
