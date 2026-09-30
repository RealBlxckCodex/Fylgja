// Package tunnel implementiert den ausgehenden Node-Tunnel (Spec 16.9, D7).
//
// Der Node (fylgja-node) baut eine WebSocket-Verbindung (WSS) zum Control Plane auf.
// Darüber läuft yamux: Der Control Plane öffnet Streams zum Node, die der Node an
// seine lokale OpenAI-API (vLLM/Ollama) weiterreicht. Der Node öffnet einen
// Kontroll-Stream für Registrierung und Heartbeats. Es gibt keinen öffentlichen
// Inference-Port.
package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Control ist eine Nachricht auf dem Kontroll-Stream (Node → Control Plane).
type Control struct {
	Type         string          `json:"type"` // register|heartbeat
	Registration json.RawMessage `json:"registration,omitempty"`
	Metrics      json.RawMessage `json:"metrics,omitempty"`
}

// Handler verarbeitet Tunnel-Ereignisse auf Seite des Control Planes.
type Handler interface {
	Authenticate(nodeID, token string) bool
	OnRegister(nodeID string, reg json.RawMessage, dial DialFunc)
	OnHeartbeat(nodeID string, metrics json.RawMessage)
	OnDisconnect(nodeID string)
}

// DialFunc öffnet eine TCP-artige Verbindung zum Upstream des Nodes.
type DialFunc func(ctx context.Context) (net.Conn, error)

// Server nimmt Node-Tunnel an.
type Server struct {
	H   Handler
	Log *slog.Logger

	mu       sync.Mutex
	sessions map[string]*yamux.Session
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ServeHTTP akzeptiert den WebSocket-Upgrade eines Nodes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nodeID := r.Header.Get("X-Fylgja-Node")
	token := r.Header.Get("Authorization")
	if len(token) > 7 && token[:7] == "Bearer " {
		token = token[7:]
	}
	if nodeID == "" || !s.H.Authenticate(nodeID, token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(-1)
	ctx := context.WithoutCancel(r.Context())
	conn := websocket.NetConn(ctx, c, websocket.MessageBinary)
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 10 * time.Second
	sess, err := yamux.Client(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]*yamux.Session{}
	}
	if old := s.sessions[nodeID]; old != nil {
		old.Close()
	}
	s.sessions[nodeID] = sess
	s.mu.Unlock()
	s.log().Info("node tunnel verbunden", "node", nodeID)
	dial := func(ctx context.Context) (net.Conn, error) { return sess.Open() }
	for {
		st, err := sess.Accept()
		if err != nil {
			break
		}
		go s.control(nodeID, st, dial)
	}
	s.mu.Lock()
	if s.sessions[nodeID] == sess {
		delete(s.sessions, nodeID)
	}
	s.mu.Unlock()
	s.H.OnDisconnect(nodeID)
	s.log().Info("node tunnel getrennt", "node", nodeID)
}

func (s *Server) control(nodeID string, st net.Conn, dial DialFunc) {
	defer st.Close()
	dec := json.NewDecoder(bufio.NewReader(st))
	for {
		var m Control
		if err := dec.Decode(&m); err != nil {
			return
		}
		switch m.Type {
		case "register":
			s.H.OnRegister(nodeID, m.Registration, dial)
		case "heartbeat":
			s.H.OnHeartbeat(nodeID, m.Metrics)
		}
	}
}

// Dialer liefert eine DialFunc für einen verbundenen Node.
func (s *Server) Dialer(nodeID string) (DialFunc, bool) {
	s.mu.Lock()
	sess := s.sessions[nodeID]
	s.mu.Unlock()
	if sess == nil {
		return nil, false
	}
	return func(ctx context.Context) (net.Conn, error) { return sess.Open() }, true
}

// Disconnect trennt einen Node (z. B. nach Terminate).
func (s *Server) Disconnect(nodeID string) {
	s.mu.Lock()
	if sess := s.sessions[nodeID]; sess != nil {
		sess.Close()
	}
	s.mu.Unlock()
}

// HTTPClient baut einen http.Client, der über den Tunnel wählt.
func HTTPClient(dial DialFunc, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{Transport: tr}
}

// Agent ist die Node-Seite des Tunnels.
type Agent struct {
	URL      string // wss://control/api/v1/node/tunnel
	NodeID   string
	Token    string
	Upstream string // lokale Adresse von vLLM/Ollama, z. B. 127.0.0.1:8000
	Log      *slog.Logger
	HTTP     *http.Client

	// Register liefert die Registrierung, Metrics die aktuellen Metriken (Heartbeat).
	Register func() any
	Metrics  func() any
	Interval time.Duration
}

// Run verbindet und hält den Tunnel; bei Abbruch Reconnect mit Backoff.
func (a *Agent) Run(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := a.once(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		a.logger().Warn("tunnel getrennt, reconnect", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return ctx.Err()
}

func (a *Agent) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

var ErrUnauthorized = errors.New("tunnel: nicht autorisiert")

func (a *Agent) once(ctx context.Context) error {
	h := http.Header{}
	h.Set("X-Fylgja-Node", a.NodeID)
	h.Set("Authorization", "Bearer "+a.Token)
	c, resp, err := websocket.Dial(ctx, a.URL, &websocket.DialOptions{HTTPHeader: h, HTTPClient: a.HTTP})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		return err
	}
	c.SetReadLimit(-1)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn := websocket.NetConn(sctx, c, websocket.MessageBinary)
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	sess, err := yamux.Server(conn, cfg)
	if err != nil {
		conn.Close()
		return err
	}
	defer sess.Close()
	ctl, err := sess.Open()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(ctl)
	reg, _ := json.Marshal(a.Register())
	if err := enc.Encode(Control{Type: "register", Registration: reg}); err != nil {
		return err
	}
	a.logger().Info("tunnel verbunden", "url", a.URL)
	go func() {
		iv := a.Interval
		if iv == 0 {
			iv = 5 * time.Second
		}
		t := time.NewTicker(iv)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-t.C:
				var met json.RawMessage
				if a.Metrics != nil {
					met, _ = json.Marshal(a.Metrics())
				}
				if err := enc.Encode(Control{Type: "heartbeat", Metrics: met}); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		st, err := sess.Accept()
		if err != nil {
			return fmt.Errorf("tunnel: %w", err)
		}
		go a.proxy(st)
	}
}

// proxy reicht einen Stream an den lokalen Upstream weiter.
func (a *Agent) proxy(st net.Conn) {
	defer st.Close()
	up, err := net.DialTimeout("tcp", a.Upstream, 10*time.Second)
	if err != nil {
		// Minimale HTTP-Antwort, damit der Router sauber einen 502 sieht.
		io.WriteString(st, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, st); done <- struct{}{} }()
	go func() { io.Copy(st, up); done <- struct{}{} }()
	<-done
}
