package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/realblxckcodex/fylgja/internal/platform/clock"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
)

// NodeState (7.3).
type NodeState string

const (
	Provisioning NodeState = "provisioning"
	Ready        NodeState = "ready"
	Draining     NodeState = "draining"
	Terminating  NodeState = "terminating"
	Gone         NodeState = "gone"
	Failed       NodeState = "failed"
)

// Metrics sind die vom Node-Agent gemeldeten Werte (16.9).
type Metrics struct {
	TS            time.Time `json:"ts"`
	GPUUtil       float64   `json:"gpu_util"`
	VRAMUsedMB    int       `json:"vram_used_mb"`
	VRAMTotalMB   int       `json:"vram_total_mb"`
	TempC         float64   `json:"temp_c"`
	PowerW        float64   `json:"power_w"`
	RunningReqs   int       `json:"running_reqs"`
	QueuedReqs    int       `json:"queued_reqs"`
	TokensPerS    float64   `json:"tokens_per_s"`
	KVCacheUtil   float64   `json:"kv_cache_util"`
	PrefixHitRate float64   `json:"prefix_hit_rate"`
}

// Node ist ein GPU- oder lokaler Inference-Node.
type Node struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Pool          string    `json:"pool"`
	Provider      string    `json:"provider"`
	ProviderRef   string    `json:"provider_ref"`
	GPUModel      string    `json:"gpu_model"`
	GPUCount      int       `json:"gpu_count"`
	VRAMGB        int       `json:"vram_gb"`
	Region        string    `json:"region"`
	State         NodeState `json:"state"`
	TunnelState   string    `json:"tunnel_state"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	HourlyCost    int64     `json:"hourly_cost_micro_eur"`
	StartedAt     time.Time `json:"started_at"`
	ReadyAt       time.Time `json:"ready_at,omitzero"`
	TerminatedAt  time.Time `json:"terminated_at,omitzero"`
	Metrics       Metrics   `json:"metrics"`
	Deployments   []NodeDeployment `json:"deployments"`
	// token authentifiziert den Node beim Tunnel-Aufbau (einmalig ausgegeben).
	token string
}

// Alert ist ein Alarm an den Owner (16.11).
type Alert struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	NodeID  string    `json:"node_id,omitempty"`
	At      time.Time `json:"at"`
}

// RouterHook verbindet Fleet und Router (Deployments ready/draining/entfernen).
type RouterHook interface {
	NodeReady(n *Node)
	NodeDraining(n *Node)
	NodeGone(n *Node)
	NodeMetrics(n *Node)
}

// Pressure liefert Router-Kennzahlen je logischem Modell.
type Pressure interface {
	QueuePressure(model string) (waitP95 float64, kvMax float64, waiting int)
}

// Manager verwaltet die Flotte.
type Manager struct {
	Provider  Provider
	Hook      RouterHook
	Pressure  Pressure
	Clock     clock.Clock
	Log       *slog.Logger
	OnAlert   func(Alert)
	OnEvent   func(kind string, detail map[string]any) // Audit/Scale-Event-Historie
	RouterURL string                                   // für die Node-Env (Tunnel-Ziel)

	HeartbeatTimeout time.Duration // Default 20 s

	mu        sync.Mutex
	nodes     map[string]*Node
	policies  map[string]*Policy
	samples   map[string][]Sample
	lastScale map[string]time.Time
	spendDay  string
	spend     int64 // µ€ heute (akkumuliert)
	lastTick  time.Time
	events    []ScaleEvent
}

// ScaleEvent ist ein Eintrag der Scale-Historie (18.5 E).
type ScaleEvent struct {
	At     time.Time  `json:"at"`
	Pool   string     `json:"pool"`
	Kind   ActionKind `json:"kind"`
	NodeID string     `json:"node_id,omitempty"`
	Reason string     `json:"reason"`
}

func NewManager(p Provider, hook RouterHook, pressure Pressure, c clock.Clock, log *slog.Logger) *Manager {
	if c == nil {
		c = clock.Real
	}
	if log == nil {
		log = slog.Default()
	}
	return &Manager{Provider: p, Hook: hook, Pressure: pressure, Clock: c, Log: log, HeartbeatTimeout: 20 * time.Second,
		nodes: map[string]*Node{}, policies: map[string]*Policy{}, samples: map[string][]Sample{}, lastScale: map[string]time.Time{}}
}

func (m *Manager) alert(a Alert) {
	a.At = m.Clock.Now()
	m.Log.Warn("fleet alert", "kind", a.Kind, "msg", a.Message, "node", a.NodeID)
	if m.OnAlert != nil {
		m.OnAlert(a)
	}
}

func (m *Manager) event(kind string, detail map[string]any) {
	if m.OnEvent != nil {
		m.OnEvent(kind, detail)
	}
}

// SetPolicy legt eine Pool-Policy an.
func (m *Manager) SetPolicy(p Policy) {
	m.mu.Lock()
	cp := p
	m.policies[p.Name] = &cp
	m.mu.Unlock()
}

func (m *Manager) Policies() []Policy {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Policy
	for _, p := range m.policies {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AddStatic registriert einen manuell betriebenen Node (lokaler Server).
func (m *Manager) AddStatic(n Node, token string) *Node {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n.ID == "" {
		n.ID = ids.New().String()
	}
	n.Provider = "static"
	n.State = Provisioning
	n.StartedAt = m.Clock.Now()
	n.token = token
	m.nodes[n.ID] = &n
	return &n
}

// Provision startet einen neuen Node für einen Pool.
func (m *Manager) Provision(ctx context.Context, pool string) (*Node, error) {
	m.mu.Lock()
	p, ok := m.policies[pool]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fleet: pool %q unbekannt", pool)
	}
	id := ids.New().String()
	token := ids.New().String() + ids.New().String()
	spec := p.Spec
	spec.Name = pool + "-" + id[len(id)-6:]
	env := map[string]string{"FYLGJA_NODE_ID": id, "FYLGJA_NODE_TOKEN": token, "FYLGJA_ROUTER_URL": m.RouterURL}
	for k, v := range spec.Env {
		env[k] = v
	}
	spec.Env = env
	pod, err := m.Provider.Provision(ctx, spec)
	if err != nil {
		m.alert(Alert{Kind: "provision_failed", Message: err.Error()})
		return nil, err
	}
	n := &Node{ID: id, Name: spec.Name, Pool: pool, Provider: m.Provider.Name(), ProviderRef: pod.ID, GPUModel: firstNonEmpty(pod.GPUType, spec.GPUType),
		GPUCount: max(pod.GPUCount, spec.GPUCount), Region: firstNonEmpty(pod.Region, spec.Region), State: Provisioning, TunnelState: "down",
		HourlyCost: pod.CostPerHour, StartedAt: m.Clock.Now(), token: token}
	m.mu.Lock()
	m.nodes[id] = n
	m.lastScale[pool] = m.Clock.Now()
	m.mu.Unlock()
	m.event("fleet.provision", map[string]any{"node": id, "pool": pool, "pod": pod.ID})
	return n, nil
}

// Authenticate prüft das Node-Token beim Tunnel-Aufbau.
func (m *Manager) Authenticate(nodeID, token string) (*Node, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[nodeID]
	if !ok || n.token == "" || n.token != token || n.State == Gone || n.State == Terminating {
		return nil, false
	}
	return n, true
}

// Registration meldet der Node-Agent nach Tunnel-Aufbau.
type Registration struct {
	GPUModel    string   `json:"gpu_model"`
	GPUCount    int      `json:"gpu_count"`
	VRAMGB      int      `json:"vram_gb"`
	Deployments []NodeDeployment `json:"deployments"` // geladene Modelle
	Region      string           `json:"region"`
}

// NodeDeployment beschreibt ein auf dem Node geladenes Modell.
type NodeDeployment struct {
	Model          string `json:"model"`        // logisches Modell im Katalog
	ServedModel    string `json:"served_model"` // Name in vLLM/Ollama
	Engine         string `json:"engine"`       // vllm|ollama|llamacpp
	MaxConcurrency int    `json:"max_concurrency"`
}

// Register markiert den Tunnel als verbunden. Ready wird erst nach erfolgreicher Health-Probe gesetzt.
func (m *Manager) Register(nodeID string, reg Registration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[nodeID]
	if !ok {
		return fmt.Errorf("fleet: node %s unbekannt", nodeID)
	}
	n.TunnelState = "up"
	n.LastHeartbeat = m.Clock.Now()
	if reg.GPUModel != "" {
		n.GPUModel = reg.GPUModel
	}
	if reg.GPUCount > 0 {
		n.GPUCount = reg.GPUCount
	}
	if reg.VRAMGB > 0 {
		n.VRAMGB = reg.VRAMGB
	}
	if reg.Region != "" && n.Region == "" {
		n.Region = reg.Region
	}
	n.Deployments = reg.Deployments
	return nil
}

// MarkReady wird nach erfolgreicher Health-Probe aufgerufen.
func (m *Manager) MarkReady(nodeID string) {
	m.mu.Lock()
	n, ok := m.nodes[nodeID]
	if ok && (n.State == Provisioning || n.State == Failed) {
		n.State = Ready
		n.ReadyAt = m.Clock.Now()
	}
	m.mu.Unlock()
	if ok && m.Hook != nil {
		m.Hook.NodeReady(n)
	}
	if ok {
		m.event("fleet.ready", map[string]any{"node": nodeID})
	}
}

// Heartbeat übernimmt Metriken (alle 2–5 s).
func (m *Manager) Heartbeat(nodeID string, met Metrics) {
	m.mu.Lock()
	n, ok := m.nodes[nodeID]
	if ok {
		n.LastHeartbeat = m.Clock.Now()
		met.TS = n.LastHeartbeat
		n.Metrics = met
		if n.State == Failed && n.TunnelState == "up" {
			n.State = Ready
		}
	}
	m.mu.Unlock()
	if ok && m.Hook != nil {
		m.Hook.NodeMetrics(n)
	}
}

// TunnelDown wird beim Verbindungsabbruch aufgerufen.
func (m *Manager) TunnelDown(nodeID string) {
	m.mu.Lock()
	if n, ok := m.nodes[nodeID]; ok {
		n.TunnelState = "down"
	}
	m.mu.Unlock()
}

// Drain nimmt einen Node aus dem Routing; laufende Streams dürfen enden.
func (m *Manager) Drain(nodeID string) error {
	m.mu.Lock()
	n, ok := m.nodes[nodeID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("fleet: node %s unbekannt", nodeID)
	}
	n.State = Draining
	m.mu.Unlock()
	if m.Hook != nil {
		m.Hook.NodeDraining(n)
	}
	m.event("fleet.drain", map[string]any{"node": nodeID})
	return nil
}

// Terminate beendet einen Node beim Provider.
func (m *Manager) Terminate(ctx context.Context, nodeID string) error {
	m.mu.Lock()
	n, ok := m.nodes[nodeID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("fleet: node %s unbekannt", nodeID)
	}
	n.State = Terminating
	ref, prov := n.ProviderRef, n.Provider
	m.mu.Unlock()
	if m.Hook != nil {
		m.Hook.NodeDraining(n)
	}
	if prov != "static" && ref != "" {
		if err := m.Provider.Terminate(ctx, ref); err != nil {
			m.alert(Alert{Kind: "terminate_failed", NodeID: nodeID, Message: err.Error()})
			return err
		}
	}
	m.mu.Lock()
	n.State, n.TerminatedAt, n.token = Gone, m.Clock.Now(), ""
	m.mu.Unlock()
	if m.Hook != nil {
		m.Hook.NodeGone(n)
	}
	m.event("fleet.terminate", map[string]any{"node": nodeID})
	return nil
}

// Nodes liefert eine Kopie aller Nodes.
func (m *Manager) Nodes() []Node {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// Node liefert einen Node.
func (m *Manager) Node(id string) (Node, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

// Events liefert die Scale-Historie.
func (m *Manager) Events() []ScaleEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ScaleEvent(nil), m.events...)
}

// SpendToday liefert die heutigen Flottenkosten (µ€).
func (m *Manager) SpendToday() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spend
}

// accrue bucht Stundenkosten für die seit dem letzten Tick vergangene Zeit.
func (m *Manager) accrueLocked(now time.Time) {
	day := now.Format("2006-01-02")
	if day != m.spendDay {
		m.spendDay, m.spend = day, 0
	}
	if !m.lastTick.IsZero() {
		dt := now.Sub(m.lastTick)
		for _, n := range m.nodes {
			switch n.State {
			case Provisioning, Ready, Draining, Terminating:
				m.spend += int64(float64(n.HourlyCost) * dt.Hours())
			}
		}
	}
	m.lastTick = now
}

// Tick führt eine Auswertungsrunde aus (Default alle 15 s): Heartbeats, Kosten, Autoscaling.
func (m *Manager) Tick(ctx context.Context) {
	now := m.Clock.Now()
	var stale []*Node
	m.mu.Lock()
	m.accrueLocked(now)
	for _, n := range m.nodes {
		if n.State == Ready && !n.LastHeartbeat.IsZero() && now.Sub(n.LastHeartbeat) > m.HeartbeatTimeout {
			n.State = Failed
			stale = append(stale, n)
		}
	}
	type job struct {
		p  Policy
		in ScaleInput
	}
	var jobs []job
	for name, p := range m.policies {
		var nodes []*Node
		var util float64
		var readyN int
		for _, n := range m.nodes {
			if n.Pool != name || n.State == Gone || n.State == Failed || n.State == Terminating {
				continue
			}
			nodes = append(nodes, n)
			if n.State == Ready {
				readyN++
				util += n.Metrics.GPUUtil
			}
		}
		s := Sample{At: now}
		if readyN > 0 {
			s.Util = util / float64(readyN)
		}
		if m.Pressure != nil && p.Model != "" {
			s.WaitP95, s.KVMax, s.Waiting = m.Pressure.QueuePressure(p.Model)
		}
		hist := append(m.samples[name], s)
		cut := 0
		for cut < len(hist) && now.Sub(hist[cut].At) > time.Hour {
			cut++
		}
		m.samples[name] = hist[cut:]
		jobs = append(jobs, job{p: *p, in: ScaleInput{Now: now, Samples: m.samples[name], Nodes: nodes, SpendToday: m.spend, LastScale: m.lastScale[name]}})
	}
	m.mu.Unlock()

	for _, n := range stale {
		m.alert(Alert{Kind: "node_unhealthy", NodeID: n.ID, Message: fmt.Sprintf("kein heartbeat seit %s", m.HeartbeatTimeout)})
		if m.Hook != nil {
			m.Hook.NodeDraining(n)
		}
	}
	for _, j := range jobs {
		a := Evaluate(j.p, j.in)
		if a.Alert != "" {
			m.alert(Alert{Kind: a.Alert, Message: j.p.Name + ": " + a.Reason})
		}
		if a.Kind == None {
			continue
		}
		m.mu.Lock()
		m.events = append(m.events, ScaleEvent{At: now, Pool: j.p.Name, Kind: a.Kind, NodeID: a.NodeID, Reason: a.Reason})
		if len(m.events) > 500 {
			m.events = m.events[len(m.events)-500:]
		}
		m.lastScale[j.p.Name] = now
		m.mu.Unlock()
		switch a.Kind {
		case Up:
			if _, err := m.Provision(ctx, j.p.Name); err != nil {
				m.Log.Error("scale-up fehlgeschlagen", "pool", j.p.Name, "err", err)
			}
		case Down:
			_ = m.Drain(a.NodeID)
			go m.terminateWhenIdle(context.WithoutCancel(ctx), a.NodeID, 5*time.Minute)
		case DrainAll:
			for _, n := range j.in.Nodes {
				_ = m.Drain(n.ID)
				go m.terminateWhenIdle(context.WithoutCancel(ctx), n.ID, 2*time.Minute)
			}
		}
	}
	// Draining-Nodes ohne laufende Requests terminieren.
	for _, n := range m.Nodes() {
		if n.State == Draining && n.Metrics.RunningReqs == 0 && n.Provider != "static" {
			_ = m.Terminate(ctx, n.ID)
		}
	}
}

func (m *Manager) terminateWhenIdle(ctx context.Context, id string, maxWait time.Duration) {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		n, ok := m.Node(id)
		if !ok || n.State == Gone {
			return
		}
		if n.Metrics.RunningReqs == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	if n, ok := m.Node(id); ok && n.State == Draining && n.Provider != "static" {
		_ = m.Terminate(ctx, id)
	}
}

// Reconcile vergleicht Provider-Bestand mit der Registry und terminiert verwaiste Pods (minütlich).
func (m *Manager) Reconcile(ctx context.Context) (orphans []ProviderPod, err error) {
	pods, err := m.Provider.List(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	known := map[string]*Node{}
	for _, n := range m.nodes {
		if n.ProviderRef != "" && n.State != Gone {
			known[n.ProviderRef] = n
		}
	}
	m.mu.Unlock()
	seen := map[string]bool{}
	for _, p := range pods {
		seen[p.ID] = true
		if !p.Tagged {
			continue // fremde Pods fassen wir nie an
		}
		if _, ok := known[p.ID]; !ok {
			orphans = append(orphans, p)
		}
	}
	for _, p := range orphans {
		m.alert(Alert{Kind: "orphan_pod", Message: fmt.Sprintf("verwaister pod %s (%s) wird terminiert", p.ID, p.Name)})
		if err := m.Provider.Terminate(ctx, p.ID); err != nil {
			m.Log.Error("orphan terminate", "pod", p.ID, "err", err)
		}
		m.event("fleet.orphan_terminated", map[string]any{"pod": p.ID})
	}
	// Nodes, deren Pod verschwunden ist.
	for ref, n := range known {
		if n.Provider == m.Provider.Name() && !seen[ref] && n.State != Provisioning {
			m.mu.Lock()
			n.State, n.TerminatedAt = Gone, m.Clock.Now()
			m.mu.Unlock()
			if m.Hook != nil {
				m.Hook.NodeGone(n)
			}
			m.alert(Alert{Kind: "node_gone", NodeID: n.ID, Message: "pod beim provider verschwunden"})
		}
	}
	return orphans, nil
}

// Run führt Tick (EvalInterval) und Reconcile (minütlich) aus, bis ctx endet.
func (m *Manager) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 15 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	rec := time.NewTicker(time.Minute)
	defer rec.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Tick(ctx)
		case <-rec.C:
			if _, err := m.Reconcile(ctx); err != nil {
				m.Log.Warn("reconcile", "err", err)
			}
		}
	}
}
