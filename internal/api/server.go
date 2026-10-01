// Package api ist die HTTP-Schnittstelle (Spec Kapitel 19): REST (JSON), SSE-Streams,
// WebSocket-Proxys, OpenAI-kompatibles Router-Gateway, Webhooks und die eingebettete Web-UI.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/audit"
	"github.com/realblxckcodex/fylgja/internal/auth"
	"github.com/realblxckcodex/fylgja/internal/channels"
	"github.com/realblxckcodex/fylgja/internal/coord"
	"github.com/realblxckcodex/fylgja/internal/events"
	"github.com/realblxckcodex/fylgja/internal/fleet"
	"github.com/realblxckcodex/fylgja/internal/fleet/pki"
	"github.com/realblxckcodex/fylgja/internal/fleet/tunnel"
	"github.com/realblxckcodex/fylgja/internal/link"
	"github.com/realblxckcodex/fylgja/internal/memory"
	"github.com/realblxckcodex/fylgja/internal/pulse"
	"github.com/realblxckcodex/fylgja/internal/router"
	"github.com/realblxckcodex/fylgja/internal/runtime"
	"github.com/realblxckcodex/fylgja/internal/sandbox"
	"github.com/realblxckcodex/fylgja/internal/tools"
	"github.com/realblxckcodex/fylgja/internal/vault"
)

// Server bündelt alle Abhängigkeiten der API.
type Server struct {
	Pool        *pgxpool.Pool
	Auth        *auth.Service
	Passkeys    *auth.Passkeys
	Runtime     *runtime.Engine
	Memory      *memory.Service
	Hub         *channels.Hub
	Bus         *events.Bus
	Router      *router.Router
	Fleet       *fleet.Manager
	Tunnel      *tunnel.Server
	NodeCA      *pki.CA
	Links       *link.Registry
	Coord       *coord.Coordinator
	Pulse       *pulse.Engine
	Tools       *tools.Registry
	Sandbox     *sandbox.Manager
	Audit       *audit.PG
	Keyring     *vault.Keyring
	Redactor    *vault.Redactor
	Log         *slog.Logger
	UI          http.Handler // eingebettete Web-UI
	BaseURL     string
	Secure      bool   // Secure-Cookies (https)
	RouterToken string // /router/v1 (leer = deaktiviert)
	HookKey     []byte // HMAC-Basis für Webhooks
	SkillKey    []byte // Basis für Skill-Signaturen
	Version     string

	idem    sync.Map
	limiter *limiter
}

const cookieName = "fylgja_session"

type ctxKey int

const principalKey ctxKey = 1

func principal(r *http.Request) *auth.Principal {
	p, _ := r.Context().Value(principalKey).(*auth.Principal)
	return p
}

func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

// Problem ist RFC 9457 Problem Details.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func problem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("ungültiger json-body: %w", err)
	}
	return nil
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, name))
}

// ---- Middleware ----

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(self)")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// authn löst Session-Cookie oder Bearer-Token auf. CSRF: Cookie-Requests mit Seiteneffekt
// brauchen den Header X-Requested-With: fylgja (erzwingt CORS-Preflight bei Fremd-Origins).
func (s *Server) authn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p *auth.Principal
		var err error
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer fyt_") {
			p, err = s.Auth.Token(r.Context(), strings.TrimPrefix(h, "Bearer "))
		} else if tok := sessionToken(r); tok != "" {
			p, err = s.Auth.Session(r.Context(), tok)
			if err == nil && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Requested-With") != "fylgja" {
				problem(w, http.StatusForbidden, "csrf-schutz: header X-Requested-With fehlt")
				return
			}
		} else {
			err = auth.ErrInvalid
		}
		if err != nil || p == nil {
			problem(w, http.StatusUnauthorized, "nicht angemeldet")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	})
}

func need(action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p := principal(r); p == nil || !p.Can(action) {
				problem(w, http.StatusForbidden, "keine berechtigung ("+action+")")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// stepUp erzwingt eine frische Passkey-/Passwort-Bestätigung für kritische Aktionen.
func stepUp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		if p == nil || !stepped(p) {
			w.Header().Set("X-Fylgja-Step-Up", "required")
			problem(w, http.StatusPreconditionRequired, "step-up erforderlich: bitte mit passkey oder passwort bestätigen")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type cachedResp struct {
	status int
	body   []byte
	at     time.Time
}

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(s int) { r.status = s; r.ResponseWriter.WriteHeader(s) }
func (r *recorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

// idempotency: schreibende Endpunkte sind über Idempotency-Key wiederholbar (19.3).
func (s *Server) idempotency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		p := principal(r)
		k := r.Method + " " + r.URL.Path + " " + key
		if p != nil {
			k = p.UserID.String() + " " + k
		}
		if v, ok := s.idem.Load(k); ok {
			c := v.(cachedResp)
			if time.Since(c.at) < 24*time.Hour {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(c.status)
				w.Write(c.body)
				return
			}
		}
		rec := &recorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if rec.status < 500 {
			s.idem.Store(k, cachedResp{status: rec.status, body: rec.buf.Bytes(), at: time.Now()})
		}
	})
}

// limiter: einfacher Token-Bucket pro Schlüssel (Login-Brute-Force, API-Rate-Limits).
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(perMinute, burst float64) *limiter {
	return &limiter{buckets: map[string]*bucket{}, rate: perMinute / 60, burst: burst}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) audit(r *http.Request, action, target string, detail map[string]any) {
	p := principal(r)
	if s.Audit == nil || p == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["ip"] = clientIP(r)
	_ = s.Audit.Log(r.Context(), audit.Entry{WorkspaceID: p.WorkspaceID, Actor: "user:" + p.UserID.String(), Action: action, Target: target, Detail: detail})
}

// Handler baut den Router.
func (s *Server) Handler() http.Handler {
	s.limiter = newLimiter(10, 5)
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, securityHeaders)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	r.Get("/readyz", s.ready)
	r.Get("/metrics", s.metrics)
	r.Mount("/router/v1", s.routerGateway())
	r.Post("/hooks/{dot}/{source}", s.webhook)
	if s.Tunnel != nil {
		r.Handle("/api/v1/node/tunnel", s.Tunnel)
		r.Post("/api/v1/node/enroll", s.nodeEnroll)
	}
	if s.Links != nil {
		r.Handle("/api/v1/link/tunnel", s.Links.Server())
	}
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/setup/status", s.setupStatus)
		r.Post("/setup", s.setup)
		r.Post("/auth/login", s.login)
		r.Post("/auth/passkey/login/begin", s.passkeyLoginBegin)
		r.Post("/auth/passkey/login/finish", s.passkeyLoginFinish)
		r.Group(func(r chi.Router) {
			r.Use(s.authn, s.idempotency)
			s.routes(r)
		})
	})
	if s.UI != nil {
		r.Handle("/*", s.UI)
	}
	return r
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Pool.Ping(ctx); err != nil {
		problem(w, http.StatusServiceUnavailable, "datenbank nicht erreichbar")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "version": s.Version})
}

var errForbidden = errors.New("keine berechtigung")

// dotInWorkspace prüft, dass eine Fylgja zum Workspace des Nutzers gehört.
func (s *Server) dotInWorkspace(r *http.Request, dot uuid.UUID) error {
	p := principal(r)
	var ws uuid.UUID
	if err := s.Pool.QueryRow(r.Context(), `SELECT workspace_id FROM dots WHERE id=$1`, dot).Scan(&ws); err != nil || ws != p.WorkspaceID {
		return errForbidden
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// stepped: frische Step-up-Session oder API-Token mit ausdrücklichem Scope "stepup"
// (nur mit Step-up erzeugbar, für Automatisierung/CLI).
func stepped(p *auth.Principal) bool {
	if p.ViaToken {
		for _, s := range p.Scopes {
			if s == "stepup" {
				return true
			}
		}
		return false
	}
	return p.SteppedUp(time.Now())
}
