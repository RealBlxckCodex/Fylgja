package tunnel

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type h struct {
	mu    sync.Mutex
	reg   chan DialFunc
	beats int
	down  chan struct{}
}

func (x *h) Authenticate(id, tok string) bool { return id == "n1" && tok == "secret" }
func (x *h) OnRegister(id string, reg json.RawMessage, d DialFunc) {
	if !strings.Contains(string(reg), "L40S") {
		panic("registration fehlt")
	}
	x.reg <- d
}
func (x *h) OnHeartbeat(string, json.RawMessage) { x.mu.Lock(); x.beats++; x.mu.Unlock() }
func (x *h) OnDisconnect(string)                 { close(x.down) }

func TestTunnelRoundtrip(t *testing.T) {
	// Lokaler "vLLM"-Upstream.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"qwen"}],"path":"`+r.URL.Path+`"}`)
	}))
	defer up.Close()
	hh := &h{reg: make(chan DialFunc, 1), down: make(chan struct{})}
	srv := &Server{H: hh}
	cp := httptest.NewServer(srv)
	defer cp.Close()

	ctx, cancel := context.WithCancel(context.Background())
	agent := &Agent{URL: "ws" + strings.TrimPrefix(cp.URL, "http"), NodeID: "n1", Token: "secret", Upstream: strings.TrimPrefix(up.URL, "http://"),
		Register: func() any { return map[string]string{"gpu_model": "L40S"} }, Metrics: func() any { return map[string]int{"x": 1} }, Interval: 20 * time.Millisecond}
	go agent.Run(ctx)
	var dial DialFunc
	select {
	case dial = <-hh.reg:
	case <-time.After(5 * time.Second):
		t.Fatal("keine registrierung")
	}
	hc := HTTPClient(dial, 5*time.Second)
	for i := 0; i < 5; i++ {
		resp, err := hc.Get("http://node/v1/models")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(b), "qwen") || !strings.Contains(string(b), "/v1/models") {
			t.Fatalf("antwort %s", b)
		}
	}
	time.Sleep(100 * time.Millisecond)
	hh.mu.Lock()
	if hh.beats == 0 {
		t.Error("keine heartbeats")
	}
	hh.mu.Unlock()
	cancel()
	select {
	case <-hh.down:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect nicht erkannt")
	}
}

func TestTunnelRejectsBadToken(t *testing.T) {
	srv := &Server{H: &h{}}
	cp := httptest.NewServer(srv)
	defer cp.Close()
	a := &Agent{URL: "ws" + strings.TrimPrefix(cp.URL, "http"), NodeID: "n1", Token: "wrong", Register: func() any { return nil }}
	if err := a.once(context.Background()); err != ErrUnauthorized {
		t.Fatalf("got %v", err)
	}
	_ = net.Conn(nil)
}
