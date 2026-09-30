package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/platform/ids"
)

// Passkeys implementiert WebAuthn-Registrierung und -Login (auch als Step-up).
type Passkeys struct {
	Svc *Service
	WA  *webauthn.WebAuthn

	mu       sync.Mutex
	sessions map[string]pendingCeremony
}

type pendingCeremony struct {
	data *webauthn.SessionData
	user uuid.UUID
	exp  time.Time
}

// NewPasskeys konfiguriert WebAuthn.
func NewPasskeys(svc *Service, rpID string, origins []string) (*Passkeys, error) {
	wa, err := webauthn.New(&webauthn.Config{RPDisplayName: "Fylgja", RPID: rpID, RPOrigins: origins})
	if err != nil {
		return nil, err
	}
	return &Passkeys{Svc: svc, WA: wa, sessions: map[string]pendingCeremony{}}, nil
}

type waUser struct {
	id    uuid.UUID
	email string
	name  string
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.id[:] }
func (u *waUser) WebAuthnName() string                       { return u.email }
func (u *waUser) WebAuthnDisplayName() string                { return u.name }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (p *Passkeys) loadUser(ctx context.Context, id uuid.UUID) (*waUser, error) {
	u := &waUser{id: id}
	if err := p.Svc.Pool.QueryRow(ctx, `SELECT email, display_name FROM users WHERE id=$1`, id).Scan(&u.email, &u.name); err != nil {
		return nil, err
	}
	rows, err := p.Svc.Pool.Query(ctx, `SELECT credential FROM passkeys WHERE user_id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if json.Unmarshal(b, &c) == nil {
			u.creds = append(u.creds, c)
		}
	}
	return u, rows.Err()
}

func (p *Passkeys) put(data *webauthn.SessionData, user uuid.UUID) string {
	key := ids.New().String()
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, v := range p.sessions {
		if now.After(v.exp) {
			delete(p.sessions, k)
		}
	}
	p.sessions[key] = pendingCeremony{data: data, user: user, exp: now.Add(5 * time.Minute)}
	return key
}

func (p *Passkeys) take(key string) (pendingCeremony, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.sessions[key]
	delete(p.sessions, key)
	return c, ok && time.Now().Before(c.exp)
}

// BeginRegistration startet die Registrierung eines Passkeys für einen angemeldeten Nutzer.
func (p *Passkeys) BeginRegistration(ctx context.Context, user uuid.UUID) (*protocol.CredentialCreation, string, error) {
	u, err := p.loadUser(ctx, user)
	if err != nil {
		return nil, "", err
	}
	opts, data, err := p.WA.BeginRegistration(u, webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred))
	if err != nil {
		return nil, "", err
	}
	return opts, p.put(data, user), nil
}

// FinishRegistration speichert den Passkey.
func (p *Passkeys) FinishRegistration(ctx context.Context, key string, r *http.Request) error {
	c, ok := p.take(key)
	if !ok {
		return errors.New("auth: registrierung abgelaufen")
	}
	u, err := p.loadUser(ctx, c.user)
	if err != nil {
		return err
	}
	cred, err := p.WA.FinishRegistration(u, *c.data, r)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(cred)
	_, err = p.Svc.Pool.Exec(ctx, `INSERT INTO passkeys (id, user_id, credential) VALUES ($1,$2,$3)`, ids.New(), c.user, b)
	return err
}

// BeginLogin startet einen discoverable Login (ohne Benutzername).
func (p *Passkeys) BeginLogin() (*protocol.CredentialAssertion, string, error) {
	opts, data, err := p.WA.BeginDiscoverableLogin()
	if err != nil {
		return nil, "", err
	}
	return opts, p.put(data, uuid.Nil), nil
}

// FinishLogin prüft die Assertion und erzeugt eine Session mit Step-up.
func (p *Passkeys) FinishLogin(ctx context.Context, key string, r *http.Request) (string, *Principal, error) {
	c, ok := p.take(key)
	if !ok {
		return "", nil, errors.New("auth: anmeldung abgelaufen")
	}
	var uid uuid.UUID
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		id, err := uuid.FromBytes(userHandle)
		if err != nil {
			return nil, err
		}
		uid = id
		return p.loadUser(ctx, id)
	}
	if _, err := p.WA.FinishDiscoverableLogin(handler, *c.data, r); err != nil {
		return "", nil, ErrInvalid
	}
	return p.Svc.NewSession(ctx, uid, true)
}

// BeginStepUp startet eine Passkey-Bestätigung für einen angemeldeten Nutzer.
func (p *Passkeys) BeginStepUp(ctx context.Context, user uuid.UUID) (*protocol.CredentialAssertion, string, error) {
	u, err := p.loadUser(ctx, user)
	if err != nil {
		return nil, "", err
	}
	if len(u.creds) == 0 {
		return nil, "", errors.New("auth: kein passkey registriert")
	}
	opts, data, err := p.WA.BeginLogin(u)
	if err != nil {
		return nil, "", err
	}
	return opts, p.put(data, user), nil
}

// FinishStepUp prüft die Assertion und markiert die Session.
func (p *Passkeys) FinishStepUp(ctx context.Context, key, sessionToken string, r *http.Request) (time.Time, error) {
	c, ok := p.take(key)
	if !ok {
		return time.Time{}, errors.New("auth: step-up abgelaufen")
	}
	u, err := p.loadUser(ctx, c.user)
	if err != nil {
		return time.Time{}, err
	}
	if _, err := p.WA.FinishLogin(u, *c.data, r); err != nil {
		return time.Time{}, ErrInvalid
	}
	return p.Svc.StepUp(ctx, sessionToken)
}

// HasPasskey meldet, ob ein Nutzer Passkeys registriert hat.
func (p *Passkeys) HasPasskey(ctx context.Context, user uuid.UUID) bool {
	var n int
	_ = p.Svc.Pool.QueryRow(ctx, `SELECT count(*) FROM passkeys WHERE user_id=$1`, user).Scan(&n)
	return n > 0
}
