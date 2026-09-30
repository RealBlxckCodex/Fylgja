package runtime

import (
	"context"
	"sync"
)

// Lanes implementiert die Lane Queue (8.1): pro Lane strikt seriell, zusätzlich
// eine Semaphore pro Fylgja (max. parallele Runs).
type Lanes struct {
	PerDot int

	mu    sync.Mutex
	lanes map[string]*lane
	sems  map[string]chan struct{}
	wg    sync.WaitGroup
}

type lane struct {
	queue   []func(context.Context)
	running bool
}

func NewLanes(perDot int) *Lanes {
	if perDot <= 0 {
		perDot = 4
	}
	return &Lanes{PerDot: perDot, lanes: map[string]*lane{}, sems: map[string]chan struct{}{}}
}

// Enqueue reiht fn in die Lane ein. Läufe einer Lane laufen nie parallel.
func (l *Lanes) Enqueue(ctx context.Context, key, dot string, fn func(context.Context)) {
	l.mu.Lock()
	ln := l.lanes[key]
	if ln == nil {
		ln = &lane{}
		l.lanes[key] = ln
	}
	ln.queue = append(ln.queue, fn)
	if ln.running {
		l.mu.Unlock()
		return
	}
	ln.running = true
	sem := l.sems[dot]
	if sem == nil {
		sem = make(chan struct{}, l.PerDot)
		l.sems[dot] = sem
	}
	l.wg.Add(1)
	l.mu.Unlock()
	go l.drain(ctx, key, ln, sem)
}

func (l *Lanes) drain(ctx context.Context, key string, ln *lane, sem chan struct{}) {
	defer l.wg.Done()
	for {
		l.mu.Lock()
		if len(ln.queue) == 0 {
			ln.running = false
			delete(l.lanes, key)
			l.mu.Unlock()
			return
		}
		fn := ln.queue[0]
		ln.queue = ln.queue[1:]
		l.mu.Unlock()
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		func() {
			defer func() { <-sem }()
			fn(ctx)
		}()
	}
}

// Depth liefert die Anzahl wartender Einträge einer Lane.
func (l *Lanes) Depth(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ln := l.lanes[key]; ln != nil {
		return len(ln.queue)
	}
	return 0
}

// Wait wartet, bis alle Lanes leer sind (Tests, Shutdown).
func (l *Lanes) Wait() { l.wg.Wait() }
