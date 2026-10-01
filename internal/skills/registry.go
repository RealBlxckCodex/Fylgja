package skills

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Skill-Registry: ein statischer JSON-Index (beliebig gehostet), dessen Einträge von einem
// vertrauten Publisher signiert sind. Installation legt immer nur einen *Entwurf* an. Aktiviert
// und mit dem Workspace-Schlüssel signiert wird erst nach Scanner-Lauf, Step-up und Freigabe
// durch den Owner (Activate). Der Registry-Betreiber kann also nie etwas ohne Zustimmung aktivieren.

const (
	maxIndexBytes = 2 << 20
	regDomain     = "fylgja/registry/v1\x00"
)

// Entry ist ein Skill im Index.
type Entry struct {
	Name        string            `json:"name"`
	Version     int               `json:"version"`
	Description string            `json:"description"`
	Body        string            `json:"body_md"`
	Files       map[string]string `json:"files,omitempty"`
	Manifest    Manifest          `json:"manifest"`
	Publisher   string            `json:"publisher"`
	Signature   string            `json:"signature"` // base64(Ed25519)
}

// Index ist das Registry-Dokument.
type Index struct {
	Skills []Entry `json:"skills"`
}

func entryMessage(e Entry) []byte {
	return append([]byte(regDomain), Digest(e.Name, e.Version, e.Body, e.Files, e.Manifest)...)
}

// SignEntry signiert einen Eintrag mit dem Publisher-Schlüssel.
func SignEntry(priv ed25519.PrivateKey, publisher string, e Entry) Entry {
	e.Publisher = publisher
	e.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, entryMessage(e)))
	return e
}

// Trust verwaltet die vertrauten Publisher (Name → öffentlicher Schlüssel).
type Trust map[string]ed25519.PublicKey

// ParseTrust liest Publisher aus Paaren (name, base64-Schlüssel).
func ParseTrust(pairs map[string]string) (Trust, error) {
	t := Trust{}
	for n, k := range pairs {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("skills: publisher %q: ungültiger Ed25519-Schlüssel", n)
		}
		t[n] = ed25519.PublicKey(b)
	}
	return t, nil
}

var ErrUntrusted = errors.New("skills: publisher nicht vertraut oder signatur ungültig")

// Verify prüft Publisher-Signatur und Inhalt. Der Scanner läuft hier mit: ein signierter
// Skill mit verdächtigen Mustern wird trotzdem nicht installiert.
func (t Trust) Verify(e Entry) ([]Finding, error) {
	pub, ok := t[e.Publisher]
	if !ok {
		return nil, ErrUntrusted
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil || !ed25519.Verify(pub, entryMessage(e), sig) {
		return nil, ErrUntrusted
	}
	if e.Name == "" || e.Version < 1 || len(e.Body) > 256<<10 {
		return nil, errors.New("skills: eintrag unvollständig oder zu groß")
	}
	if f := Scan(e.Body, e.Files); len(f) > 0 {
		return f, ErrScanner
	}
	return nil, nil
}

// Registry holt Indizes.
type Registry struct {
	HTTP  *http.Client
	URLs  []string
	Trust Trust
}

// Listing ist ein Index-Eintrag samt Prüfergebnis, für die Anzeige.
type Listing struct {
	Entry
	Registry string    `json:"registry"`
	Verified bool      `json:"verified"`
	Problem  string    `json:"problem,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
}

func (r *Registry) fetch(ctx context.Context, raw string) (*Index, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("skills: registry-url %q ungültig", raw)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skills: registry %s: status %d", u.Host, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes+1))
	if err != nil || len(b) > maxIndexBytes {
		return nil, errors.New("skills: registry-index zu groß")
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("skills: registry-index: %w", err)
	}
	return &idx, nil
}

// List liefert alle Einträge aller Registries mit Prüfstatus. Nicht erreichbare Registries
// werden übersprungen und in errs gemeldet.
func (r *Registry) List(ctx context.Context) (out []Listing, errs []string) {
	for _, u := range r.URLs {
		idx, err := r.fetch(ctx, u)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, e := range idx.Skills {
			l := Listing{Entry: e, Registry: u}
			f, err := r.Trust.Verify(e)
			l.Findings = f
			if err != nil {
				l.Problem = err.Error()
			} else {
				l.Verified = true
			}
			out = append(out, l)
		}
	}
	return out, errs
}

// Find sucht einen Eintrag in einer bestimmten Registry und prüft ihn.
func (r *Registry) Find(ctx context.Context, registry, name string, version int) (*Entry, error) {
	ok := false
	for _, u := range r.URLs {
		ok = ok || u == registry
	}
	if !ok {
		return nil, errors.New("skills: registry nicht konfiguriert")
	}
	idx, err := r.fetch(ctx, registry)
	if err != nil {
		return nil, err
	}
	var best *Entry
	for i, e := range idx.Skills {
		if e.Name == name && (version == 0 && (best == nil || e.Version > best.Version) || e.Version == version && version != 0) {
			best = &idx.Skills[i]
		}
	}
	if best == nil {
		return nil, errors.New("skills: nicht im index")
	}
	if _, err := r.Trust.Verify(*best); err != nil {
		return nil, err
	}
	return best, nil
}

var ErrExists = errors.New("skills: diese version ist schon installiert")

// Install legt den geprüften Eintrag als Entwurf (origin=imported) an.
func Install(ctx context.Context, pool *pgxpool.Pool, ws uuid.UUID, e Entry) (uuid.UUID, error) {
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM skills WHERE workspace_id=$1 AND name=$2 AND version=$3 AND origin='imported'`, ws, e.Name, e.Version).Scan(&n)
	if n > 0 {
		return uuid.Nil, ErrExists
	}
	files, _ := json.Marshal(e.Files)
	if e.Files == nil {
		files = []byte(`{}`)
	}
	man, _ := json.Marshal(e.Manifest)
	id := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO skills (id, workspace_id, name, version, description, body_md, files, manifest, status, origin)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'draft','imported')`, id, ws, strings.TrimSpace(e.Name), e.Version, e.Description, e.Body, files, man)
	return id, err
}
