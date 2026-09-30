// Package egress ist egressd: Forward-Proxy pro Sandbox-Host (Spec 13.7).
// Allow/Deny pro Quelle (Sandbox-IP), Sperre privater Netze/Metadata, Logging ohne Bodys, Rate-Limits.
package egress

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/realblxckcodex/fylgja/internal/platform/netguard"
)

// Mode je Quelle.
type Mode string

const (
	Open      Mode = "open"
	Allowlist Mode = "allowlist"
	Offline   Mode = "offline"
)

// Rule ist die Policy einer Quelle.
type Rule struct {
	Mode   Mode     `json:"mode"`
	Allow  []string `json:"allow"`   // Domains (inkl. Subdomains)
	Deny   []string `json:"deny"`    // immer gesperrt
	PerMin int      `json:"per_min"` // Requests pro Minute (0 = 600)
}

// Policy: Quelle (IP) → Regel; Default für unbekannte Quellen.
type Policy struct {
	mu      sync.RWMutex
	Sources map[string]Rule `json:"sources"`
	Default Rule            `json:"default"`
}

func (p *Policy) rule(src string) Rule {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if r, ok := p.Sources[src]; ok {
		return r
	}
	return p.Default
}

// Set ersetzt die Regeln (z. B. bei Konfig-Reload).
func (p *Policy) Set(sources map[string]Rule, def Rule) {
	p.mu.Lock()
	p.Sources, p.Default = sources, def
	p.mu.Unlock()
}

func matchDomain(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range list {
		d = strings.ToLower(d)
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// Decide prüft Domain gegen die Regel (IP-Prüfung erfolgt nach DNS im Dialer).
func Decide(r Rule, host string) (bool, string) {
	if matchDomain(host, r.Deny) {
		return false, "deny-liste"
	}
	switch r.Mode {
	case Offline:
		return false, "offline"
	case Allowlist:
		if !matchDomain(host, r.Allow) {
			return false, "nicht in allowlist"
		}
	}
	return true, ""
}

// Entry ist ein Log-Eintrag (ohne Bodys).
type Entry struct {
	At       time.Time `json:"at"`
	Source   string    `json:"source"`
	Method   string    `json:"method"`
	Host     string    `json:"host"`
	Allowed  bool      `json:"allowed"`
	Reason   string    `json:"reason,omitempty"`
	BytesOut int64     `json:"bytes_out"`
	BytesIn  int64     `json:"bytes_in"`
	Status   int       `json:"status,omitempty"`
}

// Proxy ist der Forward-Proxy (HTTP + CONNECT).
type Proxy struct {
	Policy *Policy
	Guard  *netguard.Guard
	Log    *slog.Logger
	OnLog  func(Entry)

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	n     int
	reset time.Time
}

func (p *Proxy) limit(src string, perMin int) bool {
	if perMin <= 0 {
		perMin = 600
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buckets == nil {
		p.buckets = map[string]*bucket{}
	}
	b := p.buckets[src]
	now := time.Now()
	if b == nil || now.After(b.reset) {
		b = &bucket{reset: now.Add(time.Minute)}
		p.buckets[src] = b
	}
	b.n++
	return b.n <= perMin
}

func (p *Proxy) log(e Entry) {
	if p.OnLog != nil {
		p.OnLog(e)
	}
	if p.Log != nil {
		b, _ := json.Marshal(e)
		p.Log.Info("egress", "entry", json.RawMessage(b))
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	src, _, _ := net.SplitHostPort(r.RemoteAddr)
	host := r.URL.Hostname()
	if r.Method == http.MethodConnect {
		host, _, _ = net.SplitHostPort(r.Host)
		if host == "" {
			host = r.Host
		}
	}
	rule := p.Policy.rule(src)
	e := Entry{At: time.Now(), Source: src, Method: r.Method, Host: host}
	if ok, why := Decide(rule, host); !ok {
		e.Reason = why
		p.log(e)
		http.Error(w, "egress blockiert: "+why, http.StatusForbidden)
		return
	}
	if !p.limit(src, rule.PerMin) {
		e.Reason = "rate-limit"
		p.log(e)
		http.Error(w, "egress rate-limit", http.StatusTooManyRequests)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r, e)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "nur proxy-anfragen", 400)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
		out.Header.Del(h)
	}
	resp, err := p.Guard.Client(60 * time.Second).Transport.RoundTrip(out)
	if err != nil {
		e.Reason = err.Error()
		p.log(e)
		http.Error(w, "egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	e.Allowed, e.BytesIn, e.Status = true, n, resp.StatusCode
	e.BytesOut = r.ContentLength
	p.log(e)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request, e Entry) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	up, err := p.Guard.DialContext(ctx, "tcp", r.Host)
	cancel()
	if err != nil {
		e.Reason = err.Error()
		p.log(e)
		http.Error(w, "egress: "+err.Error(), http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hijack nicht möglich", 500)
		return
	}
	w.WriteHeader(http.StatusOK)
	client, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	var in, out int64
	done := make(chan struct{}, 2)
	go func() {
		if buf.Reader.Buffered() > 0 {
			nb, _ := io.CopyN(up, buf, int64(buf.Reader.Buffered()))
			out += nb
		}
		n, _ := io.Copy(up, client)
		out += n
		done <- struct{}{}
	}()
	go func() { n, _ := io.Copy(client, up); in = n; done <- struct{}{} }()
	<-done
	client.Close()
	up.Close()
	<-done
	e.Allowed, e.BytesIn, e.BytesOut = true, in, out
	p.log(e)
}
