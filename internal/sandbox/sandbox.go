// Package sandbox verwaltet die Computer der Fylgjur (Spec Kapitel 13).
//
// Provider: docker-gvisor (Default), local (Entwicklung: nur Dateien, keine Shell), none.
// Der Manager implementiert builtin.Computer und spricht mit computerd in der Sandbox.
package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/tools/builtin"
	"github.com/realblxckcodex/fylgja/internal/vault"
)

// Instance beschreibt eine laufende Sandbox.
type Instance struct {
	Ref      string `json:"ref"`      // Container-ID
	Endpoint string `json:"endpoint"` // http://ip:7070
	VNC      string `json:"vnc"`      // ws://ip:6080/websockify
	Volume   string `json:"volume"`
	Token    string `json:"-"` // gesetzt, wenn der Provider ein festes Token vorgibt (static)
}

// Spec einer Sandbox.
type Spec struct {
	DotID     uuid.UUID
	Image     string
	Token     string
	MemoryMB  int
	CPUs      float64
	PidsLimit int
	Egress    string // open|allowlist|offline
}

// Provider (13.2): Create, Start, Sleep/Wake, Destroy, Stats.
type Provider interface {
	Name() string
	Ensure(ctx context.Context, s Spec) (Instance, error) // anlegen oder aufwecken
	Sleep(ctx context.Context, ref string) error
	Destroy(ctx context.Context, ref string, keepVolume bool) error
	Stats(ctx context.Context, ref string) (map[string]any, error)
}

// Manager verwaltet Sandboxen und implementiert builtin.Computer.
type Manager struct {
	Pool      *pgxpool.Pool
	Provider  Provider
	Keyring   *vault.Keyring
	Image     string
	IdleSleep time.Duration
	HTTP      *http.Client
	Log       *slog.Logger
	// Credentials entschlüsselt Secrets für browser.login (Broker, 13.8).
	Credentials func(ctx context.Context, dot, id uuid.UUID) (Credential, error)

	mu    sync.Mutex
	cache map[uuid.UUID]*live
}

type live struct {
	inst  Instance
	token string
	last  time.Time
}

// Credential ist ein entschlüsselter Zugang (verlässt nie den Broker→computerd-Pfad).
type Credential struct {
	Username       string   `json:"username"`
	Password       string   `json:"password"`
	TOTPSecret     string   `json:"totp_secret,omitempty"`
	AllowedDomains []string `json:"allowed_domains"`
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

func (m *Manager) client() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 35 * time.Minute}
}

var ErrNoSandbox = errors.New("sandbox: kein provider konfiguriert")

// ensure liefert eine laufende Sandbox für die Fylgja (Wake bei Bedarf).
func (m *Manager) ensure(ctx context.Context, dot uuid.UUID) (*live, error) {
	if m.Provider == nil {
		return nil, ErrNoSandbox
	}
	m.mu.Lock()
	if m.cache == nil {
		m.cache = map[uuid.UUID]*live{}
	}
	if l := m.cache[dot]; l != nil && time.Since(l.last) < m.idle() {
		l.last = time.Now()
		m.mu.Unlock()
		return l, nil
	}
	m.mu.Unlock()
	token, err := m.token(ctx, dot)
	if err != nil {
		return nil, err
	}
	var egress string
	_ = m.Pool.QueryRow(ctx, `SELECT CASE WHEN kind='specialist' THEN 'allowlist' ELSE 'open' END FROM dots WHERE id=$1`, dot).Scan(&egress)
	inst, err := m.Provider.Ensure(ctx, Spec{DotID: dot, Image: m.Image, Token: token, MemoryMB: 4096, CPUs: 2, PidsLimit: 512, Egress: egress})
	if err != nil {
		_, _ = m.Pool.Exec(ctx, `UPDATE sandboxes SET state='error' WHERE dot_id=$1`, dot)
		return nil, err
	}
	res, _ := json.Marshal(map[string]any{"endpoint": inst.Endpoint, "vnc": inst.VNC})
	_, _ = m.Pool.Exec(ctx, `UPDATE sandboxes SET state='running', volume_ref=$2, resources = resources || $3::jsonb, last_active_at=now(), image=$4 WHERE dot_id=$1`,
		dot, inst.Volume, res, m.Image)
	if inst.Token != "" {
		token = inst.Token
	}
	l := &live{inst: inst, token: token, last: time.Now()}
	if err := m.waitHealthy(ctx, l); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.cache[dot] = l
	m.mu.Unlock()
	return l, nil
}

func (m *Manager) idle() time.Duration {
	if m.IdleSleep <= 0 {
		return 15 * time.Minute
	}
	return m.IdleSleep
}

// token liefert (bzw. erzeugt) das computerd-Token; gespeichert verschlüsselt.
func (m *Manager) token(ctx context.Context, dot uuid.UUID) (string, error) {
	var ws uuid.UUID
	var res []byte
	err := m.Pool.QueryRow(ctx, `SELECT d.workspace_id, coalesce(s.resources, '{}'::jsonb) FROM dots d LEFT JOIN sandboxes s ON s.dot_id=d.id WHERE d.id=$1`, dot).Scan(&ws, &res)
	if err != nil {
		return "", err
	}
	var r struct {
		Token string `json:"token_sealed"`
		DEK   string `json:"token_dek"`
		KV    int    `json:"token_kv"`
	}
	_ = json.Unmarshal(res, &r)
	if r.Token != "" && m.Keyring != nil {
		ct, _ := base64.StdEncoding.DecodeString(r.Token)
		dek, _ := base64.StdEncoding.DecodeString(r.DEK)
		if pt, err := m.Keyring.Open(ws, vault.Sealed{Ciphertext: ct, WrappedDEK: dek, KeyVersion: r.KV}, dot[:]); err == nil {
			return string(pt), nil
		}
	}
	tok := ids.New().String() + ids.New().String()
	if m.Keyring == nil {
		return "", errors.New("sandbox: kein keyring")
	}
	sealed, err := m.Keyring.Seal(ws, []byte(tok), dot[:])
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{"token_sealed": base64.StdEncoding.EncodeToString(sealed.Ciphertext),
		"token_dek": base64.StdEncoding.EncodeToString(sealed.WrappedDEK), "token_kv": sealed.KeyVersion})
	_, err = m.Pool.Exec(ctx, `INSERT INTO sandboxes (id, dot_id, image, state, resources) VALUES ($1,$2,$3,'creating',$4)
		ON CONFLICT (dot_id) DO UPDATE SET resources = sandboxes.resources || EXCLUDED.resources`, ids.New(), dot, m.Image, b)
	return tok, err
}

func (m *Manager) waitHealthy(ctx context.Context, l *live) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, l.inst.Endpoint+"/healthz", nil)
		resp, err := m.client().Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return errors.New("sandbox: computerd antwortet nicht")
}

func (m *Manager) call(ctx context.Context, dot uuid.UUID, path string, body any, out any) error {
	l, err := m.ensure(ctx, dot)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.inst.Endpoint+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+l.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client().Do(req)
	if err != nil {
		m.mu.Lock()
		delete(m.cache, dot)
		m.mu.Unlock()
		return fmt.Errorf("computer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("computer: %s", bytes.TrimSpace(msg))
	}
	_, _ = m.Pool.Exec(context.WithoutCancel(ctx), `UPDATE sandboxes SET last_active_at=now() WHERE dot_id=$1`, dot)
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
	}
	return nil
}

// ---- builtin.Computer ----

func (m *Manager) Exec(ctx context.Context, dot uuid.UUID, cmd, cwd string, timeout time.Duration) (string, string, int, error) {
	var out struct {
		Stdout, Stderr string
		Exit           int
	}
	err := m.call(ctx, dot, "/v1/exec", map[string]any{"cmd": cmd, "cwd": cwd, "timeout_s": int(timeout.Seconds())}, &out)
	return out.Stdout, out.Stderr, out.Exit, err
}

func (m *Manager) ReadFile(ctx context.Context, dot uuid.UUID, path string) ([]byte, error) {
	var out struct {
		Data []byte `json:"data"`
	}
	err := m.call(ctx, dot, "/v1/fs/read", map[string]any{"path": path}, &out)
	return out.Data, err
}

func (m *Manager) WriteFile(ctx context.Context, dot uuid.UUID, path string, data []byte) error {
	return m.call(ctx, dot, "/v1/fs/write", map[string]any{"path": path, "data": data}, nil)
}

func (m *Manager) List(ctx context.Context, dot uuid.UUID, path string) ([]builtin.FileInfo, error) {
	var out []builtin.FileInfo
	err := m.call(ctx, dot, "/v1/fs/list", map[string]any{"path": path}, &out)
	return out, err
}

func (m *Manager) Browser(ctx context.Context, dot uuid.UUID, action string, args map[string]any) (string, []string, error) {
	var out struct {
		Text   string   `json:"text"`
		Egress []string `json:"egress"`
	}
	err := m.call(ctx, dot, "/v1/browser/"+url.PathEscape(action), args, &out)
	return out.Text, out.Egress, err
}

// Login: Der Broker entschlüsselt das Secret und sendet es direkt an computerd; das Modell sieht nur das Ergebnis.
func (m *Manager) Login(ctx context.Context, dot uuid.UUID, credID uuid.UUID, site string) (string, error) {
	if m.Credentials == nil {
		return "", errors.New("kein credential-broker")
	}
	c, err := m.Credentials(ctx, dot, credID)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(site)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("ungültige site")
	}
	if !DomainAllowed(u.Hostname(), c.AllowedDomains) {
		return "", fmt.Errorf("zugang ist nicht für %s freigegeben (phishing-schutz)", u.Hostname())
	}
	var out struct {
		Text string `json:"text"`
	}
	err = m.call(ctx, dot, "/v1/browser/login", map[string]any{"site": site, "username": c.Username, "password": c.Password, "totp_secret": c.TOTPSecret}, &out)
	return out.Text, err
}

// DomainAllowed prüft die Domain-Bindung eines Credentials (inkl. Subdomains).
func DomainAllowed(host string, allowed []string) bool {
	for _, a := range allowed {
		if host == a || (len(host) > len(a) && host[len(host)-len(a)-1:] == "."+a) {
			return true
		}
	}
	return false
}

// Session liefert die Live-View-Adresse (für den Proxy der API).
func (m *Manager) Session(ctx context.Context, dot uuid.UUID) (Instance, string, error) {
	l, err := m.ensure(ctx, dot)
	if err != nil {
		return Instance{}, "", err
	}
	return l.inst, l.token, nil
}

// Reap legt inaktive Sandboxen schlafen (Volume bleibt erhalten).
func (m *Manager) Reap(ctx context.Context) int {
	if m.Provider == nil {
		return 0
	}
	rows, err := m.Pool.Query(ctx, `SELECT dot_id, coalesce(resources->>'ref','') FROM sandboxes WHERE state='running' AND last_active_at < $1`, time.Now().Add(-m.idle()))
	if err != nil {
		return 0
	}
	type item struct {
		dot uuid.UUID
		ref string
	}
	var list []item
	for rows.Next() {
		var it item
		_ = rows.Scan(&it.dot, &it.ref)
		list = append(list, it)
	}
	rows.Close()
	n := 0
	for _, it := range list {
		m.mu.Lock()
		l := m.cache[it.dot]
		delete(m.cache, it.dot)
		m.mu.Unlock()
		ref := it.ref
		if l != nil {
			ref = l.inst.Ref
		}
		if ref == "" {
			continue
		}
		if err := m.Provider.Sleep(ctx, ref); err == nil {
			_, _ = m.Pool.Exec(ctx, `UPDATE sandboxes SET state='sleeping' WHERE dot_id=$1`, it.dot)
			n++
		}
	}
	return n
}

// Static nutzt einen extern gestarteten computerd (Entwicklung, Einzelmaschine).
type Static struct {
	Endpoint string
	Token    string
	VNC      string
}

func (s *Static) Name() string { return "static" }
func (s *Static) Ensure(context.Context, Spec) (Instance, error) {
	return Instance{Ref: "static", Endpoint: s.Endpoint, VNC: s.VNC, Token: s.Token}, nil
}
func (s *Static) Sleep(context.Context, string) error                   { return nil }
func (s *Static) Destroy(context.Context, string, bool) error           { return nil }
func (s *Static) Stats(context.Context, string) (map[string]any, error) { return map[string]any{}, nil }
