package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/realblxckcodex/fylgja/internal/platform/clock"
)

type fakeProv struct {
	mu         sync.Mutex
	pods       map[string]ProviderPod
	n          int
	terminated []string
}

func (f *fakeProv) Name() string { return "runpod" }
func (f *fakeProv) Provision(_ context.Context, s PodSpec) (ProviderPod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	p := ProviderPod{ID: "pod" + string(rune('0'+f.n)), Name: Tag + "-" + s.Name, Status: "RUNNING", GPUType: s.GPUType, GPUCount: 1, CostPerHour: 2_000_000, Tagged: true}
	if f.pods == nil {
		f.pods = map[string]ProviderPod{}
	}
	f.pods[p.ID] = p
	return p, nil
}
func (f *fakeProv) Status(_ context.Context, id string) (ProviderPod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pods[id]
	if !ok {
		return p, ErrNotFound
	}
	return p, nil
}
func (f *fakeProv) Terminate(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pods, id)
	f.terminated = append(f.terminated, id)
	return nil
}
func (f *fakeProv) List(context.Context) ([]ProviderPod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ProviderPod
	for _, p := range f.pods {
		out = append(out, p)
	}
	return out, nil
}
func (f *fakeProv) ListOffers(context.Context) ([]Offer, error) { return nil, nil }

type hook struct {
	mu                   sync.Mutex
	ready, drained, gone []string
}

func (h *hook) NodeReady(n *Node)    { h.mu.Lock(); h.ready = append(h.ready, n.ID); h.mu.Unlock() }
func (h *hook) NodeDraining(n *Node) { h.mu.Lock(); h.drained = append(h.drained, n.ID); h.mu.Unlock() }
func (h *hook) NodeGone(n *Node)     { h.mu.Lock(); h.gone = append(h.gone, n.ID); h.mu.Unlock() }
func (h *hook) NodeMetrics(*Node)    {}

type pressure struct{ wait, kv float64 }

func (p *pressure) QueuePressure(string) (float64, float64, int) { return p.wait, p.kv, 0 }

func TestLifecycleProvisionReadyTerminate(t *testing.T) {
	fp := &fakeProv{}
	h := &hook{}
	ck := clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	m := NewManager(fp, h, nil, ck, nil)
	m.SetPolicy(Policy{Name: "main", Model: "worker", MinNodes: 0, MaxNodes: 2, Enabled: true, Spec: PodSpec{GPUType: "NVIDIA L40S", Image: "fylgja-node"}})
	n, err := m.Provision(context.Background(), "main")
	if err != nil || n.State != Provisioning {
		t.Fatal(err)
	}
	if _, ok := m.Authenticate(n.ID, "falsch"); ok {
		t.Fatal("falsches token akzeptiert")
	}
	if _, ok := m.Authenticate(n.ID, n.token); !ok {
		t.Fatal("token abgelehnt")
	}
	m.Register(n.ID, Registration{GPUModel: "L40S", VRAMGB: 48, Deployments: []NodeDeployment{{Model: "worker", ServedModel: "qwen"}}})
	m.MarkReady(n.ID)
	if got, _ := m.Node(n.ID); got.State != Ready || len(h.ready) != 1 {
		t.Fatal("nicht ready")
	}
	if err := m.Terminate(context.Background(), n.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Node(n.ID)
	if got.State != Gone || len(fp.pods) != 0 || len(h.gone) != 1 {
		t.Fatalf("terminate: %+v", got)
	}
	if _, ok := m.Authenticate(n.ID, n.token); ok {
		t.Fatal("token nach terminate gültig")
	}
}

func TestOrphanReconciler(t *testing.T) {
	fp := &fakeProv{pods: map[string]ProviderPod{
		"lost":    {ID: "lost", Name: Tag + "-old", Tagged: true},
		"foreign": {ID: "foreign", Name: "my-own-pod"},
	}}
	var alerts []Alert
	m := NewManager(fp, nil, nil, nil, nil)
	m.OnAlert = func(a Alert) { alerts = append(alerts, a) }
	orphans, err := m.Reconcile(context.Background())
	if err != nil || len(orphans) != 1 || orphans[0].ID != "lost" {
		t.Fatalf("%v %+v", err, orphans)
	}
	if _, ok := fp.pods["foreign"]; !ok {
		t.Fatal("fremder pod terminiert!")
	}
	if _, ok := fp.pods["lost"]; ok || len(alerts) == 0 {
		t.Fatal("verwaister pod lebt noch / kein alarm")
	}
}

func TestHeartbeatTimeout(t *testing.T) {
	ck := clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	h := &hook{}
	m := NewManager(&fakeProv{}, h, nil, ck, nil)
	n := m.AddStatic(Node{Name: "local-1"}, "t")
	m.Register(n.ID, Registration{})
	m.MarkReady(n.ID)
	m.Heartbeat(n.ID, Metrics{GPUUtil: 0.5})
	ck.Advance(25 * time.Second)
	m.Tick(context.Background())
	if got, _ := m.Node(n.ID); got.State != Failed || len(h.drained) != 1 {
		t.Fatalf("state %s", got.State)
	}
	m.Heartbeat(n.ID, Metrics{})
	if got, _ := m.Node(n.ID); got.State != Ready {
		t.Fatal("erholt sich nicht")
	}
}

func samples(start time.Time, n int, step time.Duration, s Sample) []Sample {
	var out []Sample
	for i := 0; i < n; i++ {
		x := s
		x.At = start.Add(time.Duration(i) * step)
		out = append(out, x)
	}
	return out
}

func TestAutoscaleHysteresis(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	p := Policy{Name: "main", MinNodes: 1, MaxNodes: 3, Enabled: true, Up: ScaleUp{WaitP95MS: 5000, Window: 2 * time.Minute}, Down: ScaleDown{Util: 0.2, Window: 10 * time.Minute}, Cooldown: 5 * time.Minute}
	ready := &Node{ID: "a", State: Ready, StartedAt: now.Add(-time.Hour)}
	// Kurzer Spike (< Fenster) → kein Scale-up.
	short := samples(now.Add(-time.Minute), 5, 15*time.Second, Sample{WaitP95: 9000})
	if a := Evaluate(p, ScaleInput{Now: now, Samples: short, Nodes: []*Node{ready}}); a.Kind != None {
		t.Fatalf("spike: %+v", a)
	}
	// Anhaltende Last → Scale-up.
	long := samples(now.Add(-3*time.Minute), 13, 15*time.Second, Sample{WaitP95: 9000})
	if a := Evaluate(p, ScaleInput{Now: now, Samples: long, Nodes: []*Node{ready}}); a.Kind != Up {
		t.Fatalf("last: %+v", a)
	}
	// Im Cooldown → nichts.
	if a := Evaluate(p, ScaleInput{Now: now, Samples: long, Nodes: []*Node{ready}, LastScale: now.Add(-time.Minute)}); a.Kind != None {
		t.Fatalf("cooldown: %+v", a)
	}
	// Ein Node startet bereits → nicht doppelt.
	prov := &Node{ID: "b", State: Provisioning}
	if a := Evaluate(p, ScaleInput{Now: now, Samples: long, Nodes: []*Node{ready, prov}}); a.Kind != None {
		t.Fatalf("doppelt: %+v", a)
	}
	// Leerlauf über langes Fenster mit 2 Nodes → Scale-down des jüngsten/leersten.
	idle := samples(now.Add(-11*time.Minute), 45, 15*time.Second, Sample{Util: 0.05})
	young := &Node{ID: "young", State: Ready, StartedAt: now.Add(-10 * time.Minute)}
	if a := Evaluate(p, ScaleInput{Now: now, Samples: idle, Nodes: []*Node{ready, young}}); a.Kind != Down || a.NodeID != "young" {
		t.Fatalf("down: %+v", a)
	}
	// Nie unter min_nodes.
	if a := Evaluate(p, ScaleInput{Now: now, Samples: idle, Nodes: []*Node{ready}}); a.Kind != None {
		t.Fatalf("min: %+v", a)
	}
	// Unter min_nodes → hoch.
	if a := Evaluate(p, ScaleInput{Now: now}); a.Kind != Up {
		t.Fatalf("min up: %+v", a)
	}
}

func TestBudgetGuard(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ready := &Node{ID: "a", State: Ready}
	long := samples(now.Add(-3*time.Minute), 13, 15*time.Second, Sample{WaitP95: 99999})
	soft := Policy{Name: "p", MinNodes: 1, MaxNodes: 3, Enabled: true, DailyBudget: 100, Hard: false}
	if a := Evaluate(soft, ScaleInput{Now: now, Samples: long, Nodes: []*Node{ready}, SpendToday: 85}); a.Kind != None || a.Alert != "budget_soft" {
		t.Fatalf("soft: %+v", a)
	}
	hard := soft
	hard.Hard = true
	if a := Evaluate(hard, ScaleInput{Now: now, Nodes: []*Node{ready}, SpendToday: 100}); a.Kind != DrainAll {
		t.Fatalf("hard: %+v", a)
	}
}

func TestSpendAccrual(t *testing.T) {
	ck := clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	fp := &fakeProv{}
	m := NewManager(fp, nil, nil, ck, nil)
	m.SetPolicy(Policy{Name: "p", MaxNodes: 1, Enabled: true})
	m.Provision(context.Background(), "p")
	m.Tick(context.Background())
	ck.Advance(30 * time.Minute)
	m.Tick(context.Background())
	if s := m.SpendToday(); s != 1_000_000 {
		t.Fatalf("spend %d", s)
	}
}

func TestRunPodProvider(t *testing.T) {
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rpa_test" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/pods":
			json.NewDecoder(r.Body).Decode(&created)
			w.Write([]byte(`{"id":"p1","name":"fylgja-fleet-main-x","desiredStatus":"RUNNING","costPerHr":0.79,"gpuCount":1,"machine":{"gpuTypeId":"NVIDIA L40S","dataCenterId":"EU-RO-1"},"env":{"FYLGJA_FLEET_TAG":"fylgja-fleet"}}`))
		case r.Method == "GET" && r.URL.Path == "/pods":
			w.Write([]byte(`[{"id":"p1","name":"fylgja-fleet-main-x","env":{"FYLGJA_FLEET_TAG":"fylgja-fleet"}},{"id":"p2","name":"private"}]`))
		case r.Method == "DELETE" && r.URL.Path == "/pods/p1":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	rp := &RunPod{BaseURL: srv.URL, APIKey: "rpa_test"}
	p, err := rp.Provision(context.Background(), PodSpec{Name: "main-x", GPUType: "NVIDIA L40S", Image: "img", NetworkVolumeID: "vol1", Region: "EU-RO-1"})
	if err != nil || p.ID != "p1" || !p.Tagged || p.CostPerHour != 726800 || p.Region != "EU-RO-1" {
		t.Fatalf("%v %+v", err, p)
	}
	if !strings.HasPrefix(created["name"].(string), Tag) || created["networkVolumeId"] != "vol1" {
		t.Fatalf("body %v", created)
	}
	pods, _ := rp.List(context.Background())
	if len(pods) != 2 || !pods[0].Tagged || pods[1].Tagged {
		t.Fatalf("%+v", pods)
	}
	if err := rp.Terminate(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if err := rp.Terminate(context.Background(), "gone"); err != nil {
		t.Fatal("404 beim terminate muss ok sein")
	}
}
