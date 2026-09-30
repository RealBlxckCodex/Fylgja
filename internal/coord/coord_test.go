package coord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/store/testdb"
)

type fakeExec struct {
	mu        sync.Mutex
	started   []string
	cancelled []string
	fail      map[string]bool
}

func (f *fakeExec) Start(_ context.Context, _ *Graph, n *Node) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[n.Title] {
		return "", errors.New("kein worker verfügbar")
	}
	f.started = append(f.started, n.Title)
	return "", nil
}
func (f *fakeExec) Cancel(_ context.Context, n *Node) error {
	f.mu.Lock()
	f.cancelled = append(f.cancelled, n.Title)
	f.mu.Unlock()
	return nil
}

type fakeVerify struct{ failFirst map[string]int }

func (v *fakeVerify) Verify(_ context.Context, n *Node, _ json.RawMessage) (bool, string, error) {
	if v.failFirst[n.Title] > 0 {
		v.failFirst[n.Title]--
		return false, "quellen fehlen", nil
	}
	return true, "", nil
}

var schema = json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`)

func contract(goal string, scope ...string) Contract {
	return Contract{Goal: goal, OutputSchema: schema, ToolScope: scope, Budget: Budget{Tokens: 1000, WallClockS: 300}, Acceptance: []string{"korrekt"}}
}

type env struct {
	c    *Coordinator
	ex   *fakeExec
	lead string
	ws   uuid.UUID
	esc  []string
	mu   sync.Mutex
}

func setup(t *testing.T) *env {
	pool, _ := testdb.New(t)
	ctx := context.Background()
	ws, lead := uuid.New(), uuid.New()
	pool.Exec(ctx, `INSERT INTO workspaces (id,name) VALUES ($1,'w')`, ws)
	if _, err := pool.Exec(ctx, `INSERT INTO dots (id,workspace_id,name,kind) VALUES ($1,$2,'Lead','personal')`, lead, ws); err != nil {
		t.Fatal(err)
	}
	e := &env{ex: &fakeExec{fail: map[string]bool{}}, lead: lead.String(), ws: ws}
	e.c = &Coordinator{Store: &Store{Pool: pool}, Exec: e.ex, Verify: &fakeVerify{failFirst: map[string]int{}},
		Workspace: func(context.Context, string) (uuid.UUID, error) { return ws, nil },
		Escalate: func(_ context.Context, _ *Graph, n *Node, kind string) {
			e.mu.Lock()
			defer e.mu.Unlock()
			title := ""
			if n != nil {
				title = n.Title
			}
			e.esc = append(e.esc, kind+":"+title)
		}}
	return e
}

func (e *env) graph() *Graph {
	return &Graph{LeadDotID: e.lead, Title: "Hosting-Vergleich", Budget: Budget{Tokens: 10000},
		Nodes: []*Node{
			{ID: "a", Title: "A", OwnerKind: OwnerLead, Contract: contract("Anforderungen klären", "fs.read")},
			{ID: "b", Title: "B", OwnerKind: OwnerWorker, Contract: contract("Anbieter recherchieren", "web.search", "web.fetch")},
			{ID: "c", Title: "C", OwnerKind: OwnerWorker, Contract: contract("Preise sammeln", "web.*")},
			{ID: "d", Title: "D", OwnerKind: OwnerLead, Contract: contract("Empfehlung schreiben", "fs.write")},
		},
		Edges: []Edge{{"a", "b", DependsOn}, {"a", "c", DependsOn}, {"b", "d", DependsOn}, {"c", "d", DependsOn}},
	}
}

var leadScope = []string{"fs.read", "fs.write", "web.*"}

func status(t *testing.T, e *env, g *Graph) map[string]NodeStatus {
	gg, err := e.c.Store.LoadGraph(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]NodeStatus{}
	for _, n := range gg.Nodes {
		out[n.Title] = n.Status
	}
	return out
}

func nodeID(t *testing.T, e *env, g *Graph, title string) string {
	gg, _ := e.c.Store.LoadGraph(context.Background(), g.ID)
	for _, n := range gg.Nodes {
		if n.Title == title {
			return n.ID
		}
	}
	t.Fatalf("knoten %s fehlt", title)
	return ""
}

func TestStateMachine(t *testing.T) {
	ok := [][2]NodeStatus{{Pending, Ready}, {Ready, Running}, {Running, NeedsReview}, {NeedsReview, Done}, {Running, Blocked}, {Blocked, Running}, {NeedsReview, Ready}, {FailedN, Ready}}
	bad := [][2]NodeStatus{{Pending, Running}, {Done, Ready}, {CancelledN, Ready}, {Ready, Done}, {Pending, Done}}
	for _, p := range ok {
		if err := CanTransition(p[0], p[1]); err != nil {
			t.Error(err)
		}
	}
	for _, p := range bad {
		if CanTransition(p[0], p[1]) == nil {
			t.Errorf("%s→%s erlaubt", p[0], p[1])
		}
	}
}

func TestContractsShrinkRights(t *testing.T) {
	c := contract("x", "shell.run")
	if err := c.ValidateAgainst(leadScope, "", Budget{}); err == nil {
		t.Fatal("worker mit mehr rechten als lead akzeptiert")
	}
	c = contract("x", "web.search")
	c.Privacy = "any"
	if err := c.ValidateAgainst(leadScope, "self_hosted_only", Budget{}); err == nil {
		t.Fatal("gelockerte privacy akzeptiert")
	}
	e := setup(t)
	g := e.graph()
	g.Budget.Tokens = 3500 // 4 Knoten × 1000 passen nicht
	if err := e.c.Plan(context.Background(), g, leadScope); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget-reservierung nicht geprüft: %v", err)
	}
	g = e.graph()
	g.Nodes = append(g.Nodes, &Node{ID: "x", Title: "X", OwnerKind: OwnerWorker, Contract: contract("y", "a")})
	g.Edges = append(g.Edges, Edge{"d", "x", DependsOn}, Edge{"x", "a", DependsOn})
	g.Budget.Tokens = 0
	if err := e.c.Plan(context.Background(), g, nil); err == nil || !strings.Contains(err.Error(), "zyklus") {
		t.Fatalf("zyklus nicht erkannt: %v", err)
	}
}

func TestScenarioFullGraph(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	g := e.graph()
	if err := e.c.Plan(ctx, g, leadScope); err != nil {
		t.Fatal(err)
	}
	if r := Remaining(g); r.Tokens != 6000 {
		t.Fatalf("remaining %d", r.Tokens)
	}
	if err := e.c.Start(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if s := status(t, e, g); s["A"] != Running || s["B"] != Pending {
		t.Fatalf("%v", s)
	}
	ok := json.RawMessage(`{"answer":"x"}`)
	if err := e.c.Complete(ctx, g.ID, nodeID(t, e, g, "A"), ok, ""); err != nil {
		t.Fatal(err)
	}
	if s := status(t, e, g); s["A"] != Done || s["B"] != Running || s["C"] != Running {
		t.Fatalf("%v", s)
	}
	// B verletzt das Schema → Retry mit geändertem Kontext.
	if err := e.c.Complete(ctx, g.ID, nodeID(t, e, g, "B"), json.RawMessage(`{"wrong":1}`), ""); err != nil {
		t.Fatal(err)
	}
	gg, _ := e.c.Store.LoadGraph(ctx, g.ID)
	b := gg.Node(nodeID(t, e, g, "B"))
	if b.Status != Running || b.Attempt != 2 || !strings.Contains(strings.Join(b.Contract.Acceptance, "|"), "scheiterte") {
		t.Fatalf("retry: %+v", b)
	}
	// C: Reviewer lehnt einmal ab, dann ok.
	e.c.Verify.(*fakeVerify).failFirst["C"] = 1
	e.c.Complete(ctx, g.ID, nodeID(t, e, g, "C"), ok, "")
	e.c.Complete(ctx, g.ID, nodeID(t, e, g, "C"), ok, "")
	e.c.Complete(ctx, g.ID, nodeID(t, e, g, "B"), ok, "")
	if s := status(t, e, g); s["D"] != Running {
		t.Fatalf("%v", s)
	}
	e.c.Complete(ctx, g.ID, nodeID(t, e, g, "D"), ok, "")
	gg, _ = e.c.Store.LoadGraph(ctx, g.ID)
	if gg.Status != "done" {
		t.Fatalf("graph %s", gg.Status)
	}
	if !strings.Contains(strings.Join(e.esc, ","), "graph_done") {
		t.Fatalf("lead nicht geweckt: %v", e.esc)
	}
}

func TestEscalationAfterRetries(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	g := &Graph{LeadDotID: e.lead, Nodes: []*Node{{ID: "w", Title: "W", OwnerKind: OwnerWorker, Contract: contract("x", "web.search")}}}
	e.c.Plan(ctx, g, leadScope)
	e.c.Start(ctx, g.ID)
	id := nodeID(t, e, g, "W")
	for i := 0; i < 3; i++ {
		if err := e.c.Fail(ctx, g.ID, id, "timeout"); err != nil {
			t.Fatal(err)
		}
	}
	if s := status(t, e, g); s["W"] != FailedN {
		t.Fatalf("%v", s)
	}
	if !strings.Contains(strings.Join(e.esc, ","), "failed:W") {
		t.Fatalf("keine eskalation: %v", e.esc)
	}
	// Lead übernimmt selbst.
	if err := e.c.Reassign(ctx, g.ID, id, OwnerLead, e.lead, "planner"); err != nil {
		t.Fatal(err)
	}
	if s := status(t, e, g); s["W"] != Running {
		t.Fatalf("%v", s)
	}
}

func TestCancelPropagates(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	g := e.graph()
	e.c.Plan(ctx, g, leadScope)
	e.c.Start(ctx, g.ID)
	e.c.Complete(ctx, g.ID, nodeID(t, e, g, "A"), json.RawMessage(`{"answer":"x"}`), "")
	if err := e.c.Cancel(ctx, g.ID, nodeID(t, e, g, "B")); err != nil {
		t.Fatal(err)
	}
	s := status(t, e, g)
	if s["B"] != CancelledN || s["D"] != CancelledN || s["C"] != Running {
		t.Fatalf("%v", s)
	}
	if strings.Join(e.ex.cancelled, ",") != "B" {
		t.Fatalf("laufender knoten nicht gestoppt: %v", e.ex.cancelled)
	}
}

func TestWorkerLimits(t *testing.T) {
	e := setup(t)
	e.c.Limits = Limits{WorkersPerLead: 2, ParallelPerGraph: 10}
	ctx := context.Background()
	g := &Graph{LeadDotID: e.lead}
	for _, n := range []string{"1", "2", "3", "4"} {
		g.Nodes = append(g.Nodes, &Node{ID: n, Title: "W" + n, OwnerKind: OwnerWorker, Contract: contract("x", "web.search")})
	}
	e.c.Plan(ctx, g, leadScope)
	e.c.Start(ctx, g.ID)
	gg, _ := e.c.Store.LoadGraph(ctx, g.ID)
	running, waiting := 0, 0
	for _, n := range gg.Nodes {
		if n.Status == Running {
			running++
		}
		if n.Status == Ready && strings.Contains(n.Reason, "wartet auf kapazität") {
			waiting++
		}
	}
	if running != 2 || waiting != 2 {
		t.Fatalf("running=%d waiting=%d", running, waiting)
	}
}

func TestPlanEstimate(t *testing.T) {
	e := &env{lead: "l"}
	g := e.graph()
	est := g.EstimatePlan(2, []string{"web.fetch"})
	if est.Nodes != 4 || est.SelfNodes != 2 || est.DelegatedNodes != 2 || est.Minutes != 15 || len(est.NeedsApprovals) != 1 {
		t.Fatalf("%+v", est)
	}
	if cp := strings.Join(g.CriticalPath(), ""); cp != "abd" {
		t.Fatalf("kritischer pfad %s", cp)
	}
}

func TestLocksBlackboardBus(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	s := e.c.Store
	h1, h2 := uuid.NewString(), uuid.NewString()
	if err := s.AcquireLocks(ctx, h1, []string{"repo:foo", "file:/x"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.AcquireLocks(ctx, h2, []string{"file:/x"}, time.Minute); !errors.Is(err, ErrLocked) {
		t.Fatalf("doppelter lock: %v", err)
	}
	s.ReleaseLocks(ctx, h1)
	if err := s.AcquireLocks(ctx, h2, []string{"file:/x"}, time.Second); err != nil {
		t.Fatal(err)
	}
	// Blackboard
	team := uuid.New()
	s.Pool.Exec(ctx, `INSERT INTO teams (id, workspace_id, name, lead_dot_id) VALUES ($1,$2,'t',$3)`, team, e.ws, e.lead)
	by := uuid.New()
	v, err := s.BBPut(ctx, team, "zielhost", "hetzner", "untrusted", by, 0)
	if err != nil || v != 1 {
		t.Fatal(err)
	}
	if _, err := s.BBPut(ctx, team, "zielhost", "x", "owner", by, 5); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("konflikt nicht erkannt")
	}
	s.BBPut(ctx, team, "zielhost", "ovh", "owner", by, 1)
	list, _ := s.BBList(ctx, team)
	if list[0].Version != 2 || list[0].Trust != "untrusted" {
		t.Fatalf("trust nicht erhalten: %+v", list[0])
	}
	// Bus: Stern-Topologie
	parent, other := uuid.NewString(), uuid.NewString()
	w := Sender{DotID: uuid.NewString(), IsWorker: true, ParentID: parent}
	if _, err := s.Send(ctx, w, other, "", "", Report, map[string]string{"x": "y"}, 0); !errors.Is(err, ErrTopology) {
		t.Fatal("worker durfte fremden adressieren")
	}
	if _, err := s.Send(ctx, w, parent, "", "", Report, map[string]string{"done": "1"}, 0); err != nil {
		t.Fatal(err)
	}
	in, _ := s.Inbox(ctx, parent, 10)
	again, _ := s.Inbox(ctx, parent, 10)
	if len(in) != 1 || len(again) != 0 {
		t.Fatal("inbox")
	}
}
