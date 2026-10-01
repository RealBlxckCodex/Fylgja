// Package google ist der native Google-Connector (Gmail und Kalender) per OAuth 2.0.
//
// Das Refresh-Token liegt verschlüsselt im Vault (type oauth_token). Access-Tokens leben nur
// im Speicher. Der Zugriffsmodus der Verbindung (read | read_write) wird im Tool-Handler
// zusätzlich zur Policy geprüft: Mit read kann auch eine freigegebene Aktion nichts schreiben.
package google

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
	ScopeGmailRead     = "https://www.googleapis.com/auth/gmail.readonly"
	ScopeGmailSend     = "https://www.googleapis.com/auth/gmail.send"
	ScopeCalendarRead  = "https://www.googleapis.com/auth/calendar.readonly"
	ScopeCalendarWrite = "https://www.googleapis.com/auth/calendar.events"
)

// Config enthält die OAuth-Client-Daten; die Basis-URLs sind für Tests überschreibbar.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AuthBase     string // https://accounts.google.com/o/oauth2/v2/auth
	TokenURL     string // https://oauth2.googleapis.com/token
	GmailBase    string // https://gmail.googleapis.com
	CalendarBase string // https://www.googleapis.com/calendar/v3
}

func (c *Config) defaults() {
	set := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	set(&c.AuthBase, "https://accounts.google.com/o/oauth2/v2/auth")
	set(&c.TokenURL, "https://oauth2.googleapis.com/token")
	set(&c.GmailBase, "https://gmail.googleapis.com")
	set(&c.CalendarBase, "https://www.googleapis.com/calendar/v3")
}

// Enabled: Client-ID und -Secret sind gesetzt.
func (c Config) Enabled() bool { return c.ClientID != "" && c.ClientSecret != "" }

// Scopes liefert die OAuth-Scopes für einen Zugriffsmodus.
func Scopes(mode string) []string {
	s := []string{ScopeGmailRead, ScopeCalendarRead}
	if mode == "read_write" {
		s = append(s, ScopeGmailSend, ScopeCalendarWrite)
	}
	return s
}

var (
	ErrNotConnected = errors.New("google: für diese fylgja ist kein Google-Konto verbunden")
	ErrReadOnly     = errors.New("google: die Verbindung ist schreibgeschützt (access_mode=read)")
)

// Conn ist eine Verbindung einer Fylgja zu einem Google-Konto.
type Conn struct {
	ID    uuid.UUID
	Mode  string
	Label string
	// refresh wird nur intern gehalten.
	refresh string
}

// Client spricht mit Google und verwaltet Tokens.
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
	m.Write([]byte("google-oauth-state\x00"))
	m.Write(b)
	return m.Sum(nil)
}

// AuthURL baut die Einwilligungs-URL. Der State bindet Workspace, Fylgja und Modus.
func (c *Client) AuthURL(ws, dot uuid.UUID, mode string) (string, error) {
	c.init()
	if !c.Cfg.Enabled() {
		return "", errors.New("google: nicht konfiguriert (FYLGJA_GOOGLE_CLIENT_ID/SECRET)")
	}
	if mode != "read" && mode != "read_write" {
		return "", errors.New("google: access_mode muss read oder read_write sein")
	}
	raw, _ := json.Marshal(stateData{ws, dot, mode, c.Now().Add(10 * time.Minute).Unix()})
	state := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(c.sign(raw))
	q := url.Values{
		"client_id": {c.Cfg.ClientID}, "redirect_uri": {c.Cfg.RedirectURL}, "response_type": {"code"},
		"scope": {strings.Join(Scopes(mode), " ")}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state},
		"include_granted_scopes": {"false"},
	}
	return c.Cfg.AuthBase + "?" + q.Encode(), nil
}

func (c *Client) parseState(s string) (stateData, error) {
	var st stateData
	a, b, ok := strings.Cut(s, ".")
	if !ok {
		return st, errors.New("google: state ungültig")
	}
	raw, err1 := base64.RawURLEncoding.DecodeString(a)
	sig, err2 := base64.RawURLEncoding.DecodeString(b)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, c.sign(raw)) {
		return st, errors.New("google: state ungültig")
	}
	if err := json.Unmarshal(raw, &st); err != nil || c.Now().Unix() > st.Exp {
		return st, errors.New("google: state abgelaufen")
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
		return nil, fmt.Errorf("google: token-endpunkt: %s %s", tr.Error, tr.ErrorDesc)
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
		return uuid.Nil, errors.New("google: kein refresh-token erhalten (Zugriff in den Google-Kontoeinstellungen entfernen und erneut verbinden)")
	}
	need := Scopes(st.Mode)
	granted := " " + tr.Scope + " "
	for _, s := range need {
		if !strings.Contains(granted, " "+s+" ") {
			return uuid.Nil, fmt.Errorf("google: scope %s wurde nicht gewährt", s)
		}
	}
	label := c.profile(ctx, tr.AccessToken)
	return st.Dot, c.save(ctx, st, label, tr.RefreshToken)
}

func (c *Client) profile(ctx context.Context, access string) string {
	var out struct {
		EmailAddress string `json:"emailAddress"`
	}
	if err := c.do(ctx, access, http.MethodGet, c.Cfg.GmailBase+"/gmail/v1/users/me/profile", nil, &out); err != nil {
		return "Google-Konto"
	}
	return out.EmailAddress
}

// save legt Connector, Secret und Connection an (ersetzt eine bestehende Verbindung der Fylgja).
func (c *Client) save(ctx context.Context, st stateData, label, refresh string) error {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var connector uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM connectors WHERE workspace_id=$1 AND kind='google'`, st.WS).Scan(&connector)
	if err != nil {
		connector = uuid.New()
		if _, err = tx.Exec(ctx, `INSERT INTO connectors (id, workspace_id, kind, name) VALUES ($1,$2,'google','Google')`, connector, st.WS); err != nil {
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
		VALUES ($1,$2,$3,'oauth_token',$4,$5,$6,$7,'{"allowed_domains":["googleapis.com"]}')`, sid, st.WS, st.Dot, "Google "+label, sealed.Ciphertext, sealed.WrappedDEK, sealed.KeyVersion); err != nil {
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
	_, err := c.Pool.Exec(ctx, `WITH d AS (DELETE FROM connections WHERE dot_id=$2 AND connector_id IN (SELECT id FROM connectors WHERE workspace_id=$1 AND kind='google') RETURNING secret_id)
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
		FROM connections cn JOIN connectors ct ON ct.id=cn.connector_id AND ct.kind='google' AND ct.status='active'
		JOIN vault_secrets k ON k.id=cn.secret_id WHERE cn.dot_id=$1`, dot).Scan(&cn.ID, &cn.Mode, &cn.Label, &connStatus, &ws, &sid, &ct, &dek, &kv)
	if err != nil || connStatus != "active" {
		return nil, ErrNotConnected
	}
	pt, err := c.Keyring.Open(ws, vault.Sealed{Ciphertext: ct, WrappedDEK: dek, KeyVersion: kv}, sid[:])
	if err != nil {
		return nil, err
	}
	cn.refresh = string(pt)
	return &cn, nil
}

func (c *Client) access(ctx context.Context, cn *Conn) (string, error) {
	c.mu.Lock()
	if t, ok := c.acc[cn.ID]; ok && c.Now().Before(t.exp.Add(-time.Minute)) {
		c.mu.Unlock()
		return t.tok, nil
	}
	c.mu.Unlock()
	tr, err := c.tokenCall(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {cn.refresh}})
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.acc[cn.ID] = accessToken{tr.AccessToken, c.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)}
	c.mu.Unlock()
	return tr.AccessToken, nil
}

func (c *Client) do(ctx context.Context, access, method, u string, body any, out any) error {
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
		return fmt.Errorf("google: %s %s: status %d: %s", method, resp.Request.URL.Path, resp.StatusCode, firstLine(string(b)))
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
func (c *Client) api(ctx context.Context, dot uuid.UUID, write bool, method, u string, body, out any) error {
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
	return c.do(ctx, tok, method, u, body, out)
}
