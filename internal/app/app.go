// Package app verdrahtet alle Subsysteme zu einem Prozess (`fylgja serve`, Spec 4.2).
package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/api"
	"github.com/realblxckcodex/fylgja/internal/audit"
	"github.com/realblxckcodex/fylgja/internal/auth"
	"github.com/realblxckcodex/fylgja/internal/channels"
	"github.com/realblxckcodex/fylgja/internal/channels/discord"
	"github.com/realblxckcodex/fylgja/internal/channels/telegram"
	"github.com/realblxckcodex/fylgja/internal/connectors/google"
	"github.com/realblxckcodex/fylgja/internal/connectors/microsoft"
	"github.com/realblxckcodex/fylgja/internal/coord"
	"github.com/realblxckcodex/fylgja/internal/events"
	"github.com/realblxckcodex/fylgja/internal/fleet"
	"github.com/realblxckcodex/fylgja/internal/fleet/pki"
	"github.com/realblxckcodex/fylgja/internal/fleet/tunnel"
	"github.com/realblxckcodex/fylgja/internal/learn"
	"github.com/realblxckcodex/fylgja/internal/link"
	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/mcp"
	"github.com/realblxckcodex/fylgja/internal/memory"
	"github.com/realblxckcodex/fylgja/internal/platform/config"
	"github.com/realblxckcodex/fylgja/internal/platform/netguard"
	"github.com/realblxckcodex/fylgja/internal/pulse"
	"github.com/realblxckcodex/fylgja/internal/router"
	"github.com/realblxckcodex/fylgja/internal/runtime"
	"github.com/realblxckcodex/fylgja/internal/sandbox"
	"github.com/realblxckcodex/fylgja/internal/skills"
	"github.com/realblxckcodex/fylgja/internal/store"
	"github.com/realblxckcodex/fylgja/internal/tools"
	"github.com/realblxckcodex/fylgja/internal/tools/builtin"
	"github.com/realblxckcodex/fylgja/internal/vault"
)

// App hält alle Komponenten.
type App struct {
	Cfg       config.Config
	Log       *slog.Logger
	Pool      *pgxpool.Pool
	Keyring   *vault.Keyring
	Redactor  *vault.Redactor
	Audit     *audit.PG
	Router    *router.Router
	Fleet     *fleet.Manager
	Tunnel    *tunnel.Server
	NodeCA    *pki.CA
	Google    *google.Client
	Microsoft *microsoft.Client
	Tools     *tools.Registry
	Memory    *memory.Service
	Engine    *runtime.Engine
	Hub       *channels.Hub
	Bus       *events.Bus
	Pulse     *pulse.Engine
	Coord     *coord.Coordinator
	Learner   *learn.Learner
	Sandbox   *sandbox.Manager
	Links     *link.Registry
	Auth      *auth.Service
	Passkeys  *auth.Passkeys
	API       *api.Server
	master    []byte
	mcps      []*mcp.Client
}

// derive leitet zweckgebundene Schlüssel aus dem Master-Key ab.
func derive(master []byte, purpose string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte("fylgja/" + purpose))
	return m.Sum(nil)
}

// New baut die App (migriert die DB und lädt Katalog/Deployments).
func New(ctx context.Context, cfg config.Config, log *slog.Logger, ui http.Handler, version string) (*App, error) {
	a := &App{Cfg: cfg, Log: log}
	if err := store.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return nil, fmt.Errorf("migration: %w", err)
	}
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	a.Pool = pool
	master, err := vault.ParseMasterKey(cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	a.master = master
	a.Keyring = vault.NewKeyring(master)
	a.Redactor = vault.NewRedactor()
	for _, s := range []string{cfg.Microsoft.ClientSecret, cfg.Google.ClientSecret, cfg.Channels.Telegram.Token, cfg.Channels.Discord.Token, cfg.Fleet.RunPodAPIKey, cfg.Router.ExternalToken, cfg.MasterKey} {
		a.Redactor.Register(s)
	}
	a.Audit = &audit.PG{Pool: pool}
	a.Bus = &events.Bus{Pool: pool, Log: log}

	// ---- Router & Flotte ----
	a.Router = router.New(router.DefaultConfig(), nil, &decisionRecorder{pool: pool})
	if err := a.loadCatalog(ctx); err != nil {
		return nil, err
	}
	var prov fleet.Provider = &fleet.Static{}
	if cfg.Fleet.Enabled && cfg.Fleet.RunPodAPIKey != "" {
		prov = &fleet.RunPod{BaseURL: cfg.Fleet.RunPodAPIBase, APIKey: cfg.Fleet.RunPodAPIKey}
	}
	a.Fleet = fleet.NewManager(prov, &routerHook{a: a}, a.Router, nil, log)
	a.Fleet.RouterURL = strings.Replace(strings.Replace(cfg.BaseURL, "https://", "wss://", 1), "http://", "ws://", 1) + "/api/v1/node/tunnel"
	a.Fleet.MTLS = cfg.Fleet.RequireMTLS
	a.Fleet.OnAlert = a.onFleetAlert
	a.Fleet.OnEvent = func(kind string, detail map[string]any) {
		_ = a.Audit.Log(context.Background(), audit.Entry{Actor: "system:fleet", Action: kind, Detail: detail})
	}
	nodeCA, err := pki.NewCA(master)
	if err != nil {
		return nil, err
	}
	a.NodeCA = nodeCA
	a.Tunnel = &tunnel.Server{H: &tunnelHandler{a: a}, Log: log, CA: nodeCA, RequireMTLS: cfg.Fleet.RequireMTLS}
	if err := a.loadFleetPolicies(ctx); err != nil {
		log.Warn("flotten-policies", "err", err)
	}

	// ---- Tools, Memory, Sandbox ----
	guard := &netguard.Guard{}
	httpc := guard.Client(30 * time.Second)
	a.Tools = tools.NewRegistry()
	runtime.RegisterEngineTools(a.Tools)
	builtin.Register(a.Tools)
	var emb llm.Embedder = llm.HashEmbedder{Dim: 1024}
	if a.Router.HasModel("embedder") {
		emb = &routerEmbedder{r: a.Router, fallback: llm.HashEmbedder{Dim: 1024}, log: log}
	} else {
		log.Warn("kein logisches modell 'embedder' konfiguriert – nutze Hash-Embeddings (schwächere semantische Suche)")
	}
	a.Memory = &memory.Service{Pool: pool, Embedder: emb, Model: "embedder", Redactor: a.Redactor}
	a.Sandbox = &sandbox.Manager{Pool: pool, Keyring: a.Keyring, Image: cfg.Sandbox.Image, IdleSleep: cfg.Sandbox.IdleSleep, Log: log, Credentials: a.credential}
	switch cfg.Sandbox.Provider {
	case "docker-gvisor", "docker":
		rt := "runsc"
		if cfg.Sandbox.Provider == "docker" {
			rt = ""
		}
		host := ""
		if len(cfg.Sandbox.Hosts) > 0 {
			host = cfg.Sandbox.Hosts[0].Endpoint
		}
		a.Sandbox.Provider = &sandbox.Docker{Host: host, Runtime: rt, Network: envOr("FYLGJA_SANDBOX_NETWORK", "fylgja-sandbox"), EgressProxy: envOr("FYLGJA_EGRESS_PROXY", "http://egressd:3128")}
	case "firecracker":
		fc := cfg.Sandbox.Firecracker
		a.Sandbox.Provider = &sandbox.Firecracker{Binary: fc.Binary, Kernel: fc.Kernel, Rootfs: fc.Rootfs, DataDir: fc.DataDir, HomeMB: fc.HomeMB, Subnet: fc.Subnet}
	case "static":
		if len(cfg.Sandbox.Hosts) > 0 {
			a.Sandbox.Provider = &sandbox.Static{Endpoint: cfg.Sandbox.Hosts[0].Endpoint, Token: os.Getenv("FYLGJA_COMPUTERD_TOKEN"), VNC: os.Getenv("FYLGJA_COMPUTERD_VNC")}
		}
	}

	// ---- Runtime ----
	a.Engine = &runtime.Engine{
		Store: &runtime.Store{Pool: pool}, LLM: a.Router, Tools: a.Tools, Redactor: a.Redactor, Audit: a.Audit, Pub: a.Bus, Log: log,
		Lanes: runtime.NewLanes(cfg.Runtime.MaxParallelRunsPerDot), MaxSteps: cfg.Runtime.MaxSteps, ToolTimeout: cfg.Runtime.ToolTimeout,
		DefaultTiers: defaultTiers(cfg), Memory: &memAdapter{m: a.Memory},
	}
	a.Engine.Reviewer = &runtime.LLMReviewer{LLM: a.Router, Model: a.Engine.DefaultTiers["reviewer"]}

	// ---- Kanäle ----
	a.Hub = &channels.Hub{Pool: pool, Engine: a.Engine, Signer: channels.Signer{Key: derive(master, "callbacks")}, Audit: a.Audit, Log: log,
		BaseURL: cfg.BaseURL, Memory: &memOps{m: a.Memory}}
	if a.Router.HasModel("stt") {
		a.Hub.STT = &sttAdapter{r: a.Router}
	}
	a.Engine.Out = a.Hub

	a.Tools.MustRegister(&tools.Tool{Name: "computer.status", Class: "read", Base: false, Source: "builtin", Idempotent: true,
		Description: "Zeigt, ob der eigene Computer verfügbar ist.", Schema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			if a.Sandbox.Provider == nil {
				return tools.Result{Content: "kein computer konfiguriert"}, nil
			}
			return tools.Result{Content: "computer verfügbar (" + a.Sandbox.Provider.Name() + ")"}, nil
		}})
	var computer builtin.Computer
	if a.Sandbox.Provider != nil {
		computer = a.Sandbox
	}
	a.Engine.Services = &builtin.Services{Pool: pool, Memory: a.Memory, Messenger: &messenger{a: a}, Computer: computer, HTTP: httpc,
		SearxURL: os.Getenv("FYLGJA_SEARXNG_URL")}

	// ---- Koordination, Pulse, Lernen ----
	a.Coord = &coord.Coordinator{Store: &coord.Store{Pool: pool}, Exec: &coord.RunExecutor{Pool: pool, Runtime: a.Engine},
		Verify: &coord.LLMVerifier{LLM: a.Router, Model: a.Engine.DefaultTiers["reviewer"]}, Log: log,
		Workspace: func(ctx context.Context, lead string) (uuid.UUID, error) {
			var ws uuid.UUID
			err := pool.QueryRow(ctx, `SELECT workspace_id FROM dots WHERE id=$1`, lead).Scan(&ws)
			return ws, err
		},
		Publish:  a.Bus.Publish,
		Escalate: a.escalate,
	}
	coord.RegisterTools(a.Tools, a.Coord, pool, 2)
	a.Links = &link.Registry{Pool: pool}
	a.Links.RegisterTools(a.Tools)
	a.Pulse = &pulse.Engine{Pool: pool, Runtime: a.Engine, Memory: a.Memory, Notify: a.Hub, HTTP: httpc, Log: log}
	a.Learner = &learn.Learner{Pool: pool, LLM: a.Router, Memory: a.Memory, Runtime: a.Engine, Log: log, TriageModel: a.Engine.DefaultTiers["triage"]}
	a.Engine.OnFinish = func(ctx context.Context, run *runtime.Run, final string) {
		a.Pulse.AfterRun(ctx, run)
		a.Coord.OnRunFinished(ctx, run, a.Engine)
		if cfg.Features["memory_extraction"] || cfg.Features == nil || !hasKey(cfg.Features, "memory_extraction") {
			go a.Learner.Extract(context.WithoutCancel(ctx), run, final)
		}
	}
	a.Engine.OnFail = func(ctx context.Context, run *runtime.Run, reason string) { a.Coord.OnRunFailed(ctx, run, reason) }

	// ---- Auth & API ----
	a.Auth = &auth.Service{Pool: pool, SessionTTL: cfg.Auth.SessionTTL}
	if pk, err := auth.NewPasskeys(a.Auth, cfg.Auth.WebAuthnRPID, cfg.Auth.WebAuthnOrigins); err == nil {
		a.Passkeys = pk
	} else {
		log.Warn("passkeys deaktiviert", "err", err)
	}
	if cfg.Google.ClientID != "" && cfg.Google.ClientSecret != "" {
		a.Google = &google.Client{
			Cfg:  google.Config{ClientID: cfg.Google.ClientID, ClientSecret: cfg.Google.ClientSecret, RedirectURL: strings.TrimRight(cfg.BaseURL, "/") + "/api/v1/connectors/google/callback"},
			HTTP: guard.Client(30 * time.Second), Pool: pool, Keyring: a.Keyring, StateKey: derive(master, "google-state"),
		}
		google.Register(a.Tools, a.Google)
	}
	if cfg.Microsoft.ClientID != "" && cfg.Microsoft.ClientSecret != "" {
		a.Microsoft = &microsoft.Client{
			Cfg:  microsoft.Config{ClientID: cfg.Microsoft.ClientID, ClientSecret: cfg.Microsoft.ClientSecret, Tenant: cfg.Microsoft.Tenant, RedirectURL: strings.TrimRight(cfg.BaseURL, "/") + "/api/v1/connectors/microsoft/callback"},
			HTTP: guard.Client(30 * time.Second), Pool: pool, Keyring: a.Keyring, StateKey: derive(master, "microsoft-state"),
		}
		microsoft.Register(a.Tools, a.Microsoft)
	}
	var skillReg *skills.Registry
	if len(cfg.Skills.Registries) > 0 {
		trust, err := skills.ParseTrust(cfg.Skills.TrustedPublishers)
		if err != nil {
			return nil, err
		}
		skillReg = &skills.Registry{HTTP: guard.Client(20 * time.Second), URLs: cfg.Skills.Registries, Trust: trust}
	}
	a.API = &api.Server{SkillRegistry: skillReg, Voice: api.Voice{Router: a.Router}, TelegramToken: tgMiniAppToken(cfg), Google: a.Google, Microsoft: a.Microsoft, Pool: pool, Auth: a.Auth, Passkeys: a.Passkeys, Runtime: a.Engine, Memory: a.Memory, Hub: a.Hub, Bus: a.Bus,
		Router: a.Router, Fleet: a.Fleet, Tunnel: a.Tunnel, NodeCA: a.NodeCA, Links: a.Links, Coord: a.Coord, Pulse: a.Pulse, Tools: a.Tools, Sandbox: a.Sandbox, Audit: a.Audit,
		Keyring: a.Keyring, Redactor: a.Redactor, Log: log, UI: ui, BaseURL: cfg.BaseURL, Secure: strings.HasPrefix(cfg.BaseURL, "https://"),
		RouterToken: cfg.Router.ExternalToken, HookKey: derive(master, "hooks"), SkillKey: derive(master, "skills"), Version: version}
	return a, nil
}

func hasKey(m map[string]bool, k string) bool { _, ok := m[k]; return ok }

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func defaultTiers(cfg config.Config) map[string]string {
	t := map[string]string{"planner": "planner-default", "worker": "worker-default", "triage": "triage-fast", "reviewer": "reviewer", "vision": "vision", "embedder": "embedder", "stt": "stt", "tts": "tts"}
	for k, v := range cfg.Router.Tiers {
		t[k] = v
	}
	return t
}

// Run startet alle Hintergrundprozesse gemäß Rolle und blockiert bis ctx endet.
func (a *App) Run(ctx context.Context) error {
	role := a.Cfg.Role
	has := func(r string) bool { return role == "all" || role == r }
	var wg sync.WaitGroup
	gof := func(f func(context.Context)) {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	gof(a.Bus.Run)
	if has("worker") || has("api") {
		// Unterbrochene Runs fortsetzen (8.8) und aktive Graphen weitertreiben.
		if n, err := a.Engine.ResumeAll(ctx); err == nil && n > 0 {
			a.Log.Info("runs fortgesetzt", "anzahl", n)
		}
		gof(a.coordLoop)
		gof(func(ctx context.Context) { a.Hub.RunOutbox(ctx) })
		gof(a.sandboxReaper)
		gof(a.Learner.RunNightly)
		gof(a.skillVerifier)
		a.connectMCP(ctx)
	}
	if has("scheduler") {
		gof(a.Pulse.Run)
	}
	if has("router") || role == "all" {
		gof(func(ctx context.Context) { a.Fleet.Run(ctx, a.Cfg.Fleet.EvalInterval) })
	}
	if has("channels") {
		gof(a.runChannels)
	}
	gof(a.defaultDotLoop)
	var srvErr error
	if has("api") {
		srv := &http.Server{Addr: a.Cfg.Listen, Handler: a.API.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
		a.Log.Info("fylgja lauscht", "addr", a.Cfg.Listen, "role", role)
		var serve func() error = srv.ListenAndServe
		if a.Cfg.TLS.CertFile != "" {
			srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ClientCAs: a.NodeCA.Pool(), ClientAuth: tls.VerifyClientCertIfGiven}
			serve = func() error { return srv.ListenAndServeTLS(a.Cfg.TLS.CertFile, a.Cfg.TLS.KeyFile) }
		}
		if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr = err
		}
	} else {
		<-ctx.Done()
	}
	wg.Wait()
	for _, c := range a.mcps {
		_ = c.Close()
	}
	a.Pool.Close()
	return srvErr
}

// runChannels startet Kanäle mit Leader-Election (Advisory-Lock), damit pro Bot-Token nur ein Prozess pollt.
func (a *App) runChannels(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := a.Pool.Acquire(ctx)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		var got bool
		_ = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(4655)`).Scan(&got)
		if !got {
			conn.Release()
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		a.Log.Info("kanäle: leader")
		var wg sync.WaitGroup
		if a.Cfg.Channels.Telegram.Enabled {
			bot := &telegram.Bot{MiniAppURL: miniAppURL(a.Cfg), Token: a.Cfg.Channels.Telegram.Token, APIBase: a.Cfg.Channels.Telegram.APIBase, Mode: a.Cfg.Channels.Telegram.Mode,
				WebhookSecret: a.Cfg.Channels.Telegram.WebhookSecret, Log: a.Log}
			wg.Add(1)
			go func() { defer wg.Done(); a.Hub.Run(ctx, bot, uuid.Nil) }()
		}
		if a.Cfg.Channels.Discord.Enabled {
			bot := &discord.Bot{Token: a.Cfg.Channels.Discord.Token, MessageContent: a.Cfg.Channels.Discord.MessageContent, Log: a.Log}
			wg.Add(1)
			go func() { defer wg.Done(); a.Hub.Run(ctx, bot, uuid.Nil) }()
		}
		wg.Wait()
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(4655)`)
		conn.Release()
		return
	}
}

// defaultDotLoop bestimmt die Fylgja für Shared-Bots (erste persönliche Fylgja).
func (a *App) defaultDotLoop(ctx context.Context) {
	for {
		if a.Hub.DefaultDotID() == uuid.Nil {
			var id uuid.UUID
			if err := a.Pool.QueryRow(ctx, `SELECT id FROM dots WHERE status='active' ORDER BY kind='personal' DESC, created_at LIMIT 1`).Scan(&id); err == nil {
				a.Hub.SetDefaultDot(id)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}

func (a *App) coordLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rows, err := a.Pool.Query(ctx, `SELECT id FROM work_graphs WHERE status='running'`)
			if err != nil {
				continue
			}
			var ids []string
			for rows.Next() {
				var id uuid.UUID
				_ = rows.Scan(&id)
				ids = append(ids, id.String())
			}
			rows.Close()
			for _, id := range ids {
				if err := a.Coord.Tick(ctx, id); err != nil {
					a.Log.Warn("coord tick", "graph", id, "err", err)
				}
			}
		}
	}
}

func (a *App) sandboxReaper(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Sandbox.Reap(ctx)
		}
	}
}

func (a *App) skillVerifier(ctx context.Context) {
	st := &skills.Store{Pool: a.Pool, Master: derive(a.master, "skills")}
	for {
		if n, err := st.VerifyAll(ctx); err == nil && n > 0 {
			a.Log.Warn("skills deaktiviert (signatur ungültig)", "anzahl", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}

// connectMCP verbindet konfigurierte MCP-Server (FYLGJA_MCP_SERVERS als JSON-Liste).
func (a *App) connectMCP(ctx context.Context) {
	raw := os.Getenv("FYLGJA_MCP_SERVERS")
	if raw == "" {
		return
	}
	var list []mcp.ServerConfig
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		a.Log.Warn("FYLGJA_MCP_SERVERS ungültig", "err", err)
		return
	}
	for _, sc := range list {
		c, err := mcp.Connect(ctx, sc)
		if err != nil {
			a.Log.Warn("mcp-server nicht erreichbar", "name", sc.Name, "err", err)
			continue
		}
		n, err := mcp.Register(ctx, a.Tools, c)
		if err != nil {
			a.Log.Warn("mcp-tools", "name", sc.Name, "err", err)
			continue
		}
		a.mcps = append(a.mcps, c)
		a.Log.Info("mcp-server verbunden", "name", sc.Name, "tools", n)
	}
}

// escalate weckt den Lead ereignisgetrieben bzw. informiert den Owner (17.4).
func (a *App) escalate(ctx context.Context, g *coord.Graph, n *coord.Node, kind string) {
	lead, err := uuid.Parse(g.LeadDotID)
	if err != nil {
		return
	}
	dot, err := a.Engine.Store.GetDot(ctx, lead)
	if err != nil {
		return
	}
	switch kind {
	case "graph_done":
		var sb strings.Builder
		fmt.Fprintf(&sb, "Alle Knoten des Plans %q sind erledigt. Integriere die Ergebnisse zu einer Zusammenfassung für den Owner (mit Quellen/Artefakten). Ergebnisse:\n", g.Title)
		for _, x := range g.Nodes {
			if len(x.Result) > 0 {
				sb.WriteString("- " + x.Title + ": " + runtime.WrapUntrusted("node:"+x.ID, "result", string(x.Result)) + "\n")
			}
		}
		run := &runtime.Run{DotID: lead, Kind: runtime.KindTaskStep, Tier: "planner", Input: runtime.Input{Text: sb.String(), Trust: "system", MaxSteps: 15}}
		_ = a.Engine.Submit(ctx, run)
	case "failed", "verification_failed", "stalled", "graph_stuck", "blocked":
		title := g.Title
		if n != nil {
			title = n.Title + " (" + g.Title + ")"
		}
		a.Hub.NotifyOwner(ctx, dot, fmt.Sprintf("⚠️ %s: Knoten „%s“ braucht eine Entscheidung (%s). %s/teams/%s", dot.Name, title, kind, a.Cfg.BaseURL, g.ID))
	}
}

func (a *App) onFleetAlert(al fleet.Alert) {
	ctx := context.Background()
	b, _ := json.Marshal(al)
	a.Bus.Publish(ctx, "alerts", json.RawMessage(b))
	// Owner aller Workspaces mit aktiven Fylgjur informieren (Single-Tenant-Betrieb üblich).
	rows, err := a.Pool.Query(ctx, `SELECT DISTINCT ON (workspace_id) id FROM dots WHERE status='active' AND kind='personal' ORDER BY workspace_id, created_at`)
	if err != nil {
		return
	}
	var dots []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		_ = rows.Scan(&id)
		dots = append(dots, id)
	}
	rows.Close()
	for _, id := range dots {
		if d, err := a.Engine.Store.GetDot(ctx, id); err == nil {
			a.Hub.NotifyOwner(ctx, d, "🖥️ Flotte: "+al.Message)
		}
	}
}

// credential ist der Broker für browser.login: entschlüsselt ein Secret nur für computerd (13.8).
func (a *App) credential(ctx context.Context, dot, id uuid.UUID) (sandbox.Credential, error) {
	var ws uuid.UUID
	var sdot *uuid.UUID
	var ct, dek []byte
	var kv int
	var meta []byte
	var typ string
	err := a.Pool.QueryRow(ctx, `SELECT workspace_id, dot_id, ciphertext, wrapped_dek, key_version, meta, type FROM vault_secrets WHERE id=$1`, id).
		Scan(&ws, &sdot, &ct, &dek, &kv, &meta, &typ)
	if err != nil {
		return sandbox.Credential{}, errors.New("credential nicht gefunden")
	}
	if sdot != nil && *sdot != dot {
		return sandbox.Credential{}, errors.New("credential gehört einer anderen fylgja")
	}
	if typ != "password" {
		return sandbox.Credential{}, errors.New("credential ist kein passwort-zugang")
	}
	pt, err := a.Keyring.Open(ws, vault.Sealed{Ciphertext: ct, WrappedDEK: dek, KeyVersion: kv}, id[:])
	if err != nil {
		return sandbox.Credential{}, err
	}
	var c sandbox.Credential
	if err := json.Unmarshal(pt, &c); err != nil {
		return sandbox.Credential{}, err
	}
	var m struct {
		AllowedDomains []string `json:"allowed_domains"`
	}
	_ = json.Unmarshal(meta, &m)
	c.AllowedDomains = m.AllowedDomains
	a.Redactor.Register(c.Password)
	a.Redactor.Register(c.TOTPSecret)
	_ = a.Audit.Log(ctx, audit.Entry{WorkspaceID: ws, Actor: "dot:" + dot.String(), Action: "vault.use", Target: id.String(), Detail: map[string]any{"purpose": "browser.login"}})
	return c, nil
}

// tgMiniAppToken: Die Mini App ist nur aktiv, wenn der Bot läuft und die Instanz per https erreichbar ist.
func tgMiniAppToken(cfg config.Config) string {
	if cfg.Channels.Telegram.Enabled && strings.HasPrefix(cfg.BaseURL, "https://") {
		return cfg.Channels.Telegram.Token
	}
	return ""
}

func miniAppURL(cfg config.Config) string {
	if tgMiniAppToken(cfg) == "" {
		return ""
	}
	return strings.TrimRight(cfg.BaseURL, "/") + "/tg"
}
