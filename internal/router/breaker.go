package router

import "time"

// breaker ist ein einfacher Circuit Breaker mit Half-Open-Probe.
type breaker struct {
	failures  int
	openUntil time.Time
	backoff   time.Duration
	probing   bool
}

const (
	breakerThreshold = 3
	breakerMin       = 5 * time.Second
	breakerMax       = 5 * time.Minute
)

func (b *breaker) state(now time.Time) string {
	switch {
	case b.openUntil.IsZero():
		return "closed"
	case now.Before(b.openUntil):
		return "open"
	default:
		return "half_open"
	}
}

// allow meldet, ob Anfragen durchgelassen werden (half-open: genau eine Probe).
func (b *breaker) allow(now time.Time) bool {
	switch b.state(now) {
	case "closed":
		return true
	case "open":
		return false
	default:
		return !b.probing
	}
}

func (b *breaker) onStart(now time.Time) {
	if b.state(now) == "half_open" {
		b.probing = true
	}
}

func (b *breaker) success() {
	b.failures, b.openUntil, b.backoff, b.probing = 0, time.Time{}, 0, false
}

func (b *breaker) failure(now time.Time) {
	b.failures++
	wasProbe := b.probing
	b.probing = false
	if b.failures >= breakerThreshold || wasProbe {
		if b.backoff == 0 {
			b.backoff = breakerMin
		} else {
			b.backoff = min(b.backoff*2, breakerMax)
		}
		b.openUntil = now.Add(b.backoff)
	}
}
