// Package coord ist die Koordinationsschicht (Spec Kapitel 17): Teams, Work-Graph,
// Verträge, deterministischer Koordinator, Worker-Pool, Bus, Blackboard, Locks.
//
// Der Koordinator ist deterministischer Code (D12). Das Lead-LLM plant und integriert,
// aber Zustandsübergänge schreibt ausschließlich dieses Package.
package coord

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/policy"
)

// NodeStatus (17.6).
type NodeStatus string

const (
	Pending     NodeStatus = "pending"
	Ready       NodeStatus = "ready"
	Running     NodeStatus = "running"
	Blocked     NodeStatus = "blocked"
	NeedsReview NodeStatus = "needs_review"
	Done        NodeStatus = "done"
	FailedN     NodeStatus = "failed"
	CancelledN  NodeStatus = "cancelled"
)

func (s NodeStatus) Terminal() bool { return s == Done || s == FailedN || s == CancelledN }

// transitions ist der Zustandsautomat aus 17.6.
var transitions = map[NodeStatus][]NodeStatus{
	Pending:     {Ready, CancelledN},
	Ready:       {Running, CancelledN},
	Running:     {NeedsReview, Blocked, FailedN, Ready, CancelledN},
	Blocked:     {Running, Ready, FailedN, CancelledN},
	NeedsReview: {Done, Ready, FailedN, CancelledN},
	FailedN:     {Ready}, // Lead entscheidet nach Eskalation: neuer Versuch (anderer Worker/Tier/selbst)
	Done:        {},
	CancelledN:  {},
}

var ErrTransition = errors.New("coord: unzulässiger zustandsübergang")

// CanTransition prüft einen Übergang.
func CanTransition(from, to NodeStatus) error {
	for _, t := range transitions[from] {
		if t == to {
			return nil
		}
	}
	return fmt.Errorf("%w: %s → %s", ErrTransition, from, to)
}

// OwnerKind eines Knotens.
type OwnerKind string

const (
	OwnerLead   OwnerKind = "lead"
	OwnerMember OwnerKind = "member"
	OwnerWorker OwnerKind = "worker"
	OwnerHuman  OwnerKind = "human"
)

// Budget eines Vertrags.
type Budget struct {
	Tokens       int64 `json:"tokens"`
	WallClockS   int   `json:"wall_clock_s"`
	CostMicroEUR int64 `json:"cost_micro_eur"`
}

// Contract ist eine Delegation (17.3) – nie Freitext.
type Contract struct {
	Goal         string          `json:"goal"`
	Inputs       []string        `json:"inputs,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Acceptance   []string        `json:"acceptance,omitempty"`
	ToolScope    []string        `json:"tool_scope"`
	Budget       Budget          `json:"budget"`
	Priority     llm.Priority    `json:"priority,omitempty"`
	Privacy      llm.Privacy     `json:"privacy,omitempty"`
	Deadline     *time.Time      `json:"deadline,omitempty"`
	EscalateOn   []string        `json:"escalate_on,omitempty"`
	Redundancy   int             `json:"redundancy,omitempty"`
	Template     string          `json:"template,omitempty"` // Worker-Template
	Tier         string          `json:"tier,omitempty"`
	// Checks: ausführbare Prüfungen (z. B. "go test ./...", "link-check").
	Checks []string `json:"checks,omitempty"`
}

// ValidateAgainst prüft "Rechte schrumpfen nur" gegenüber dem Auftraggeber.
func (c Contract) ValidateAgainst(parentScope []string, parentPrivacy llm.Privacy, remaining Budget) error {
	if c.Goal == "" {
		return errors.New("vertrag: goal fehlt")
	}
	if len(c.OutputSchema) == 0 {
		return errors.New("vertrag: output_schema fehlt")
	}
	var s map[string]any
	if err := json.Unmarshal(c.OutputSchema, &s); err != nil {
		return fmt.Errorf("vertrag: output_schema ungültig: %w", err)
	}
	if !policy.ScopeSubset(c.ToolScope, parentScope) {
		return errors.New("vertrag: tool_scope ist keine teilmenge des auftraggeber-scopes")
	}
	if privRank(c.Privacy) < privRank(parentPrivacy) {
		return errors.New("vertrag: privacy darf nicht gelockert werden")
	}
	if remaining.Tokens > 0 && c.Budget.Tokens > remaining.Tokens {
		return fmt.Errorf("vertrag: budget %d tokens übersteigt verfügbare %d", c.Budget.Tokens, remaining.Tokens)
	}
	if remaining.CostMicroEUR > 0 && c.Budget.CostMicroEUR > remaining.CostMicroEUR {
		return errors.New("vertrag: kostenbudget übersteigt verfügbares budget")
	}
	return nil
}

func privRank(p llm.Privacy) int {
	switch p {
	case llm.AnyPrivacy:
		return 0
	case llm.EUOnly:
		return 1
	}
	return 2
}

// Node ist ein Knoten im Work-Graph.
type Node struct {
	ID         string          `json:"id"`
	GraphID    string          `json:"graph_id"`
	Title      string          `json:"title"`
	Goal       string          `json:"goal"`
	OwnerKind  OwnerKind       `json:"owner_kind"`
	OwnerDotID string          `json:"owner_dot_id,omitempty"`
	WorkerID   string          `json:"worker_id,omitempty"`
	Status     NodeStatus      `json:"status"`
	Contract   Contract        `json:"contract"`
	Result     json.RawMessage `json:"result,omitempty"`
	ResultRef  string          `json:"result_ref,omitempty"`
	Attempt    int             `json:"attempt"`
	Notes      string          `json:"notes,omitempty"` // Freitext, immer untrusted
	UpdatedAt  time.Time       `json:"updated_at"`
	RunID      string          `json:"run_id,omitempty"`
	// Reason: warum gewartet/eskaliert (UI: "wartet auf Kapazität").
	Reason string `json:"reason,omitempty"`
	// Rationale für die Self-vs-Delegate-Entscheidung (Plan-Vorschau, 18.6 D).
	Rationale string `json:"rationale,omitempty"`
}

// EdgeKind (7.3).
type EdgeKind string

const (
	DependsOn EdgeKind = "depends_on"
	Reviews   EdgeKind = "reviews"
	Feeds     EdgeKind = "feeds"
)

// Edge verbindet Knoten: From muss fertig sein, bevor To startet (depends_on/feeds).
type Edge struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Kind EdgeKind `json:"kind"`
}

// Graph ist ein Work-Graph.
type Graph struct {
	ID          string  `json:"id"`
	TeamID      string  `json:"team_id,omitempty"`
	LeadDotID   string  `json:"lead_dot_id"`
	Title       string  `json:"title"`
	Status      string  `json:"status"` // planned|running|done|failed|cancelled
	PlanVersion int     `json:"plan_version"`
	Budget      Budget  `json:"budget"`
	Nodes       []*Node `json:"nodes"`
	Edges       []Edge  `json:"edges"`
}

func (g *Graph) Node(id string) *Node {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// Validate prüft Struktur: bekannte Knoten, keine Zyklen.
func (g *Graph) Validate() error {
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		if ids[n.ID] {
			return fmt.Errorf("graph: doppelte knoten-id %s", n.ID)
		}
		ids[n.ID] = true
	}
	adj := map[string][]string{}
	for _, e := range g.Edges {
		if !ids[e.From] || !ids[e.To] {
			return fmt.Errorf("graph: kante %s→%s verweist auf unbekannten knoten", e.From, e.To)
		}
		adj[e.From] = append(adj[e.From], e.To)
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(n string) error {
		switch state[n] {
		case 1:
			return fmt.Errorf("graph: zyklus bei %s", n)
		case 2:
			return nil
		}
		state[n] = 1
		for _, m := range adj[n] {
			if err := visit(m); err != nil {
				return err
			}
		}
		state[n] = 2
		return nil
	}
	for id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// Deps liefert die Vorgänger eines Knotens (depends_on, feeds, reviews).
func (g *Graph) Deps(id string) []string {
	var out []string
	for _, e := range g.Edges {
		if e.To == id {
			out = append(out, e.From)
		}
	}
	return out
}

// Promotable liefert pending-Knoten, deren Abhängigkeiten alle done sind.
func (g *Graph) Promotable() []*Node {
	var out []*Node
	for _, n := range g.Nodes {
		if n.Status != Pending {
			continue
		}
		ok := true
		for _, d := range g.Deps(n.ID) {
			if dn := g.Node(d); dn == nil || dn.Status != Done {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, n)
		}
	}
	return out
}

// Descendants liefert alle (transitiven) Nachfolger (Cancel-Propagation).
func (g *Graph) Descendants(id string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		for _, e := range g.Edges {
			if e.From == n && !seen[e.To] {
				seen[e.To] = true
				walk(e.To)
			}
		}
	}
	walk(id)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CriticalPath liefert den längsten Pfad nach geschätzter Dauer (Timeline, 18.6 B).
func (g *Graph) CriticalPath() []string {
	dur := func(n *Node) int { return max(n.Contract.Budget.WallClockS, 60) }
	order := g.topo()
	best := map[string]int{}
	prev := map[string]string{}
	for _, id := range order {
		n := g.Node(id)
		best[id] = dur(n)
		for _, d := range g.Deps(id) {
			if v := best[d] + dur(n); v > best[id] {
				best[id], prev[id] = v, d
			}
		}
	}
	end, bestV := "", -1
	for id, v := range best {
		if v > bestV || (v == bestV && id < end) {
			end, bestV = id, v
		}
	}
	var path []string
	for end != "" {
		path = append([]string{end}, path...)
		end = prev[end]
	}
	return path
}

func (g *Graph) topo() []string {
	in := map[string]int{}
	for _, n := range g.Nodes {
		in[n.ID] = 0
	}
	for _, e := range g.Edges {
		in[e.To]++
	}
	var q, out []string
	for _, n := range g.Nodes {
		if in[n.ID] == 0 {
			q = append(q, n.ID)
		}
	}
	for len(q) > 0 {
		sort.Strings(q)
		n := q[0]
		q = q[1:]
		out = append(out, n)
		for _, e := range g.Edges {
			if e.From == n {
				in[e.To]--
				if in[e.To] == 0 {
					q = append(q, e.To)
				}
			}
		}
	}
	return out
}

// Estimate liefert eine Kosten-/Zeitvorschau (17.10: "Plan: 14 Knoten, ~€3–6, ~25 min").
type Estimate struct {
	Nodes          int      `json:"nodes"`
	CostMinEUR     float64  `json:"cost_min_eur"`
	CostMaxEUR     float64  `json:"cost_max_eur"`
	Minutes        int      `json:"minutes"`
	NeedsApprovals []string `json:"needs_approvals"`
	SelfNodes      int      `json:"self_nodes"`
	DelegatedNodes int      `json:"delegated_nodes"`
}

// EstimatePlan schätzt Kosten (µ€ pro Token) und Dauer über den kritischen Pfad.
func (g *Graph) EstimatePlan(microEURPerToken float64, riskyTools []string) Estimate {
	e := Estimate{Nodes: len(g.Nodes)}
	var tokens int64
	for _, n := range g.Nodes {
		t := n.Contract.Budget.Tokens
		if t == 0 {
			t = 50_000
		}
		tokens += t
		if n.OwnerKind == OwnerLead {
			e.SelfNodes++
		} else if n.OwnerKind != OwnerHuman {
			e.DelegatedNodes++
		}
		for _, s := range n.Contract.ToolScope {
			for _, r := range riskyTools {
				if s == r {
					e.NeedsApprovals = append(e.NeedsApprovals, n.Title+": "+s)
				}
			}
		}
		if n.OwnerKind == OwnerHuman {
			e.NeedsApprovals = append(e.NeedsApprovals, n.Title+": menschliche Entscheidung")
		}
	}
	e.CostMaxEUR = float64(tokens) * microEURPerToken / 1e6
	e.CostMinEUR = e.CostMaxEUR * 0.4
	secs := 0
	for _, id := range g.CriticalPath() {
		secs += max(g.Node(id).Contract.Budget.WallClockS, 60)
	}
	e.Minutes = (secs + 59) / 60
	return e
}
