package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/realblxckcodex/fylgja/internal/fleet/pki"
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

func TestMTLS(t *testing.T) {
	ca, err := pki.NewCA([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	hh := &h{reg: make(chan DialFunc, 1), down: make(chan struct{})}
	srv := &Server{H: hh, CA: ca, RequireMTLS: true}
	mux := http.NewServeMux()
	mux.Handle("/tunnel", srv)
	mux.HandleFunc("/enroll", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "no", 401)
			return
		}
		csr, _ := io.ReadAll(r.Body)
		cert, na, err := ca.SignCSR(csr, r.Header.Get("X-Fylgja-Node"), time.Hour, time.Now())
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		json.NewEncoder(w).Encode(EnrollResponse{Cert: string(cert), CA: string(ca.CertPEM()), ExpiresAt: na})
	})
	cp := httptest.NewUnstartedServer(mux)
	cp.TLS = &tls.Config{ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: ca.Pool()}
	cp.StartTLS()
	defer cp.Close()
	roots := x509.NewCertPool()
	roots.AddCert(cp.Certificate())
	base := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
	wsURL := "wss" + strings.TrimPrefix(cp.URL, "https") + "/tunnel"

	// Ohne Zertifikat: abgelehnt.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	noCert := &Agent{URL: wsURL, NodeID: "n1", Token: "secret", HTTP: &http.Client{Transport: &http.Transport{TLSClientConfig: base}}}
	if err := noCert.once(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ohne zertifikat sollte 401 kommen, got %v", err)
	}

	// Zertifikat für anderen Node: abgelehnt.
	other := &Enroller{URL: "https" + strings.TrimPrefix(cp.URL, "https") + "/enroll", NodeID: "n2", Token: "secret", Base: base}
	if err := other.Enroll(ctx); err != nil {
		t.Fatal(err)
	}
	wrong := &Agent{URL: wsURL, NodeID: "n1", Token: "secret", HTTP: other.HTTPClient()}
	if err := wrong.once(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("fremdes zertifikat sollte 401 kommen, got %v", err)
	}

	// Falsches Token beim Enrollment.
	bad := &Enroller{URL: other.URL, NodeID: "n1", Token: "nope", Base: base}
	if err := bad.Enroll(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("enroll mit falschem token: %v", err)
	}

	// Richtiges Zertifikat: Tunnel steht.
	en := &Enroller{URL: other.URL, NodeID: "n1", Token: "secret", Base: base}
	if err := en.Enroll(ctx); err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithCancel(context.Background())
	defer rcancel()
	agent := &Agent{URL: wsURL, NodeID: "n1", Token: "secret", Upstream: strings.TrimPrefix(up.URL, "http://"), HTTP: en.HTTPClient(),
		Register: func() any { return map[string]string{"gpu_model": "L40S"} }}
	go agent.Run(rctx)
	select {
	case <-hh.reg:
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel mit gültigem zertifikat kam nicht zustande")
	}
}
