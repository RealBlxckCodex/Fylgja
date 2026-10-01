// Package microsoft ist der native Microsoft-365-Connector (Outlook-Mail und Kalender über Microsoft Graph, OAuth 2.0).
//
// Das Refresh-Token liegt verschlüsselt im Vault (type oauth_token). Access-Tokens leben nur
// im Speicher. Der Zugriffsmodus der Verbindung (read | read_write) wird im Tool-Handler
// zusätzlich zur Policy geprüft: Mit read kann auch eine freigegebene Aktion nichts schreiben.
package microsoft

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/vault"
)

const (
	ScopeMailRead      = "Mail.Read"
	ScopeMailSend      = "Mail.Send"
	ScopeCalendarRead  = "Calendars.Read"
	ScopeCalendarWrite = "Calendars.ReadWrite"
	scopeOffline       = "offline_access"
	scopeUser          = "User.Read"
)

// Config enthält die OAuth-Client-Daten; die Basis-URLs sind für Tests überschreibbar.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Tenant       string // "common", "organizations" oder eine Tenant-ID
	AuthBase     string // https://login.microsoftonline.com/<tenant>/oauth2/v2.0/authorize
	TokenURL     string // https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token
	GraphBase    string // https://graph.microsoft.com/v1.0
}

func (c *Config) defaults() {
	set := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	set(&c.Tenant, "common")
	set(&c.AuthBase, "https://login.microsoftonline.com/"+c.Tenant+"/oauth2/v2.0/authorize")
	set(&c.TokenURL, "https://login.microsoftonline.com/"+c.Tenant+"/oauth2/v2.0/token")
	set(&c.GraphBase, "https://graph.microsoft.com/v1.0")
}

// Enabled: Client-ID und -Secret sind gesetzt.
func (c Config) Enabled() bool { return c.ClientID != "" && c.ClientSecret != "" }

// Scopes liefert die OAuth-Scopes für einen Zugriffsmodus.
func Scopes(mode string) []string {
	s := []string{ScopeMailRead, ScopeCalendarRead}
	if mode == "read_write" {
		s = []string{ScopeMailRead, ScopeMailSend, ScopeCalendarWrite}
	}
	return s
}

var (
	ErrNotConnected = errors.New("microsoft: für diese fylgja ist kein Microsoft-Konto verbunden")
	ErrReadOnly     = errors.New("microsoft: die Verbindung ist schreibgeschützt (access_mode=read)")
)

// Conn ist eine Verbindung einer Fylgja zu einem Microsoft-Konto.
type Conn struct {
	ID    uuid.UUID
	Mode  string
	Label string
	// intern: Refresh-Token und Vault-Zuordnung (Microsoft rotiert Refresh-Tokens bei jeder Erneuerung).
	refresh string
	secret  uuid.UUID
	ws      uuid.UUID
}

// Client spricht mit Microsoft und verwaltet Tokens.
type Client struct {
	Cfg     Config
	HTTP    *http.Client
	Pool    *pgxpool.Pool
	Keyring *vault.Keyring
	// StateKey signiert den OAuth-State (CSRF-Schutz und Bindung an Workspace/Fylgja).
	StateKey []byte
	Now      func() time.Time

	once sync.Once
	mu   sync.Mutex
	acc  map[uuid.UUID]accessToken
}

type accessToken struct {
	tok string
	exp time.Time
}

func (c *Client) init() {
	c.once.Do(func() {
		c.Cfg.defaults()
		c.acc = map[uuid.UUID]accessToken{}
		if c.Now == nil {
			c.Now = time.Now
		}
	})
}

// ---- OAuth ----

type stateData struct {
	WS   uuid.UUID `json:"w"`
	Dot  uuid.UUID `json:"d"`
	Mode string    `json:"m"`
	Exp  int64     `json:"e"`
}

func (c *Client) sign(b []byte) []byte {
	m := hmac.New(sha256.New, c.StateKey)
	m.Write([]byte("microsoft-oauth-state\x00"))
	m.Write(b)
	return m.Sum(nil)
}

// AuthURL baut die Einwilligungs-URL. Der State bindet Workspace, Fylgja und Modus.
func (c *Client) AuthURL(ws, dot uuid.UUID, mode string) (string, error) {
	c.init()
	if !c.Cfg.Enabled() {
		return "", errors.New("microsoft: nicht konfiguriert (FYLGJA_MICROSOFT_CLIENT_ID/SECRET)")
	}
	if mode != "read" && mode != "read_write" {
		return "", errors.New("microsoft: access_mode muss read oder read_write sein")
	}
	raw, _ := json.Marshal(stateData{ws, dot, mode, c.Now().Add(10 * time.Minute).Unix()})
	state := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(c.sign(raw))
	q := url.Values{
		"client_id": {c.Cfg.ClientID}, "redirect_uri": {c.Cfg.RedirectURL}, "response_type": {"code"},
		"scope": {strings.Join(append(Scopes(mode), scopeOffline, scopeUser), " ")}, "prompt": {"consent"}, "state": {state}, "response_mode": {"query"},
	}
	return c.Cfg.AuthBase + "?" + q.Encode(), nil
}

func (c *Client) parseState(s string) (stateData, error) {
	var st stateData
	a, b, ok := strings.Cut(s, ".")
	if !ok {
		return st, errors.New("microsoft: state ungültig")
	}
	raw, err1 := base64.RawURLEncoding.DecodeString(a)
	sig, err2 := base64.RawURLEncoding.DecodeString(b)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, c.sign(raw)) {
		return st, errors.New("microsoft: state ungültig")
	}
	if err := json.Unmarshal(raw, &st); err != nil || c.Now().Unix() > st.Exp {
		return st, errors.New("microsoft: state abgelaufen")
	}
	return st, nil
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (c *Client) tokenCall(ctx context.Context, form url.Values) (*tokenResp, error) {
	form.Set("client_id", c.Cfg.ClientID)
	form.Set("client_secret", c.Cfg.ClientSecret)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.Cfg.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var tr tokenResp
	_ = json.Unmarshal(b, &tr)
	if resp.StatusCode != http.StatusOK || tr.Error != "" {
		return nil, fmt.Errorf("microsoft: token-endpunkt: %s %s", tr.Error, tr.ErrorDesc)
	}
	return &tr, nil
}

// HandleCallback schließt die Einwilligung ab und speichert die Verbindung.
func (c *Client) HandleCallback(ctx context.Context, code, state string) (dot uuid.UUID, err error) {
	c.init()
	st, err := c.parseState(state)
	if err != nil {
		return uuid.Nil, err
	}
	tr, err := c.tokenCall(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.Cfg.RedirectURL}})
	if err != nil {
		return uuid.Nil, err
	}
	if tr.RefreshToken == "" {
		return uuid.Nil, errors.New("microsoft: kein refresh-token erhalten (Zugriff in den Microsoft-Kontoeinstellungen entfernen und erneut verbinden)")
	}
	need := Scopes(st.Mode)
	// Microsoft liefert Scopes teils mit Ressourcen-Präfix und in anderer Schreibweise.
	granted := " " + strings.ToLower(tr.Scope) + " "
	for _, s := range need {
		l := strings.ToLower(s)
		if !strings.Contains(granted, " "+l+" ") && !strings.Contains(granted, "/"+l+" ") {
			return uuid.Nil, fmt.Errorf("microsoft: scope %s wurde nicht gewährt", s)
		}
	}
	label := c.profile(ctx, tr.AccessToken)
	return st.Dot, c.save(ctx, st, label, tr.RefreshToken)
}

func (c *Client) profile(ctx context.Context, access string) string {
	var out struct {
		Mail, UserPrincipalName string
	}
	if err := c.do(ctx, access, http.MethodGet, c.Cfg.GraphBase+"/me?$select=mail,userPrincipalName", nil, &out); err != nil {
		return "Microsoft-Konto"
	}
	if out.Mail != "" {
		return out.Mail
	}
	return out.UserPrincipalName
}

// save legt Connector, Secret und Connection an (ersetzt eine bestehende Verbindung der Fylgja).
func (c *Client) save(ctx context.Context, st stateData, label, refresh string) error {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var connector uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM connectors WHERE workspace_id=$1 AND kind='microsoft'`, st.WS).Scan(&connector)
	if err != nil {
		connector = uuid.New()
		if _, err = tx.Exec(ctx, `INSERT INTO connectors (id, workspace_id, kind, name) VALUES ($1,$2,'microsoft','Microsoft')`, connector, st.WS); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM vault_secrets WHERE id IN (SELECT secret_id FROM connections WHERE connector_id=$1 AND dot_id=$2)`, connector, st.Dot); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM connections WHERE connector_id=$1 AND dot_id=$2`, connector, st.Dot); err != nil {
		return err
	}
	sid := uuid.New()
	sealed, err := c.Keyring.Seal(st.WS, []byte(refresh), sid[:])
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO vault_secrets (id, workspace_id, dot_id, type, label, ciphertext, wrapped_dek, key_version, meta)
		VALUES ($1,$2,$3,'oauth_token',$4,$5,$6,$7,'{"allowed_domains":["graph.microsoft.com"]}')`, sid, st.WS, st.Dot, "Microsoft "+label, sealed.Ciphertext, sealed.WrappedDEK, sealed.KeyVersion); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO connections (connector_id, dot_id, account_label, scopes, access_mode, secret_id) VALUES ($1,$2,$3,$4,$5,$6)`,
		connector, st.Dot, label, Scopes(st.Mode), st.Mode, sid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Disconnect entfernt die Verbindung und das Secret.
func (c *Client) Disconnect(ctx context.Context, ws, dot uuid.UUID) error {
	_, err := c.Pool.Exec(ctx, `WITH d AS (DELETE FROM connections WHERE dot_id=$2 AND connector_id IN (SELECT id FROM connectors WHERE workspace_id=$1 AND kind='microsoft') RETURNING secret_id)
		DELETE FROM vault_secrets WHERE id IN (SELECT secret_id FROM d)`, ws, dot)
	return err
}

// Conn lädt die aktive Verbindung einer Fylgja.
func (c *Client) conn(ctx context.Context, dot uuid.UUID) (*Conn, error) {
	var (
		cn         Conn
		ws         uuid.UUID
		sid        uuid.UUID
		ct, dek    []byte
		kv         int
		connStatus string
	)
	err := c.Pool.QueryRow(ctx, `SELECT cn.id, cn.access_mode, cn.account_label, cn.status, k.workspace_id, k.id, k.ciphertext, k.wrapped_dek, k.key_version
		FROM connections cn JOIN connectors ct ON ct.id=cn.connector_id AND ct.kind='microsoft' AND ct.status='active'
		JOIN vault_secrets k ON k.id=cn.secret_id WHERE cn.dot_id=$1`, dot).Scan(&cn.ID, &cn.Mode, &cn.Label, &connStatus, &ws, &sid, &ct, &dek, &kv)
	if err != nil || connStatus != "active" {
		return nil, ErrNotConnected
	}
	pt, err := c.Keyring.Open(ws, vault.Sealed{Ciphertext: ct, WrappedDEK: dek, KeyVersion: kv}, sid[:])
	if err != nil {
		return nil, err
	}
	cn.refresh, cn.secret, cn.ws = string(pt), sid, ws
	return &cn, nil
}

func (c *Client) access(ctx context.Context, cn *Conn) (string, error) {
	c.mu.Lock()
	if t, ok := c.acc[cn.ID]; ok && c.Now().Before(t.exp.Add(-time.Minute)) {
		c.mu.Unlock()
		return t.tok, nil
	}
	c.mu.Unlock()
	tr, err := c.tokenCall(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {cn.refresh}, "scope": {strings.Join(append(Scopes(cn.Mode), scopeOffline), " ")}})
	if err != nil {
		return "", err
	}
	if tr.RefreshToken != "" && tr.RefreshToken != cn.refresh {
		if err := c.rotate(ctx, cn, tr.RefreshToken); err != nil {
			return "", fmt.Errorf("microsoft: neues refresh-token konnte nicht gespeichert werden: %w", err)
		}
	}
	c.mu.Lock()
	c.acc[cn.ID] = accessToken{tr.AccessToken, c.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)}
	c.mu.Unlock()
	return tr.AccessToken, nil
}

// rotate ersetzt das gespeicherte Refresh-Token durch das neu ausgegebene.
func (c *Client) rotate(ctx context.Context, cn *Conn, refresh string) error {
	sealed, err := c.Keyring.Seal(cn.ws, []byte(refresh), cn.secret[:])
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `UPDATE vault_secrets SET ciphertext=$2, wrapped_dek=$3, key_version=$4, rotated_at=now() WHERE id=$1`, cn.secret, sealed.Ciphertext, sealed.WrappedDEK, sealed.KeyVersion)
	if err == nil {
		cn.refresh = refresh
	}
	return err
}

func (c *Client) do(ctx context.Context, access, method, u string, body any, out any, hdr ...string) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("microsoft: %s %s: status %d: %s", method, resp.Request.URL.Path, resp.StatusCode, firstLine(string(b)))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// api führt einen Aufruf für eine Fylgja aus; write verlangt access_mode=read_write.
func (c *Client) api(ctx context.Context, dot uuid.UUID, write bool, method, u string, body, out any, hdr ...string) error {
	c.init()
	cn, err := c.conn(ctx, dot)
	if err != nil {
		return err
	}
	if write && cn.Mode != "read_write" {
		return ErrReadOnly
	}
	tok, err := c.access(ctx, cn)
	if err != nil {
		return err
	}
	return c.do(ctx, tok, method, u, body, out, hdr...)
}
