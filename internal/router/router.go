package router

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/platform/clock"
)

// Weights des Scores je Prioritätsklasse (16.4).
type Weights struct {
	Queue, Latency, KV, Affinity, Cost float64
}

// Config des Routers.
type Config struct {
	Weights map[llm.Priority]Weights
	// MaxWait je Klasse, danach Fallback-Kette.
	MaxWait map[llm.Priority]time.Duration
	// InteractiveReserve: Anteil der Slots, der für interactive reserviert ist (Default 0.3).
	InteractiveReserve float64
	// MaxShare: max. Anteil der Slots eines Modells, den eine Fylgja belegen darf, solange andere warten.
	MaxShare float64
	// SampleRate für routing_decisions (Fehler werden immer protokolliert).
	SampleRate float64
}

func DefaultConfig() Config {
	return Config{
		Weights: map[llm.Priority]Weights{
			llm.Interactive: {Queue: 1, Latency: 2, KV: 1, Affinity: 1.5, Cost: 0.2},
			llm.ReviewPrio:  {Queue: 1, Latency: 2, KV: 1, Affinity: 1, Cost: 0.2},
			llm.TaskPrio:    {Queue: 1, Latency: 1, KV: 1, Affinity: 1.5, Cost: 0.5},
			llm.Background:  {Queue: 1, Latency: 0.2, KV: 0.5, Affinity: 1, Cost: 2},
		},
		MaxWait: map[llm.Priority]time.Duration{
			llm.Interactive: 20 * time.Second,
			llm.ReviewPrio:  15 * time.Second,
			llm.TaskPrio:    2 * time.Minute,
			llm.Background:  10 * time.Minute,
		},
		InteractiveReserve: 0.3,
		MaxShare:           0.6,
		SampleRate:         0.1,
	}
}

// Decision wird für routing_decisions protokolliert.
type Decision struct {
	RunID        string `json:"run_id"`
	Tier         string `json:"tier"`
	LogicalModel string `json:"logical_model"`
	Deployment   string `json:"deployment"`
	DeploymentID string `json:"deployment_id"`
	Reason       string `json:"reason"`
	QueueWaitMS  int    `json:"queue_wait_ms"`
	Error        bool   `json:"error"`
}

// Recorder speichert Entscheidungen und Nutzung.
type Recorder interface {
	RecordDecision(ctx context.Context, d Decision)
}

type depStats struct {
	inflight   int
	byDot      map[string]int
	latEWMA    float64 // ms
	latP95     float64 // ms (EWMA des Maximums)
	kvUtil     float64 // 0..1, vom Node gemeldet
	nodeQueued int
	breaker    breaker
	served     int64
	errors     int64
	prefixes   map[string]time.Time // Prefix-Hashes der letzten Minuten
}

type waiter struct {
	prio  llm.Priority
	model string
	dot   string
	seq   uint64
	since time.Time
}

// Router implementiert llm.Client.
type Router struct {
	cfg   Config
	clock clock.Clock
	rec   Recorder

	mu      sync.Mutex
	models  map[string]Model
	deps    map[string]*Deployment
	stats   map[string]*depStats
	waiters map[*waiter]struct{}
	seq     uint64
	wake    chan struct{}
	// Wartezeiten je Klasse (für UI/Metriken)
	waitHist map[llm.Priority]*ringbuf
}

func New(cfg Config, c clock.Clock, rec Recorder) *Router {
	if c == nil {
		c = clock.Real
	}
	r := &Router{cfg: cfg, clock: c, rec: rec, models: map[string]Model{}, deps: map[string]*Deployment{},
		stats: map[string]*depStats{}, waiters: map[*waiter]struct{}{}, wake: make(chan struct{}), waitHist: map[llm.Priority]*ringbuf{}}
	for _, p := range []llm.Priority{llm.Interactive, llm.ReviewPrio, llm.TaskPrio, llm.Background} {
		r.waitHist[p] = newRing(256)
	}
	return r
}

// SetModel legt ein logisches Modell an oder aktualisiert es.
func (r *Router) SetModel(m Model) {
	r.mu.Lock()
	r.models[m.Name] = m
	r.mu.Unlock()
}

// Upsert fügt ein Deployment hinzu bzw. aktualisiert es.
func (r *Router) Upsert(d *Deployment) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.MaxConcurrency <= 0 {
		d.MaxConcurrency = 8
	}
	if d.Weight <= 0 {
		d.Weight = 1
	}
	r.deps[d.Name] = d
	if r.stats[d.Name] == nil {
		r.stats[d.Name] = &depStats{byDot: map[string]int{}, prefixes: map[string]time.Time{}}
	}
	r.broadcastLocked()
}

// SetState ändert den Zustand eines Deployments (z. B. draining).
func (r *Router) SetState(name string, s DeploymentState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.deps[name]
	if !ok {
		return fmt.Errorf("router: deployment %q unbekannt", name)
	}
	d.State = s
	r.broadcastLocked()
	return nil
}

// Remove entfernt ein Deployment.
func (r *Router) Remove(name string) {
	r.mu.Lock()
	delete(r.deps, name)
	r.broadcastLocked()
	r.mu.Unlock()
}

// ReportNodeMetrics übernimmt vLLM-Metriken eines Nodes (KV-Cache, Queue).
func (r *Router) ReportNodeMetrics(deployment string, kvUtil float64, queued int) {
	r.mu.Lock()
	if s := r.stats[deployment]; s != nil {
		s.kvUtil, s.nodeQueued = kvUtil, queued
	}
	r.mu.Unlock()
}

func (r *Router) broadcastLocked() {
	close(r.wake)
	r.wake = make(chan struct{})
}

func prioRank(p llm.Priority) int {
	switch p {
	case llm.Interactive:
		return 3
	case llm.ReviewPrio:
		return 2
	case llm.TaskPrio:
		return 1
	}
	return 0
}

// capacityFor: Slots, die eine Klasse auf einem Deployment nutzen darf.
func (r *Router) capacityFor(d *Deployment, p llm.Priority) int {
	if p == llm.Interactive || p == llm.ReviewPrio {
		return d.MaxConcurrency
	}
	reserved := int(math.Ceil(float64(d.MaxConcurrency) * r.cfg.InteractiveReserve))
	return max(d.MaxConcurrency-reserved, 1)
}

var (
	ErrNoCandidates = errors.New("router: kein deployment erfüllt die anforderungen")
	ErrPrivacy      = errors.New("router: privacy-anforderung verhindert fallback")
	ErrWaitTimeout  = errors.New("router: wartezeit überschritten")
	ErrUnhealthy    = errors.New("router: alle passenden deployments ausgefallen (circuit breaker offen)")
)

type candidate struct {
	d     *Deployment
	s     *depStats
	score float64
}

// candidatesLocked liefert alle Deployments des Modells, die Privacy und Fähigkeiten erfüllen.
// Rückgabe: (verfügbar mit Kapazität, grundsätzlich passend).
func (r *Router) candidatesLocked(model string, req llm.Request, now time.Time, exclude map[string]bool) (avail []candidate, eligible, healthy, privacyBlocked int) {
	m := r.models[model]
	w := r.cfg.Weights[req.Meta.Priority]
	if (w == Weights{}) {
		w = r.cfg.Weights[llm.TaskPrio]
	}
	needTools := len(req.Tools) > 0
	var maxCost float64
	for _, d := range r.deps {
		if d.Model == model {
			maxCost = math.Max(maxCost, float64(d.PriceOutPerMTok+d.CostPerHour/100))
		}
	}
	for _, d := range r.deps {
		if d.Model != model || d.State != Ready {
			continue
		}
		if !d.SatisfiesPrivacy(req.Meta.Privacy) {
			privacyBlocked++
			continue
		}
		if needTools && len(m.Capabilities) > 0 && !m.Has("tools") {
			continue
		}
		s := r.stats[d.Name]
		eligible++
		if !s.breaker.allow(now) || exclude[d.Name] {
			continue
		}
		healthy++
		if s.inflight >= r.capacityFor(d, req.Meta.Priority) {
			continue
		}
		queue := float64(s.inflight+s.nodeQueued) / float64(d.MaxConcurrency)
		lat := math.Min(s.latP95/30000, 1)
		aff := 0.0
		if req.Meta.PrefixHash != "" {
			if t, ok := s.prefixes[req.Meta.PrefixHash]; ok && now.Sub(t) < 10*time.Minute {
				aff = 1
			}
		}
		cost := 0.0
		if maxCost > 0 {
			cost = float64(d.PriceOutPerMTok+d.CostPerHour/100) / maxCost
		}
		score := w.Queue*queue + w.Latency*lat + w.KV*s.kvUtil - w.Affinity*aff + w.Cost*cost
		avail = append(avail, candidate{d: d, s: s, score: score})
	}
	sort.Slice(avail, func(i, j int) bool {
		if math.Abs(avail[i].score-avail[j].score) > 1e-9 {
			return avail[i].score < avail[j].score
		}
		return avail[i].d.Name < avail[j].d.Name
	})
	return avail, eligible, healthy, privacyBlocked
}

// pick wählt bei Gleichstand gewichtet zufällig.
func pick(c []candidate) candidate {
	best := c[0].score
	var ties []candidate
	var total float64
	for _, x := range c {
		if math.Abs(x.score-best) < 1e-9 {
			ties = append(ties, x)
			total += x.d.Weight
		}
	}
	if len(ties) == 1 {
		return ties[0]
	}
	v := rand.Float64() * total
	for _, x := range ties {
		v -= x.d.Weight
		if v <= 0 {
			return x
		}
	}
	return ties[len(ties)-1]
}

// higherWaiterLocked meldet, ob ein höher priorisierter (oder älterer gleich priorisierter) Waiter wartet.
func (r *Router) higherWaiterLocked(me *waiter) bool {
	for w := range r.waiters {
		if w == me || w.model != me.model {
			continue
		}
		// Waiter, die ihren Fair-Share-Anteil ausgeschöpft haben, blockieren niemanden.
		if w.dot != me.dot && r.overShareLocked(w.model, w.dot, w) {
			continue
		}
		if prioRank(w.prio) > prioRank(me.prio) || (w.prio == me.prio && w.seq < me.seq) {
			return true
		}
	}
	return false
}

func (r *Router) overShareLocked(model, dot string, me *waiter) bool {
	if dot == "" || r.cfg.MaxShare <= 0 {
		return false
	}
	total, mine := 0, 0
	for _, d := range r.deps {
		if d.Model == model && d.State == Ready {
			total += d.MaxConcurrency
			mine += r.stats[d.Name].byDot[dot]
		}
	}
	others := false
	for w := range r.waiters {
		if w != me && w.model == model && w.dot != dot {
			others = true
		}
	}
	return others && float64(mine+1) > math.Ceil(float64(total)*r.cfg.MaxShare)
}

// acquire reserviert einen Slot auf dem besten Deployment oder wartet priorisiert.
func (r *Router) acquire(ctx context.Context, model string, req llm.Request, exclude map[string]bool) (*Deployment, time.Duration, error) {
	start := r.clock.Now()
	maxWait := r.cfg.MaxWait[req.Meta.Priority]
	if maxWait == 0 {
		maxWait = time.Minute
	}
	r.mu.Lock()
	r.seq++
	me := &waiter{prio: req.Meta.Priority, model: model, dot: req.Meta.DotID, seq: r.seq, since: start}
	registered := false
	defer func() {
		if registered {
			r.mu.Lock()
			delete(r.waiters, me)
			r.broadcastLocked()
			r.mu.Unlock()
		}
	}()
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	for {
		now := r.clock.Now()
		filtered, eligible, healthy, privBlocked := r.candidatesLocked(model, req, now, exclude)
		if eligible == 0 {
			r.mu.Unlock()
			if privBlocked > 0 {
				return nil, 0, ErrPrivacy
			}
			return nil, 0, ErrNoCandidates
		}
		if healthy == 0 {
			// Alle passenden Deployments sind ausgefallen: nicht warten, sondern Fallback-Kette.
			r.mu.Unlock()
			return nil, 0, ErrUnhealthy
		}
		if len(filtered) > 0 && !r.higherWaiterLocked(me) && !r.overShareLocked(model, me.dot, me) {
			c := pick(filtered)
			c.s.inflight++
			c.s.byDot[me.dot]++
			c.s.breaker.onStart(now)
			if req.Meta.PrefixHash != "" {
				c.s.prefixes[req.Meta.PrefixHash] = now
			}
			wait := now.Sub(start)
			r.waitHist[req.Meta.Priority].add(float64(wait.Milliseconds()))
			r.mu.Unlock()
			return c.d, wait, nil
		}
		if !registered {
			r.waiters[me] = struct{}{}
			registered = true
		}
		wake := r.wake
		r.mu.Unlock()
		select {
		case <-wake:
		case <-timer.C:
			r.waitHist[req.Meta.Priority].add(float64(maxWait.Milliseconds()))
			return nil, maxWait, ErrWaitTimeout
		case <-ctx.Done():
			return nil, r.clock.Now().Sub(start), ctx.Err()
		case <-time.After(time.Second): // Breaker-Übergänge (half-open) ohne Event
		}
		r.mu.Lock()
	}
}

func (r *Router) release(d *Deployment, dot string, latency time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats[d.Name]
	if s == nil {
		return
	}
	s.inflight--
	s.byDot[dot]--
	if s.byDot[dot] <= 0 {
		delete(s.byDot, dot)
	}
	now := r.clock.Now()
	if err != nil && !errors.Is(err, context.Canceled) {
		s.errors++
		s.breaker.failure(now)
	} else if err == nil {
		s.served++
		s.breaker.success()
		ms := float64(latency.Milliseconds())
		if s.latEWMA == 0 {
			s.latEWMA, s.latP95 = ms, ms
		} else {
			s.latEWMA = 0.8*s.latEWMA + 0.2*ms
			if ms > s.latP95 {
				s.latP95 = 0.7*s.latP95 + 0.3*ms
			} else {
				s.latP95 = 0.97*s.latP95 + 0.03*ms
			}
		}
	}
	for k, t := range s.prefixes {
		if now.Sub(t) > 10*time.Minute {
			delete(s.prefixes, k)
		}
	}
	r.broadcastLocked()
}

// Chat routet eine Anfrage. req.Model ist ein logisches Modell.
func (r *Router) Chat(ctx context.Context, req llm.Request, onDelta llm.DeltaFunc) (*llm.Response, error) {
	if req.Meta.Priority == "" {
		req.Meta.Priority = llm.TaskPrio
	}
	if req.Meta.Privacy == "" {
		req.Meta.Privacy = llm.SelfHostedOnly
	}
	r.mu.Lock()
	m, ok := r.models[req.Model]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("router: logisches modell %q unbekannt", req.Model)
	}
	chain := []string{m.Name}
	if !req.Meta.NoDegrade {
		chain = append(chain, m.Fallbacks...)
	}
	var lastErr error
	for i, logical := range chain {
		resp, err := r.tryModel(ctx, logical, req, onDelta)
		if err == nil {
			resp.Degraded = i > 0
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ae *llm.APIError
		if errors.As(err, &ae) && !ae.BeforeFirstToken {
			return nil, err // Nach dem ersten Token entscheidet der Run über Retry (16.4.7).
		}
	}
	return nil, lastErr
}

func (r *Router) tryModel(ctx context.Context, logical string, req llm.Request, onDelta llm.DeltaFunc) (*llm.Response, error) {
	exclude := map[string]bool{}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		d, wait, err := r.acquire(ctx, logical, req, exclude)
		if err != nil {
			r.record(ctx, Decision{RunID: req.Meta.RunID, Tier: req.Meta.Tier, LogicalModel: logical, Reason: err.Error(), QueueWaitMS: int(wait.Milliseconds()), Error: true})
			if lastErr != nil && (errors.Is(err, ErrWaitTimeout) || errors.Is(err, ErrUnhealthy)) {
				return nil, lastErr
			}
			return nil, err
		}
		sub := req
		sub.Model = d.ServedModel
		start := r.clock.Now()
		gotToken := false
		var wrapped llm.DeltaFunc
		if onDelta != nil {
			wrapped = func(s string) { gotToken = true; onDelta(s) }
		}
		resp, err := d.Client.Chat(ctx, sub, wrapped)
		lat := r.clock.Now().Sub(start)
		r.release(d, req.Meta.DotID, lat, err)
		if err == nil {
			resp.Deployment = d.Name
			resp.CostMicroEUR = cost(d, resp.Usage, lat)
			resp.Model = logical
			r.record(ctx, Decision{RunID: req.Meta.RunID, Tier: req.Meta.Tier, LogicalModel: logical, Deployment: d.Name, DeploymentID: d.ID, Reason: "score", QueueWaitMS: int(wait.Milliseconds())})
			return resp, nil
		}
		lastErr = err
		r.record(ctx, Decision{RunID: req.Meta.RunID, Tier: req.Meta.Tier, LogicalModel: logical, Deployment: d.Name, DeploymentID: d.ID, Reason: "fehler: " + err.Error(), Error: true})
		var ae *llm.APIError
		if gotToken || (errors.As(err, &ae) && !ae.BeforeFirstToken) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		exclude[d.Name] = true // transparent auf anderes Deployment umlenken
	}
	return nil, lastErr
}

func (r *Router) record(ctx context.Context, d Decision) {
	if r.rec == nil {
		return
	}
	if !d.Error && rand.Float64() > r.cfg.SampleRate {
		return
	}
	r.rec.RecordDecision(ctx, d)
}

// cost berechnet Kosten: extern nach Tokenpreis, GPU nach Stundenpreis × Laufzeit (Schattenpreis).
func cost(d *Deployment, u llm.Usage, lat time.Duration) int64 {
	if d.PriceInPerMTok > 0 || d.PriceOutPerMTok > 0 {
		return (int64(u.In-u.Cached)*d.PriceInPerMTok + int64(u.Out)*d.PriceOutPerMTok) / 1_000_000
	}
	if d.CostPerHour > 0 {
		// Anteilig: eine Anfrage belegt 1/MaxConcurrency der GPU für ihre Laufzeit.
		return int64(float64(d.CostPerHour) * lat.Hours() / float64(max(d.MaxConcurrency, 1)))
	}
	return 0
}
