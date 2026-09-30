package router

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/realblxckcodex/fylgja/internal/llm"
)

// fakeNode simuliert ein Deployment mit steuerbarer Latenz/Fehlern.
type fakeNode struct {
	name     string
	delay    time.Duration
	fail     atomic.Bool
	calls    atomic.Int64
	inflight atomic.Int64
	maxSeen  atomic.Int64
	gate     chan struct{}
}

func (f *fakeNode) Chat(ctx context.Context, req llm.Request, _ llm.DeltaFunc) (*llm.Response, error) {
	f.calls.Add(1)
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxSeen.Load()
		if n <= m || f.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	time.Sleep(f.delay)
	if f.fail.Load() {
		return nil, &llm.APIError{Status: 503, Body: "node down", BeforeFirstToken: true}
	}
	return &llm.Response{Message: llm.Message{Role: llm.Assistant, Content: f.name}, Usage: llm.Usage{In: 1000, Out: 1000}}, nil
}

func setup(t *testing.T, deps ...*Deployment) *Router {
	r := New(DefaultConfig(), nil, nil)
	r.SetModel(Model{Name: "worker", PrivacyClass: "self_hosted", Capabilities: []string{"tools"}, Fallbacks: []string{"small"}})
	r.SetModel(Model{Name: "small", PrivacyClass: "self_hosted"})
	for _, d := range deps {
		if d.Model == "" {
			d.Model = "worker"
		}
		d.State = Ready
		r.Upsert(d)
	}
	return r
}

func TestPrivacyNeverViolated(t *testing.T) {
	ext := &fakeNode{name: "ext"}
	loc := &fakeNode{name: "loc"}
	loc.fail.Store(true)
	r := setup(t,
		&Deployment{Name: "local", Provider: "local", Client: loc, MaxConcurrency: 2},
		&Deployment{Name: "openai", Provider: "external", Region: "us", Client: ext, MaxConcurrency: 10},
	)
	for i := 0; i < 5; i++ {
		_, err := r.Chat(context.Background(), llm.Request{Model: "worker", Meta: llm.Meta{Privacy: llm.SelfHostedOnly, NoDegrade: true}}, nil)
		if err == nil {
			t.Fatal("antwort trotz ausgefallenem self-hosted node")
		}
	}
	if ext.calls.Load() != 0 {
		t.Fatal("PRIVACY-VERLETZUNG: externe api genutzt")
	}
	// Mit "any" darf extern genutzt werden.
	resp, err := r.Chat(context.Background(), llm.Request{Model: "worker", Meta: llm.Meta{Privacy: llm.AnyPrivacy}}, nil)
	if err != nil || resp.Deployment != "openai" {
		t.Fatalf("%v %+v", err, resp)
	}
	// Nur externe Deployments + self_hosted_only → klare Privacy-Meldung.
	r2 := setup(t, &Deployment{Name: "openai", Provider: "external", Client: ext})
	if _, err := r2.Chat(context.Background(), llm.Request{Model: "worker", Meta: llm.Meta{NoDegrade: true}}, nil); !errors.Is(err, ErrPrivacy) {
		t.Fatalf("want ErrPrivacy, got %v", err)
	}
}

func TestEUOnly(t *testing.T) {
	us := &Deployment{Name: "us", Provider: "runpod", Region: "US-TX-3"}
	eu := &Deployment{Name: "eu", Provider: "runpod", Region: "EU-RO-1"}
	ext := &Deployment{Name: "x", Provider: "external", Region: "eu-central-1"}
	if us.SatisfiesPrivacy(llm.EUOnly) || !eu.SatisfiesPrivacy(llm.EUOnly) || !ext.SatisfiesPrivacy(llm.EUOnly) || ext.SatisfiesPrivacy(llm.SelfHostedOnly) {
		t.Fatal("eu-prüfung falsch")
	}
	if Strictest(llm.AnyPrivacy, llm.EUOnly) != llm.EUOnly || Strictest(llm.EUOnly, llm.SelfHostedOnly, llm.AnyPrivacy) != llm.SelfHostedOnly {
		t.Fatal("strictest")
	}
}

func TestNodeKillUnderLoad(t *testing.T) {
	a := &fakeNode{name: "a", delay: 2 * time.Millisecond}
	b := &fakeNode{name: "b", delay: 2 * time.Millisecond}
	r := setup(t,
		&Deployment{Name: "pod-a", Provider: "runpod", Client: a, MaxConcurrency: 4},
		&Deployment{Name: "pod-b", Provider: "runpod", Client: b, MaxConcurrency: 4},
	)
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i := 0; i < 60; i++ {
		if i == 10 {
			a.fail.Store(true) // Node A stirbt mitten in der Last
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := r.Chat(ctx, llm.Request{Model: "worker", Meta: llm.Meta{Priority: llm.Interactive}}, nil); err != nil {
				failed.Add(1)
			}
		}()
		time.Sleep(time.Millisecond)
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d anfragen fehlgeschlagen trotz gesundem pod-b", failed.Load())
	}
	snap := r.Snapshot()
	for _, d := range snap.Deployments {
		if d.Name == "pod-a" && d.Breaker == "closed" {
			t.Fatal("breaker für pod-a nicht geöffnet")
		}
	}
	if a.maxSeen.Load() > 4 || b.maxSeen.Load() > 4 {
		t.Fatal("max_concurrency überschritten")
	}
}

func TestFallbackChainAndNoDegrade(t *testing.T) {
	big := &fakeNode{name: "big"}
	big.fail.Store(true)
	small := &fakeNode{name: "small"}
	r := setup(t,
		&Deployment{Name: "big", Provider: "runpod", Client: big},
		&Deployment{Name: "small-1", Model: "small", Provider: "local", Client: small},
	)
	resp, err := r.Chat(context.Background(), llm.Request{Model: "worker"}, nil)
	if err != nil || !resp.Degraded || resp.Deployment != "small-1" {
		t.Fatalf("%v %+v", err, resp)
	}
	if _, err := r.Chat(context.Background(), llm.Request{Model: "worker", Meta: llm.Meta{NoDegrade: true}}, nil); err == nil {
		t.Fatal("reviewer darf nicht degradieren")
	}
}

func TestPrefixAffinity(t *testing.T) {
	a, b := &fakeNode{name: "a"}, &fakeNode{name: "b"}
	r := setup(t,
		&Deployment{Name: "a", Provider: "runpod", Client: a, MaxConcurrency: 8},
		&Deployment{Name: "b", Provider: "runpod", Client: b, MaxConcurrency: 8},
	)
	first, _ := r.Chat(context.Background(), llm.Request{Model: "worker", Meta: llm.Meta{PrefixHash: "p1"}}, nil)
	for i := 0; i < 10; i++ {
		resp, _ := r.Chat(context.Background(), llm.Request{Model: "worker", Meta: llm.Meta{PrefixHash: "p1"}}, nil)
		if resp.Deployment != first.Deployment {
			t.Fatal("prefix-affinität verletzt")
		}
	}
}

func TestPriorityAndReserve(t *testing.T) {
	n := &fakeNode{name: "n", gate: make(chan struct{})}
	cfg := DefaultConfig()
	cfg.MaxShare = 0
	r := New(cfg, nil, nil)
	r.SetModel(Model{Name: "worker", PrivacyClass: "self_hosted"})
	r.Upsert(&Deployment{Name: "n", Model: "worker", Provider: "local", Client: n, MaxConcurrency: 10, State: Ready})
	ctx := context.Background()
	var wg sync.WaitGroup
	// Background darf nur 7 von 10 Slots nutzen (30 % Reserve).
	for i := 0; i < 9; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Chat(ctx, llm.Request{Model: "worker", Meta: llm.Meta{Priority: llm.Background}}, nil) }()
	}
	time.Sleep(50 * time.Millisecond)
	if got := n.inflight.Load(); got != 7 {
		t.Fatalf("background belegt %d slots, erwartet 7", got)
	}
	// Interactive bekommt sofort einen reservierten Slot.
	done := make(chan string, 1)
	go func() {
		resp, err := r.Chat(ctx, llm.Request{Model: "worker", Meta: llm.Meta{Priority: llm.Interactive}}, nil)
		if err == nil {
			done <- resp.Deployment
		}
	}()
	time.Sleep(50 * time.Millisecond)
	if got := n.inflight.Load(); got != 8 {
		t.Fatalf("interactive nicht zugelassen: inflight=%d", got)
	}
	snap := r.Snapshot()
	var bgDepth int
	for _, q := range snap.Queues {
		if q.Class == llm.Background {
			bgDepth = q.Depth
		}
	}
	if bgDepth != 2 {
		t.Fatalf("background-queue %d, erwartet 2", bgDepth)
	}
	close(n.gate)
	wg.Wait()
	<-done
}

func TestFairShare(t *testing.T) {
	n := &fakeNode{name: "n", gate: make(chan struct{})}
	r := New(DefaultConfig(), nil, nil)
	r.SetModel(Model{Name: "worker", PrivacyClass: "self_hosted"})
	r.Upsert(&Deployment{Name: "n", Model: "worker", Provider: "local", Client: n, MaxConcurrency: 10, State: Ready})
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Chat(ctx, llm.Request{Model: "worker", Meta: llm.Meta{Priority: llm.Interactive, DotID: "hog"}}, nil) }()
	}
	time.Sleep(50 * time.Millisecond)
	got := make(chan error, 1)
	go func() {
		_, err := r.Chat(ctx, llm.Request{Model: "worker", Meta: llm.Meta{Priority: llm.Interactive, DotID: "other"}}, nil)
		got <- err
	}()
	time.Sleep(50 * time.Millisecond)
	// Ein hog-Aufruf endet; der freie Slot geht an "other", obwohl ältere hog-Waiter warten,
	// weil "hog" bei wartenden anderen max. 60 % halten darf.
	n.gate <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	by := r.Snapshot().Deployments[0].ByDot
	if by["other"] != 1 {
		t.Fatalf("fair share verletzt: %v", by)
	}
	close(n.gate)
	wg.Wait()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

func TestCost(t *testing.T) {
	ext := &Deployment{PriceInPerMTok: 2_000_000, PriceOutPerMTok: 8_000_000}
	if c := cost(ext, llm.Usage{In: 1_000_000, Out: 500_000}, 0); c != 6_000_000 {
		t.Fatal(c)
	}
	gpu := &Deployment{CostPerHour: 3_600_000, MaxConcurrency: 1}
	if c := cost(gpu, llm.Usage{}, time.Second); c != 1000 {
		t.Fatal(c)
	}
}
