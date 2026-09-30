package router

import (
	"sort"

	"github.com/realblxckcodex/fylgja/internal/llm"
)

type ringbuf struct {
	v []float64
	i int
	n int
}

func newRing(n int) *ringbuf { return &ringbuf{v: make([]float64, n)} }

func (r *ringbuf) add(x float64) {
	r.v[r.i] = x
	r.i = (r.i + 1) % len(r.v)
	if r.n < len(r.v) {
		r.n++
	}
}

func (r *ringbuf) pct(p float64) float64 {
	if r.n == 0 {
		return 0
	}
	c := append([]float64(nil), r.v[:r.n]...)
	sort.Float64s(c)
	return c[min(int(float64(r.n-1)*p+0.5), r.n-1)]
}

// DeploymentView ist die UI-Sicht auf ein Deployment (18.5 C).
type DeploymentView struct {
	Deployment
	Inflight   int     `json:"inflight"`
	NodeQueued int     `json:"node_queued"`
	LatencyP95 float64 `json:"latency_p95_ms"`
	KVUtil     float64 `json:"kv_cache_util"`
	Breaker    string  `json:"breaker"`
	Served     int64   `json:"served"`
	Errors     int64   `json:"errors"`
	ByDot      map[string]int `json:"by_dot"`
}

// QueueView: Warteschlange je Prioritätsklasse.
type QueueView struct {
	Class  llm.Priority `json:"class"`
	Depth  int          `json:"depth"`
	WaitP50 float64     `json:"wait_p50_ms"`
	WaitP95 float64     `json:"wait_p95_ms"`
}

// Snapshot liefert den aktuellen Zustand für UI und Metriken.
type Snapshot struct {
	Models      []Model          `json:"models"`
	Deployments []DeploymentView `json:"deployments"`
	Queues      []QueueView      `json:"queues"`
}

func (r *Router) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	var s Snapshot
	for _, m := range r.models {
		s.Models = append(s.Models, m)
	}
	sort.Slice(s.Models, func(i, j int) bool { return s.Models[i].Name < s.Models[j].Name })
	for _, d := range r.deps {
		st := r.stats[d.Name]
		by := map[string]int{}
		for k, v := range st.byDot {
			by[k] = v
		}
		s.Deployments = append(s.Deployments, DeploymentView{Deployment: *d, Inflight: st.inflight, NodeQueued: st.nodeQueued,
			LatencyP95: st.latP95, KVUtil: st.kvUtil, Breaker: st.breaker.state(now), Served: st.served, Errors: st.errors, ByDot: by})
	}
	sort.Slice(s.Deployments, func(i, j int) bool { return s.Deployments[i].Name < s.Deployments[j].Name })
	depth := map[llm.Priority]int{}
	for w := range r.waiters {
		depth[w.prio]++
	}
	for _, p := range []llm.Priority{llm.Interactive, llm.ReviewPrio, llm.TaskPrio, llm.Background} {
		h := r.waitHist[p]
		s.Queues = append(s.Queues, QueueView{Class: p, Depth: depth[p], WaitP50: h.pct(0.5), WaitP95: h.pct(0.95)})
	}
	return s
}

// QueuePressure liefert p95-Wartezeit (ms) und KV-Auslastung eines Modells für das Autoscaling.
func (r *Router) QueuePressure(model string) (waitP95 float64, kvMax float64, waiting int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for w := range r.waiters {
		if w.model == model {
			waiting++
		}
	}
	for _, d := range r.deps {
		if d.Model == model && d.State == Ready {
			kvMax = max(kvMax, r.stats[d.Name].kvUtil)
		}
	}
	return r.waitHist[llm.TaskPrio].pct(0.95), kvMax, waiting
}
