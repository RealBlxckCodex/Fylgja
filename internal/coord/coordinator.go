package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/tools"
)

// Executor startet die Arbeit an einem Knoten (Self-Lane, Teammate oder Worker).
type Executor interface {
	Start(ctx context.Context, g *Graph, n *Node) (runID string, err error)
	Cancel(ctx context.Context, n *Node) error
}

// Verifier prüft Ergebnisse gegen Akzeptanzkriterien (Reviewer-Run, anderes Modell als der Ausführende).
type Verifier interface {
	Verify(ctx context.Context, n *Node, result json.RawMessage) (pass bool, feedback string, err error)
}

// Capacity meldet, ob Sandbox-Hosts und GPU-Queue einen weiteren Start erlauben (17.8).
type Capacity interface {
	Available(ctx context.Context, n *Node) (bool, string)
}

// Limits der Koordination.
type Limits struct {
	WorkersPerLead      int
	WorkersPerWorkspace int
	ParallelPerGraph    int
	MaxAttempts         int           // 1 + Retries (Default 3 = 2 Retries)
	StallTimeout        time.Duration // fehlender Fortschritt → wie failed
}

func DefaultLimits() Limits {
	return Limits{WorkersPerLead: 8, WorkersPerWorkspace: 24, ParallelPerGraph: 6, MaxAttempts: 3, StallTimeout: 30 * time.Minute}
}

// Coordinator ist der deterministische Koordinator (kein LLM).
type Coordinator struct {
	Store     *Store
	Exec      Executor
	Verify    Verifier
	Capacity  Capacity
	Limits    Limits
	Workspace func(ctx context.Context, leadDot string) (uuid.UUID, error)
	// Escalate weckt den Lead ereignisgetrieben (Report, Blocker, Verifikationsfehler) – kein Polling.
	Escalate func(ctx context.Context, g *Graph, n *Node, kind string)
	Publish  func(ctx context.Context, topic string, payload any)
	Log      *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex // pro Graph serialisiert
}

func (c *Coordinator) graphLock(id string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	if c.locks[id] == nil {
		c.locks[id] = &sync.Mutex{}
	}
	return c.locks[id]
}

func (c *Coordinator) publish(ctx context.Context, g *Graph, typ string, n *Node) {
	if c.Publish != nil {
		c.Publish(ctx, "graph."+g.ID, map[string]any{"type": typ, "node": n, "graph_status": g.Status})
	}
}

func (c *Coordinator) escalate(ctx context.Context, g *Graph, n *Node, kind string) {
	if c.Escalate != nil {
		c.Escalate(ctx, g, n, kind)
	}
}

func (c *Coordinator) limits() Limits {
	l := c.Limits
	d := DefaultLimits()
	if l.WorkersPerLead == 0 {
		l.WorkersPerLead = d.WorkersPerLead
	}
	if l.WorkersPerWorkspace == 0 {
		l.WorkersPerWorkspace = d.WorkersPerWorkspace
	}
	if l.ParallelPerGraph == 0 {
		l.ParallelPerGraph = d.ParallelPerGraph
	}
	if l.MaxAttempts == 0 {
		l.MaxAttempts = d.MaxAttempts
	}
	if l.StallTimeout == 0 {
		l.StallTimeout = d.StallTimeout
	}
	return l
}

// Plan legt einen Graphen an und prüft Verträge und Budget-Reservierung.
func (c *Coordinator) Plan(ctx context.Context, g *Graph, leadScope []string) error {
	if err := g.Validate(); err != nil {
		return err
	}
	remaining := g.Budget
	for _, n := range g.Nodes {
		if n.OwnerKind == OwnerHuman {
			continue
		}
		if err := n.Contract.ValidateAgainst(leadScope, "", remaining); err != nil {
			return fmt.Errorf("knoten %q: %w", n.Title, err)
		}
		// Budget wird reserviert, nicht nur begrenzt (17.3).
		if remaining.Tokens > 0 {
			remaining.Tokens -= n.Contract.Budget.Tokens
		}
		if remaining.CostMicroEUR > 0 {
			remaining.CostMicroEUR -= n.Contract.Budget.CostMicroEUR
		}
		if n.Goal == "" {
			n.Goal = n.Contract.Goal
		}
	}
	return c.Store.CreateGraph(ctx, g)
}

// Remaining liefert das noch nicht reservierte Graph-Budget.
func Remaining(g *Graph) Budget {
	r := g.Budget
	for _, n := range g.Nodes {
		if n.Status == CancelledN {
			continue
		}
		r.Tokens -= n.Contract.Budget.Tokens
		r.CostMicroEUR -= n.Contract.Budget.CostMicroEUR
	}
	return r
}

// Start setzt einen geplanten Graphen in Gang.
func (c *Coordinator) Start(ctx context.Context, graphID string) error {
	if err := c.Store.SetGraphStatus(ctx, graphID, "running"); err != nil {
		return err
	}
	return c.Tick(ctx, graphID)
}

// Tick treibt den Graphen voran: promote → dispatch → Stall-Erkennung → Abschluss.
func (c *Coordinator) Tick(ctx context.Context, graphID string) error {
	lk := c.graphLock(graphID)
	lk.Lock()
	defer lk.Unlock()
	g, err := c.Store.LoadGraph(ctx, graphID)
	if err != nil {
		return err
	}
	if g.Status != "running" {
		return nil
	}
	lim := c.limits()
	now := time.Now()
	// Stall-Erkennung.
	for _, n := range g.Nodes {
		if n.Status == Running && now.Sub(n.UpdatedAt) > max(lim.StallTimeout, 2*time.Duration(n.Contract.Budget.WallClockS)*time.Second) {
			n.Reason = "stalled: kein fortschritt"
			if err := c.failOrRetry(ctx, g, n, "stalled"); err != nil {
				return err
			}
		}
	}
	for _, n := range g.Promotable() {
		if err := c.Store.SetStatus(ctx, n, Ready, func(n *Node) { n.Reason = "" }); err != nil {
			return err
		}
		c.publish(ctx, g, "node.ready", n)
	}
	running := 0
	for _, n := range g.Nodes {
		if n.Status == Running || n.Status == NeedsReview || n.Status == Blocked {
			running++
		}
	}
	ready := []*Node{}
	for _, n := range g.Nodes {
		if n.Status == Ready {
			ready = append(ready, n)
		}
	}
	sort.SliceStable(ready, func(i, j int) bool { return prioOf(ready[i]) > prioOf(ready[j]) })
	for _, n := range ready {
		if running >= lim.ParallelPerGraph {
			_ = c.Store.SetReason(ctx, n, "wartet auf kapazität (graph-limit)")
			continue
		}
		if n.OwnerKind == OwnerHuman {
			_ = c.Store.SetReason(ctx, n, "wartet auf menschliche entscheidung")
			continue
		}
		if n.OwnerKind == OwnerWorker && c.Workspace != nil {
			ws, err := c.Workspace(ctx, g.LeadDotID)
			if err == nil {
				perLead, perWS, err := c.Store.RunningWorkers(ctx, g.LeadDotID, ws)
				if err == nil && (perLead >= lim.WorkersPerLead || perWS >= lim.WorkersPerWorkspace) {
					_ = c.Store.SetReason(ctx, n, fmt.Sprintf("wartet auf kapazität (worker %d/%d lead, %d/%d workspace)", perLead, lim.WorkersPerLead, perWS, lim.WorkersPerWorkspace))
					continue
				}
			}
		}
		if c.Capacity != nil {
			if ok, why := c.Capacity.Available(ctx, n); !ok {
				_ = c.Store.SetReason(ctx, n, "wartet auf kapazität: "+why)
				continue
			}
		}
		if err := c.Store.SetStatus(ctx, n, Running, func(n *Node) { n.Attempt++; n.Reason = "" }); err != nil {
			return err
		}
		runID, err := c.Exec.Start(ctx, g, n)
		if err != nil {
			n.Reason = "start fehlgeschlagen: " + err.Error()
			if err := c.failOrRetry(ctx, g, n, "start_failed"); err != nil {
				return err
			}
			continue
		}
		if runID != "" {
			n.RunID = runID
			_, _ = c.Store.Pool.Exec(ctx, `UPDATE work_nodes SET run_id=$2 WHERE id=$1`, n.ID, runID)
		}
		running++
		c.publish(ctx, g, "node.running", n)
	}
	return c.finishIfDone(ctx, g)
}

func prioOf(n *Node) int {
	switch n.Contract.Priority {
	case "interactive":
		return 3
	case "review":
		return 2
	case "background":
		return 0
	}
	return 1
}

func (c *Coordinator) finishIfDone(ctx context.Context, g *Graph) error {
	allDone, anyOpen := true, false
	for _, n := range g.Nodes {
		if n.Status != Done && n.Status != CancelledN {
			allDone = false
		}
		if !n.Status.Terminal() {
			anyOpen = true
		}
	}
	if allDone && len(g.Nodes) > 0 {
		g.Status = "done"
		if err := c.Store.SetGraphStatus(ctx, g.ID, "done"); err != nil {
			return err
		}
		c.publish(ctx, g, "graph.done", nil)
		c.escalate(ctx, g, nil, "graph_done")
	} else if !anyOpen {
		// Nur noch failed/cancelled-Knoten: Lead muss entscheiden.
		c.escalate(ctx, g, nil, "graph_stuck")
	}
	return nil
}

// Complete meldet ein Ergebnis. Verifikation: Schema (hart), dann Reviewer.
func (c *Coordinator) Complete(ctx context.Context, graphID, nodeID string, result json.RawMessage, notes string) error {
	lk := c.graphLock(graphID)
	lk.Lock()
	g, err := c.Store.LoadGraph(ctx, graphID)
	if err != nil {
		lk.Unlock()
		return err
	}
	n := g.Node(nodeID)
	if n == nil {
		lk.Unlock()
		return fmt.Errorf("coord: knoten %s unbekannt", nodeID)
	}
	if err := c.Store.SetStatus(ctx, n, NeedsReview, func(n *Node) {
		n.Result = result
		// Freitext ist immer untrusted und begrenzt (17.3).
		if len(notes) > 2000 {
			notes = notes[:2000]
		}
		n.Notes = notes
	}); err != nil {
		lk.Unlock()
		return err
	}
	c.publish(ctx, g, "node.needs_review", n)
	if _, err := tools.ValidateArgs(n.Contract.OutputSchema, result); err != nil {
		n.Reason = "schema verletzt: " + err.Error()
		err := c.failOrRetry(ctx, g, n, "verification_failed")
		lk.Unlock()
		if err != nil {
			return err
		}
		return c.Tick(ctx, graphID)
	}
	pass, feedback := true, ""
	if c.Verify != nil && len(n.Contract.Acceptance) > 0 {
		var verr error
		pass, feedback, verr = c.Verify.Verify(ctx, n, result)
		if verr != nil {
			pass, feedback = false, "verifikation fehlgeschlagen (fail-closed): "+verr.Error()
		}
	}
	if pass {
		if err := c.Store.SetStatus(ctx, n, Done, func(n *Node) { n.Reason = "" }); err != nil {
			lk.Unlock()
			return err
		}
		c.publish(ctx, g, "node.done", n)
		c.escalate(ctx, g, n, "report")
	} else {
		n.Reason = "review: " + feedback
		if err := c.failOrRetry(ctx, g, n, "verification_failed"); err != nil {
			lk.Unlock()
			return err
		}
	}
	lk.Unlock()
	return c.Tick(ctx, graphID)
}

// Fail meldet einen fehlgeschlagenen Knoten.
func (c *Coordinator) Fail(ctx context.Context, graphID, nodeID, reason string) error {
	lk := c.graphLock(graphID)
	lk.Lock()
	g, err := c.Store.LoadGraph(ctx, graphID)
	if err != nil {
		lk.Unlock()
		return err
	}
	n := g.Node(nodeID)
	if n == nil {
		lk.Unlock()
		return fmt.Errorf("coord: knoten %s unbekannt", nodeID)
	}
	n.Reason = reason
	err = c.failOrRetry(ctx, g, n, "failed")
	lk.Unlock()
	if err != nil {
		return err
	}
	return c.Tick(ctx, graphID)
}

// failOrRetry: max. 2 Wiederholungen mit geändertem Kontext, danach Eskalation an den Lead (17.6).
func (c *Coordinator) failOrRetry(ctx context.Context, g *Graph, n *Node, kind string) error {
	if n.Attempt < c.limits().MaxAttempts {
		reason := n.Reason
		if err := c.Store.SetStatus(ctx, n, Ready, func(n *Node) {
			// Geänderter Kontext: Fehlerbeschreibung fließt in den nächsten Versuch.
			n.Contract.Acceptance = appendUnique(n.Contract.Acceptance, "Vorheriger Versuch scheiterte: "+reason)
			n.Reason = "retry nach: " + reason
		}); err != nil {
			return err
		}
		c.publish(ctx, g, "node.retry", n)
		return nil
	}
	if n.Status != FailedN {
		if err := c.Store.SetStatus(ctx, n, FailedN, nil); err != nil {
			return err
		}
	}
	c.publish(ctx, g, "node.failed", n)
	c.escalate(ctx, g, n, kind)
	return nil
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// Block/Unblock (z. B. wartet auf Lock, Credential, Frage).
func (c *Coordinator) Block(ctx context.Context, graphID, nodeID, reason string) error {
	g, err := c.Store.LoadGraph(ctx, graphID)
	if err != nil {
		return err
	}
	n := g.Node(nodeID)
	if n == nil {
		return errors.New("coord: knoten unbekannt")
	}
	if err := c.Store.SetStatus(ctx, n, Blocked, func(n *Node) { n.Reason = reason }); err != nil {
		return err
	}
	c.publish(ctx, g, "node.blocked", n)
	c.escalate(ctx, g, n, "blocked")
	return nil
}

// Reassign: Lead/Nutzer weist einen fehlgeschlagenen/blockierten Knoten neu zu (anderer Agent/Tier/selbst).
func (c *Coordinator) Reassign(ctx context.Context, graphID, nodeID string, kind OwnerKind, dot, tier string) error {
	lk := c.graphLock(graphID)
	lk.Lock()
	g, err := c.Store.LoadGraph(ctx, graphID)
	if err != nil {
		lk.Unlock()
		return err
	}
	n := g.Node(nodeID)
	if n == nil {
		lk.Unlock()
		return errors.New("coord: knoten unbekannt")
	}
	if n.Status == Running {
		_ = c.Exec.Cancel(ctx, n)
	}
	err = c.Store.SetStatus(ctx, n, Ready, func(n *Node) {
		n.OwnerKind, n.OwnerDotID, n.Attempt = kind, dot, 0
		if tier != "" {
			n.Contract.Tier = tier
		}
		n.Reason = "neu zugewiesen"
	})
	lk.Unlock()
	if err != nil {
		return err
	}
	return c.Tick(ctx, graphID)
}

// Cancel bricht einen Knoten (und top-down alle Nachfolger) bzw. den ganzen Graphen ab.
func (c *Coordinator) Cancel(ctx context.Context, graphID, nodeID string) error {
	lk := c.graphLock(graphID)
	lk.Lock()
	defer lk.Unlock()
	g, err := c.Store.LoadGraph(ctx, graphID)
	if err != nil {
		return err
	}
	var targets []string
	if nodeID == "" {
		for _, n := range g.Nodes {
			targets = append(targets, n.ID)
		}
	} else {
		targets = append([]string{nodeID}, g.Descendants(nodeID)...)
	}
	for _, id := range targets {
		n := g.Node(id)
		if n == nil || n.Status.Terminal() {
			continue
		}
		if n.Status == Running || n.Status == Blocked || n.Status == NeedsReview {
			_ = c.Exec.Cancel(ctx, n)
		}
		if err := c.Store.SetStatus(ctx, n, CancelledN, func(n *Node) { n.Reason = "abgebrochen" }); err != nil {
			return err
		}
		c.publish(ctx, g, "node.cancelled", n)
	}
	if nodeID == "" {
		g.Status = "cancelled"
		return c.Store.SetGraphStatus(ctx, graphID, "cancelled")
	}
	return nil
}
