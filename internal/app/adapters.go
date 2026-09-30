package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/channels"
	"github.com/realblxckcodex/fylgja/internal/fleet"
	"github.com/realblxckcodex/fylgja/internal/fleet/tunnel"
	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/memory"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/router"
	"github.com/realblxckcodex/fylgja/internal/runtime"
	"github.com/realblxckcodex/fylgja/internal/vault"
)

var systemWS = uuid.Nil

// ---- Router-Katalog ----

func (a *App) clientFor(kind, endpoint, key string) llm.Client {
	if kind == "anthropic" {
		return &llm.Anthropic{BaseURL: endpoint, APIKey: key}
	}
	return &llm.OpenAI{BaseURL: endpoint, APIKey: key}
}

func (a *App) sealGlobal(ctx context.Context, label, secret string) (uuid.UUID, error) {
	id := ids.New()
	s, err := a.Keyring.Seal(systemWS, []byte(secret), id[:])
	if err != nil {
		return uuid.Nil, err
	}
	_, err = a.Pool.Exec(ctx, `INSERT INTO vault_secrets (id, workspace_id, type, label, ciphertext, wrapped_dek, key_version) VALUES ($1,$2,'api_key',$3,$4,$5,$6)`,
		id, systemWS, label, s.Ciphertext, s.WrappedDEK, s.KeyVersion)
	return id, err
}

func (a *App) openGlobal(ctx context.Context, id uuid.UUID) (string, error) {
	var ct, dek []byte
	var kv int
	if err := a.Pool.QueryRow(ctx, `SELECT ciphertext, wrapped_dek, key_version FROM vault_secrets WHERE id=$1`, id).Scan(&ct, &dek, &kv); err != nil {
		return "", err
	}
	pt, err := a.Keyring.Open(systemWS, vault.Sealed{Ciphertext: ct, WrappedDEK: dek, KeyVersion: kv}, id[:])
	return string(pt), err
}

// loadCatalog übernimmt Modelle/Deployments aus der Konfiguration in die DB (Erststart) und lädt alles in den Router.
func (a *App) loadCatalog(ctx context.Context) error {
	for _, m := range a.Cfg.Router.Models {
		caps, _ := json.Marshal(map[string]any{"list": m.Capabilities, "max_context": m.MaxContext, "fallbacks": m.Fallbacks})
		if _, err := a.Pool.Exec(ctx, `INSERT INTO model_catalog (id, logical_name, capabilities, privacy_class, quality_rank) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (logical_name) DO UPDATE SET capabilities=EXCLUDED.capabilities, privacy_class=EXCLUDED.privacy_class, quality_rank=EXCLUDED.quality_rank`,
			ids.New(), m.Name, caps, m.PrivacyClass, m.QualityRank); err != nil {
			return err
		}
	}
	for _, d := range a.Cfg.Router.Deployments {
		var secret *uuid.UUID
		if d.APIKeyEnv != "" {
			if v := os.Getenv(d.APIKeyEnv); v != "" {
				a.Redactor.Register(v)
				var existing *uuid.UUID
				_ = a.Pool.QueryRow(ctx, `SELECT secret_id FROM deployments WHERE name=$1`, d.Name).Scan(&existing)
				if existing != nil {
					if cur, err := a.openGlobal(ctx, *existing); err == nil && cur == v {
						secret = existing
					}
				}
				if secret == nil {
					id, err := a.sealGlobal(ctx, "deployment:"+d.Name, v)
					if err != nil {
						return err
					}
					secret = &id
				}
			}
		}
		kind := d.Kind
		if kind == "" {
			kind = "openai"
		}
		engine := d.Engine
		if engine == "" {
			engine = "remote_api"
		}
		if _, err := a.Pool.Exec(ctx, `INSERT INTO deployments (id, name, model_id, provider, engine, endpoint, served_model, api_kind, secret_id, region, max_concurrency, weight, state,
				cost_per_hour_micro_eur, price_in_micro_eur_per_mtok, price_out_micro_eur_per_mtok)
			VALUES ($1,$2,(SELECT id FROM model_catalog WHERE logical_name=$3),$4,$5,$6,$7,$8,coalesce($9,(SELECT secret_id FROM deployments WHERE name=$2)),$10,$11,$12,'ready',$13,$14,$15)
			ON CONFLICT (name) DO UPDATE SET model_id=EXCLUDED.model_id, provider=EXCLUDED.provider, engine=EXCLUDED.engine, endpoint=EXCLUDED.endpoint,
				served_model=EXCLUDED.served_model, api_kind=EXCLUDED.api_kind, secret_id=coalesce($9, deployments.secret_id), region=EXCLUDED.region,
				max_concurrency=EXCLUDED.max_concurrency, weight=EXCLUDED.weight, cost_per_hour_micro_eur=EXCLUDED.cost_per_hour_micro_eur,
				price_in_micro_eur_per_mtok=EXCLUDED.price_in_micro_eur_per_mtok, price_out_micro_eur_per_mtok=EXCLUDED.price_out_micro_eur_per_mtok`,
			ids.New(), d.Name, d.Model, d.Provider, engine, d.Endpoint, d.ServedModel, kind, secret, d.Region, max(d.MaxConcurrency, 1), max(d.Weight, 0.1),
			d.CostPerHour, d.PriceInPerMTok, d.PriceOutPerMTok); err != nil {
			return err
		}
	}
	// Katalog → Router
	rows, err := a.Pool.Query(ctx, `SELECT logical_name, capabilities, privacy_class, quality_rank FROM model_catalog`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var m router.Model
		var caps []byte
		_ = rows.Scan(&m.Name, &caps, &m.PrivacyClass, &m.QualityRank)
		var c struct {
			List       []string `json:"list"`
			MaxContext int      `json:"max_context"`
			Fallbacks  []string `json:"fallbacks"`
		}
		_ = json.Unmarshal(caps, &c)
		m.Capabilities, m.MaxContext, m.Fallbacks = c.List, c.MaxContext, c.Fallbacks
		a.Router.SetModel(m)
	}
	rows.Close()
	drows, err := a.Pool.Query(ctx, `SELECT d.id, d.name, m.logical_name, d.provider, d.engine, d.endpoint, d.served_model, d.api_kind, d.secret_id, d.region,
			d.max_concurrency, d.weight, d.state, coalesce(d.cost_per_hour_micro_eur,0), coalesce(d.price_in_micro_eur_per_mtok,0), coalesce(d.price_out_micro_eur_per_mtok,0)
		FROM deployments d JOIN model_catalog m ON m.id=d.model_id WHERE d.node_id IS NULL AND d.endpoint NOT LIKE 'tunnel://%'`)
	if err != nil {
		return err
	}
	type dep struct {
		router.Deployment
		kind   string
		secret *uuid.UUID
	}
	var deps []dep
	for drows.Next() {
		var d dep
		var id uuid.UUID
		var state string
		_ = drows.Scan(&id, &d.Name, &d.Model, &d.Provider, &d.Engine, &d.Endpoint, &d.ServedModel, &d.kind, &d.secret, &d.Region,
			&d.MaxConcurrency, &d.Weight, &state, &d.CostPerHour, &d.PriceInPerMTok, &d.PriceOutPerMTok)
		d.ID, d.State = id.String(), router.DeploymentState(state)
		deps = append(deps, d)
	}
	drows.Close()
	for _, d := range deps {
		key := ""
		if d.secret != nil {
			if k, err := a.openGlobal(ctx, *d.secret); err == nil {
				key = k
				a.Redactor.Register(k)
			}
		}
		dd := d.Deployment
		dd.Client = a.clientFor(d.kind, d.Endpoint, key)
		a.Router.Upsert(&dd)
	}
	a.Log.Info("router-katalog geladen", "modelle", len(a.Router.Snapshot().Models), "deployments", len(deps))
	return nil
}

func (a *App) loadFleetPolicies(ctx context.Context) error {
	rows, err := a.Pool.Query(ctx, `SELECT name, min_nodes, max_nodes, scale_up, scale_down, daily_budget_micro_eur, hard, enabled FROM fleet_policies`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p fleet.Policy
		var up, down []byte
		_ = rows.Scan(&p.Name, &p.MinNodes, &p.MaxNodes, &up, &down, &p.DailyBudget, &p.Hard, &p.Enabled)
		var u struct {
			Up        fleet.ScaleUp `json:"up"`
			Spec      fleet.PodSpec `json:"spec"`
			Model     string        `json:"model"`
			CooldownS float64       `json:"cooldown_s"`
		}
		_ = json.Unmarshal(up, &u)
		_ = json.Unmarshal(down, &p.Down)
		p.Up, p.Spec, p.Model, p.Cooldown = u.Up, u.Spec, u.Model, time.Duration(u.CooldownS)*time.Second
		a.Fleet.SetPolicy(p)
	}
	rows.Close()
	if a.Cfg.Fleet.PodTemplate.GPUType != "" && len(a.Fleet.Policies()) == 0 {
		t := a.Cfg.Fleet.PodTemplate
		a.Fleet.SetPolicy(fleet.Policy{Name: "main", Model: defaultTiers(a.Cfg)["worker"], MinNodes: 0, MaxNodes: 2, Enabled: a.Cfg.Fleet.Enabled,
			Spec: fleet.PodSpec{GPUType: t.GPUType, GPUCount: max(t.GPUCount, 1), Image: t.Image, NetworkVolumeID: t.NetworkVolumeID, CloudType: t.CloudType, Region: t.Region}})
	}
	// Persistierte Nodes wiederherstellen.
	nrows, err := a.Pool.Query(ctx, `SELECT id, name, pool, provider, provider_ref, gpu_model, gpu_count, vram_gb, region, state, hourly_cost_micro_eur, started_at, token_hash, node_deployments
		FROM nodes WHERE state NOT IN ('gone','failed') OR (state='failed' AND started_at > now()-interval '1 day')`)
	if err != nil {
		return err
	}
	for nrows.Next() {
		var n fleet.Node
		var id uuid.UUID
		var th, deps []byte
		var st string
		_ = nrows.Scan(&id, &n.Name, &n.Pool, &n.Provider, &n.ProviderRef, &n.GPUModel, &n.GPUCount, &n.VRAMGB, &n.Region, &st, &n.HourlyCost, &n.StartedAt, &th, &deps)
		n.ID, n.State = id.String(), fleet.NodeState(st)
		_ = json.Unmarshal(deps, &n.Deployments)
		a.Fleet.Restore(n, th)
	}
	nrows.Close()
	a.Fleet.Persist = func(n fleet.Node, th []byte) {
		deps, _ := json.Marshal(n.Deployments)
		met, _ := json.Marshal(n.Metrics)
		var term any
		if !n.TerminatedAt.IsZero() {
			term = n.TerminatedAt
		}
		_, err := a.Pool.Exec(context.Background(), `INSERT INTO nodes (id, name, pool, provider, provider_ref, gpu_model, gpu_count, vram_gb, region, state, tunnel_state,
				last_heartbeat, hourly_cost_micro_eur, started_at, terminated_at, token_hash, node_deployments, metrics)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT (id) DO UPDATE SET state=EXCLUDED.state, tunnel_state=EXCLUDED.tunnel_state, last_heartbeat=EXCLUDED.last_heartbeat, gpu_model=EXCLUDED.gpu_model,
				gpu_count=EXCLUDED.gpu_count, vram_gb=EXCLUDED.vram_gb, region=EXCLUDED.region, terminated_at=EXCLUDED.terminated_at,
				token_hash=EXCLUDED.token_hash, node_deployments=EXCLUDED.node_deployments, metrics=EXCLUDED.metrics`,
			n.ID, n.Name, n.Pool, n.Provider, n.ProviderRef, n.GPUModel, n.GPUCount, n.VRAMGB, n.Region, n.State, n.TunnelState,
			nullTime(n.LastHeartbeat), n.HourlyCost, n.StartedAt, term, th, deps, met)
		if err != nil {
			a.Log.Warn("node persistieren", "err", err)
		}
	}
	return nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// AddStaticNode registriert einen lokalen Inference-Server (fyctl fleet add-node).
func (a *App) AddStaticNode(name, region string) (id, token string) {
	token = ids.New().String() + ids.New().String()
	n := a.Fleet.AddStatic(fleet.Node{Name: name, Region: region, Pool: "static"}, token)
	return n.ID, token
}

// ---- Router ↔ Flotte ----

type decisionRecorder struct{ pool *pgxpool.Pool }

func (d *decisionRecorder) RecordDecision(ctx context.Context, x router.Decision) {
	var run, dep any
	if id, err := uuid.Parse(x.RunID); err == nil {
		run = id
	}
	if id, err := uuid.Parse(x.DeploymentID); err == nil {
		dep = id
	}
	_, _ = d.pool.Exec(context.WithoutCancel(ctx), `INSERT INTO routing_decisions (run_id, tier, logical_model, deployment_id, reason, queue_wait_ms) VALUES ($1,$2,$3,$4,$5,$6)`,
		run, x.Tier, x.LogicalModel, dep, x.Reason, x.QueueWaitMS)
}

type routerHook struct {
	a     *App
	mu    sync.Mutex
	dials map[string]tunnel.DialFunc
}

func (h *routerHook) setDial(node string, d tunnel.DialFunc) {
	h.mu.Lock()
	if h.dials == nil {
		h.dials = map[string]tunnel.DialFunc{}
	}
	h.dials[node] = d
	h.mu.Unlock()
}

func depName(n *fleet.Node, d fleet.NodeDeployment) string { return n.Name + "/" + d.ServedModel }

func (h *routerHook) NodeReady(n *fleet.Node) {
	h.mu.Lock()
	dial := h.dials[n.ID]
	h.mu.Unlock()
	if dial == nil {
		if d, ok := h.a.Tunnel.Dialer(n.ID); ok {
			dial = d
		} else {
			return
		}
	}
	hc := tunnel.HTTPClient(dial, 10*time.Minute)
	provider := "runpod"
	if n.Provider == "static" {
		provider = "local"
	}
	per := n.HourlyCost
	if len(n.Deployments) > 1 {
		per /= int64(len(n.Deployments))
	}
	for _, d := range n.Deployments {
		if !h.a.Router.HasModel(d.Model) {
			h.a.Log.Warn("node meldet unbekanntes logisches modell", "node", n.Name, "model", d.Model)
			continue
		}
		name := depName(n, d)
		var id uuid.UUID
		_ = h.a.Pool.QueryRow(context.Background(), `INSERT INTO deployments (id, name, model_id, node_id, provider, engine, endpoint, served_model, region, max_concurrency, state, cost_per_hour_micro_eur)
			VALUES ($1,$2,(SELECT id FROM model_catalog WHERE logical_name=$3),$4,$5,$6,$7,$8,$9,$10,'ready',$11)
			ON CONFLICT (name) DO UPDATE SET state='ready', node_id=EXCLUDED.node_id, max_concurrency=EXCLUDED.max_concurrency RETURNING id`,
			ids.New(), name, d.Model, n.ID, provider, firstNonEmpty(d.Engine, "vllm"), "tunnel://"+n.ID, d.ServedModel, n.Region, max(d.MaxConcurrency, 1), per).Scan(&id)
		h.a.Router.Upsert(&router.Deployment{ID: id.String(), Name: name, Model: d.Model, Provider: provider, Engine: d.Engine, Endpoint: "tunnel://" + n.ID,
			ServedModel: d.ServedModel, Region: n.Region, NodeID: n.ID, MaxConcurrency: max(d.MaxConcurrency, 1), Weight: 1, State: router.Ready, CostPerHour: per,
			Client: &llm.OpenAI{BaseURL: "http://node/v1", HTTP: hc}})
	}
}

func (h *routerHook) setState(n *fleet.Node, s router.DeploymentState) {
	for _, d := range n.Deployments {
		_ = h.a.Router.SetState(depName(n, d), s)
		_, _ = h.a.Pool.Exec(context.Background(), `UPDATE deployments SET state=$2 WHERE name=$1`, depName(n, d), string(s))
	}
}

func (h *routerHook) NodeDraining(n *fleet.Node) { h.setState(n, router.Draining) }

func (h *routerHook) NodeGone(n *fleet.Node) {
	for _, d := range n.Deployments {
		h.a.Router.Remove(depName(n, d))
		_, _ = h.a.Pool.Exec(context.Background(), `UPDATE deployments SET state='stopped' WHERE name=$1`, depName(n, d))
	}
	h.a.Tunnel.Disconnect(n.ID)
}

func (h *routerHook) NodeMetrics(n *fleet.Node) {
	for _, d := range n.Deployments {
		h.a.Router.ReportNodeMetrics(depName(n, d), n.Metrics.KVCacheUtil, n.Metrics.QueuedReqs)
	}
	m := n.Metrics
	_, _ = h.a.Pool.Exec(context.Background(), `INSERT INTO node_metrics (node_id, ts, gpu_util, vram_used_mb, vram_total_mb, temp_c, power_w, running_reqs, queued_reqs, tokens_per_s, kv_cache_util)
		VALUES ($1, date_trunc('second', now()), $2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, n.ID, m.GPUUtil, m.VRAMUsedMB, m.VRAMTotalMB, m.TempC, m.PowerW, m.RunningReqs, m.QueuedReqs, m.TokensPerS, m.KVCacheUtil)
}

type tunnelHandler struct{ a *App }

func (t *tunnelHandler) Authenticate(nodeID, token string) bool {
	_, ok := t.a.Fleet.Authenticate(nodeID, token)
	return ok
}

func (t *tunnelHandler) OnRegister(nodeID string, raw json.RawMessage, dial tunnel.DialFunc) {
	var reg fleet.Registration
	_ = json.Unmarshal(raw, &reg)
	if err := t.a.Fleet.Register(nodeID, reg); err != nil {
		t.a.Log.Warn("node-registrierung", "err", err)
		return
	}
	hook := t.a.Fleet.Hook.(*routerHook)
	hook.setDial(nodeID, dial)
	go func() {
		hc := tunnel.HTTPClient(dial, 10*time.Second)
		for i := 0; i < 60; i++ {
			resp, err := hc.Get("http://node/v1/models")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					t.a.Fleet.MarkReady(nodeID)
					t.a.Log.Info("node ready", "node", nodeID)
					return
				}
			}
			time.Sleep(10 * time.Second)
		}
		t.a.Log.Warn("node-health-probe fehlgeschlagen", "node", nodeID)
	}()
}

func (t *tunnelHandler) OnHeartbeat(nodeID string, raw json.RawMessage) {
	var m fleet.Metrics
	_ = json.Unmarshal(raw, &m)
	t.a.Fleet.Heartbeat(nodeID, m)
}

func (t *tunnelHandler) OnDisconnect(nodeID string) {
	t.a.Fleet.TunnelDown(nodeID)
	if n, ok := t.a.Fleet.Node(nodeID); ok {
		t.a.Fleet.Hook.(*routerHook).setState(&n, router.Failed)
	}
}

// ---- Memory / STT / Messenger ----

type routerEmbedder struct {
	r        *router.Router
	fallback llm.Embedder
	log      *slog.Logger
	warned   sync.Once
}

func (e *routerEmbedder) Embed(ctx context.Context, model string, in []string) ([][]float32, error) {
	v, err := e.r.Embed(ctx, "embedder", in)
	if err == nil {
		return v, nil
	}
	e.warned.Do(func() { e.log.Warn("embedder nicht erreichbar – fallback auf hash-embeddings", "err", err) })
	return e.fallback.Embed(ctx, model, in)
}

type memAdapter struct{ m *memory.Service }

func (a *memAdapter) Core(ctx context.Context, dot uuid.UUID) ([]runtime.MemoryItem, error) {
	list, err := a.m.CoreMemories(ctx, dot)
	return toItems(list), err
}

func (a *memAdapter) Retrieve(ctx context.Context, dot uuid.UUID, q string) ([]runtime.MemoryItem, error) {
	list, err := a.m.Search(ctx, dot, q, memory.SearchOptions{K: 8})
	return toItems(list), err
}

func toItems(list []*memory.Memory) []runtime.MemoryItem {
	out := make([]runtime.MemoryItem, 0, len(list))
	for _, m := range list {
		out = append(out, runtime.MemoryItem{ID: m.ID.String(), Content: m.Content, Tier: string(m.Tier), Score: m.Score, Untrusted: m.Origin == memory.FromUntrusted, Date: m.CreatedAt})
	}
	return out
}

type memOps struct{ m *memory.Service }

func (o *memOps) Remember(ctx context.Context, dot uuid.UUID, text string) error {
	_, err := o.m.Write(ctx, memory.WriteRequest{DotID: dot, Tier: memory.Semantic, Content: text, Importance: 0.8, Origin: memory.FromOwner, Direct: true, Source: map[string]any{"via": "/remember"}})
	return err
}

func (o *memOps) Forget(ctx context.Context, dot uuid.UUID, topic string) (int, error) {
	return o.m.ForgetTopic(ctx, dot, topic)
}

type sttAdapter struct{ r *router.Router }

func (s *sttAdapter) Transcribe(ctx context.Context, audio []byte, mime string) (string, error) {
	return s.r.Transcribe(ctx, "stt", audio, mime)
}

type messenger struct{ a *App }

func (m *messenger) NotifyOwnerID(ctx context.Context, dot uuid.UUID, text string) error {
	d, err := m.a.Engine.Store.GetDot(ctx, dot)
	if err != nil {
		return err
	}
	m.a.Hub.NotifyOwner(ctx, d, text)
	return nil
}

func (m *messenger) SendTo(ctx context.Context, dot uuid.UUID, platform, chatID, text string) error {
	if platform != "telegram" && platform != "discord" {
		return errors.New("unbekannte plattform")
	}
	return m.a.Hub.Enqueue(ctx, dot, channels.Target{Platform: platform, ChatID: chatID}, channels.Text(text), dot.String(), uuid.Nil)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
