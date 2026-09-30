package link

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/fleet/tunnel"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/tools"
)

// Registry verwaltet verbundene Links (Server-Seite).
type Registry struct {
	Pool *pgxpool.Pool

	mu    sync.Mutex
	dials map[string]tunnel.DialFunc
	srv   *tunnel.Server
}

// Server liefert den WebSocket-Endpunkt für Link-Agents.
func (r *Registry) Server() *tunnel.Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.srv == nil {
		r.srv = &tunnel.Server{H: r}
	}
	return r.srv
}

func hash(t string) []byte { h := sha256.Sum256([]byte(t)); return h[:] }

// Create legt einen Link an und liefert das Token (nur einmal sichtbar).
func (r *Registry) Create(ctx context.Context, ws, user, dot uuid.UUID, name string) (uuid.UUID, string, error) {
	id := ids.New()
	tok := ids.New().String() + ids.New().String()
	_, err := r.Pool.Exec(ctx, `INSERT INTO links (id, workspace_id, user_id, dot_id, name, token_hash) VALUES ($1,$2,$3,$4,$5,$6)`, id, ws, user, dot, name, hash(tok))
	return id, tok, err
}

// Revoke trennt sofort (Kill-Switch der Web-UI).
func (r *Registry) Revoke(ctx context.Context, id uuid.UUID) error {
	_, err := r.Pool.Exec(ctx, `UPDATE links SET revoked_at=now() WHERE id=$1`, id)
	r.Server().Disconnect(id.String())
	r.mu.Lock()
	delete(r.dials, id.String())
	r.mu.Unlock()
	return err
}

func (r *Registry) Authenticate(id, token string) bool {
	var h []byte
	err := r.Pool.QueryRow(context.Background(), `SELECT token_hash FROM links WHERE id=$1 AND revoked_at IS NULL`, id).Scan(&h)
	return err == nil && bytes.Equal(h, hash(token))
}

func (r *Registry) OnRegister(id string, raw json.RawMessage, dial tunnel.DialFunc) {
	r.mu.Lock()
	if r.dials == nil {
		r.dials = map[string]tunnel.DialFunc{}
	}
	r.dials[id] = dial
	r.mu.Unlock()
	_, _ = r.Pool.Exec(context.Background(), `UPDATE links SET last_seen=now(), capabilities=$2 WHERE id=$1`, id, raw)
}

func (r *Registry) OnHeartbeat(id string, _ json.RawMessage) {
	_, _ = r.Pool.Exec(context.Background(), `UPDATE links SET last_seen=now() WHERE id=$1`, id)
}

func (r *Registry) OnDisconnect(id string) {
	r.mu.Lock()
	delete(r.dials, id)
	r.mu.Unlock()
}

var ErrOffline = errors.New("laptop-link ist nicht verbunden")

func (r *Registry) call(ctx context.Context, dot uuid.UUID, path string, body any, out any) error {
	var id uuid.UUID
	if err := r.Pool.QueryRow(ctx, `SELECT id FROM links WHERE dot_id=$1 AND revoked_at IS NULL ORDER BY last_seen DESC NULLS LAST LIMIT 1`, dot).Scan(&id); err != nil {
		return ErrOffline
	}
	r.mu.Lock()
	dial := r.dials[id.String()]
	r.mu.Unlock()
	if dial == nil {
		return ErrOffline
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://link"+path, bytes.NewReader(b))
	resp, err := tunnel.HTTPClient(dial, 3*time.Minute).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("laptop: %s", bytes.TrimSpace(msg))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 12<<20)).Decode(out)
}

// RegisterTools registriert laptop.* (Klasse laptop: in allen Autonomiestufen ask, außer Regel auf L3).
func (r *Registry) RegisterTools(reg *tools.Registry) {
	mk := func(name, desc, schema, path, preview string) {
		reg.MustRegister(&tools.Tool{Name: name, Class: policy.Laptop, Source: "link", Description: desc, Schema: json.RawMessage(schema), Preview: preview,
			Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
				var args map[string]any
				_ = json.Unmarshal(c.Args, &args)
				var out any
				if err := r.call(ctx, uuid.MustParse(c.Env.DotID), path, args, &out); err != nil {
					return tools.Result{Content: err.Error(), IsError: true}, nil
				}
				b, _ := json.Marshal(out)
				return tools.Result{Content: string(b), Untrusted: true, Source: "laptop"}, nil
			}})
	}
	mk("laptop.read", "Liest eine Datei auf dem Laptop des Owners (nur freigegebene Ordner, braucht Freigabe).", `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`, "/v1/fs/read", "Laptop: {path} lesen")
	mk("laptop.list", "Listet einen Ordner auf dem Laptop des Owners.", `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`, "/v1/fs/list", "Laptop: {path} auflisten")
	mk("laptop.write", "Schreibt eine Datei auf dem Laptop des Owners.", `{"type":"object","properties":{"path":{"type":"string"},"data":{"type":"string"}},"required":["path","data"]}`, "/v1/fs/write", "Laptop: {path} schreiben")
	mk("laptop.exec", "Führt ein freigegebenes Kommando auf dem Laptop aus (jedes Kommando braucht Freigabe).", `{"type":"object","properties":{"cmd":{"type":"string"},"cwd":{"type":"string"}},"required":["cmd"]}`, "/v1/exec", "Laptop: $ {cmd}")
}
