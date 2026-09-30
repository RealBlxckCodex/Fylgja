package fleet

import (
	"fmt"
	"time"
)

// ScaleUp-Regeln (16.8): Queue-Wartezeit p95 > Schwelle über Fenster ODER KV-Cache > 85 % über Fenster, UND Budget erlaubt.
type ScaleUp struct {
	WaitP95MS float64       `json:"wait_p95_ms"`
	KVUtil    float64       `json:"kv_util"`
	Window    time.Duration `json:"window"`
}

// ScaleDown: Auslastung < Schwelle über langes Fenster.
type ScaleDown struct {
	Util   float64       `json:"util"`
	Window time.Duration `json:"window"`
}

// Policy entspricht fleet_policies.
type Policy struct {
	Name        string        `json:"name"`
	Model       string        `json:"model"` // logisches Modell, das dieser Pool bedient
	MinNodes    int           `json:"min_nodes"`
	MaxNodes    int           `json:"max_nodes"`
	Up          ScaleUp       `json:"scale_up"`
	Down        ScaleDown     `json:"scale_down"`
	Cooldown    time.Duration `json:"cooldown"`
	DailyBudget int64         `json:"daily_budget_micro_eur"`
	Hard        bool          `json:"hard"`
	Enabled     bool          `json:"enabled"`
	Spec        PodSpec       `json:"spec"`
	// ExpectedReady: typische Zeit bis ready (fließt in Vorwärmen ein).
	ExpectedReady time.Duration `json:"expected_ready"`
	// Prewarm: Zeitfenster (lokale Stunden), in denen min_nodes+1 vorgehalten werden (Nutzungsmuster).
	PrewarmHours []int `json:"prewarm_hours"`
}

func (p *Policy) defaults() {
	if p.Up.Window == 0 {
		p.Up.Window = 2 * time.Minute
	}
	if p.Up.KVUtil == 0 {
		p.Up.KVUtil = 0.85
	}
	if p.Up.WaitP95MS == 0 {
		p.Up.WaitP95MS = 10000
	}
	if p.Down.Window == 0 {
		p.Down.Window = 20 * time.Minute
	}
	if p.Down.Util == 0 {
		p.Down.Util = 0.15
	}
	if p.Cooldown == 0 {
		p.Cooldown = 5 * time.Minute
	}
}

// Sample ist ein Messpunkt für die Autoscaling-Auswertung (alle 15 s).
type Sample struct {
	At      time.Time
	WaitP95 float64 // ms
	KVMax   float64
	Util    float64 // mittlere Auslastung (laufende Requests / Kapazität) über ready-Nodes
	Waiting int
}

// ActionKind des Autoscalers.
type ActionKind string

const (
	None     ActionKind = "none"
	Up       ActionKind = "scale_up"
	Down     ActionKind = "scale_down"
	DrainAll ActionKind = "drain_noncritical"
)

// Action ist eine Autoscaling-Entscheidung.
type Action struct {
	Kind   ActionKind `json:"kind"`
	NodeID string     `json:"node_id,omitempty"`
	Reason string     `json:"reason"`
	Alert  string     `json:"alert,omitempty"`
}

// ScaleInput fasst den Zustand eines Pools zusammen.
type ScaleInput struct {
	Now        time.Time
	Samples    []Sample // chronologisch
	Nodes      []*Node  // Nodes dieses Pools (ohne gone/failed)
	SpendToday int64    // µ€
	LastScale  time.Time
}

func holds(samples []Sample, now time.Time, window time.Duration, f func(Sample) bool) bool {
	if len(samples) == 0 {
		return false
	}
	// Das Fenster muss vollständig abgedeckt sein.
	if now.Sub(samples[0].At) < window {
		return false
	}
	for i := len(samples) - 1; i >= 0; i-- {
		s := samples[i]
		if now.Sub(s.At) > window {
			break
		}
		if !f(s) {
			return false
		}
	}
	return true
}

// Evaluate ist die reine Autoscaling-Funktion (deterministisch, testbar).
func Evaluate(p Policy, in ScaleInput) Action {
	p.defaults()
	if !p.Enabled {
		return Action{Kind: None, Reason: "policy deaktiviert"}
	}
	active, ready := 0, 0
	var idleCandidate *Node
	for _, n := range in.Nodes {
		switch n.State {
		case Provisioning, Ready:
			active++
		}
		if n.State == Ready {
			ready++
			if idleCandidate == nil || n.Metrics.RunningReqs < idleCandidate.Metrics.RunningReqs ||
				(n.Metrics.RunningReqs == idleCandidate.Metrics.RunningReqs && n.StartedAt.After(idleCandidate.StartedAt)) {
				idleCandidate = n
			}
		}
	}
	// Kosten-Wächter.
	if p.DailyBudget > 0 && in.SpendToday >= p.DailyBudget {
		if p.Hard && active > 0 {
			return Action{Kind: DrainAll, Reason: "hartes tagesbudget erreicht", Alert: "budget_hard"}
		}
		if active > p.MinNodes && idleCandidate != nil {
			return Action{Kind: Down, NodeID: idleCandidate.ID, Reason: "tagesbudget erreicht", Alert: "budget_soft"}
		}
		return Action{Kind: None, Reason: "tagesbudget erreicht – kein scale-up", Alert: "budget_soft"}
	}
	budgetSoft := p.DailyBudget > 0 && in.SpendToday >= p.DailyBudget*8/10

	target := p.MinNodes
	for _, h := range p.PrewarmHours {
		if in.Now.Hour() == h {
			target = min(p.MinNodes+1, p.MaxNodes)
		}
	}
	if active < target {
		if budgetSoft {
			return Action{Kind: None, Reason: "unter min_nodes, aber 80 % des budgets verbraucht", Alert: "budget_soft"}
		}
		return Action{Kind: Up, Reason: fmt.Sprintf("unter mindestbestand (%d < %d)", active, target)}
	}
	cooling := !in.LastScale.IsZero() && in.Now.Sub(in.LastScale) < p.Cooldown
	pressure := holds(in.Samples, in.Now, p.Up.Window, func(s Sample) bool {
		return s.WaitP95 > p.Up.WaitP95MS || s.KVMax > p.Up.KVUtil
	})
	if pressure {
		switch {
		case active >= p.MaxNodes:
			return Action{Kind: None, Reason: "last hoch, aber max_nodes erreicht", Alert: "queue_overload"}
		case budgetSoft:
			return Action{Kind: None, Reason: "last hoch, aber budget-soft-limit", Alert: "budget_soft"}
		case cooling:
			return Action{Kind: None, Reason: "cooldown"}
		case active > ready:
			return Action{Kind: None, Reason: "node startet bereits"}
		}
		return Action{Kind: Up, Reason: "anhaltende queue-/kv-last"}
	}
	idle := holds(in.Samples, in.Now, p.Down.Window, func(s Sample) bool {
		return s.Util < p.Down.Util && s.Waiting == 0
	})
	if idle && ready > target && !cooling && idleCandidate != nil {
		return Action{Kind: Down, NodeID: idleCandidate.ID, Reason: "anhaltend geringe auslastung"}
	}
	return Action{Kind: None, Reason: "stabil"}
}
