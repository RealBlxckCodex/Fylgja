// Package clock abstrahiert die Uhrzeit, damit Zeitlogik deterministisch testbar ist.
package clock

import (
	"sync"
	"time"
)

type Clock interface{ Now() time.Time }

type real struct{}

func (real) Now() time.Time { return time.Now() }

// Real ist die Systemuhr.
var Real Clock = real{}

// Fake ist eine manuell gesteuerte Uhr.
type Fake struct {
	mu sync.Mutex
	t  time.Time
}

func NewFake(t time.Time) *Fake { return &Fake{t: t} }

func (f *Fake) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }

func (f *Fake) Advance(d time.Duration) { f.mu.Lock(); f.t = f.t.Add(d); f.mu.Unlock() }

func (f *Fake) Set(t time.Time) { f.mu.Lock(); f.t = t; f.mu.Unlock() }
