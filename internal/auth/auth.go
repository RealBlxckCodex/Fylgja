// Package auth: Passwörter (argon2id), Sessions, API-Tokens, Passkeys (WebAuthn), Step-up (Spec 19.2, 14.8).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"

	"github.com/realblxckcodex/fylgja/internal/platform/ids"
)

var (
	ErrInvalid = errors.New("auth: ungültige anmeldedaten")
	ErrExpired = errors.New("auth: sitzung abgelaufen")
)

// argon2id-Parameter (OWASP-Empfehlung: m=64 MiB, t=3, p=2).
const (
	aTime    = 3
	aMemory  = 64 * 1024
	aThreads = 2
	aKeyLen  = 32
)

// HashPassword erzeugt einen PHC-String.
func HashPassword(pw string) (string, error) {
	if len(pw) < 12 {
		return "", errors.New("auth: passwort muss mindestens 12 zeichen haben")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	k := argon2.IDKey([]byte(pw), salt, aTime, aMemory, aThreads, aKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", aMemory, aTime, aThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(k)), nil
}

// VerifyPassword prüft in konstanter Zeit.
func VerifyPassword(hash, pw string) bool {
	p := strings.Split(hash, "$")
	if len(p) != 6 || p[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var par uint8
	if _, err := fmt.Sscanf(p[3], "m=%d,t=%d,p=%d", &m, &t, &par); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(p[4])
	want, err2 := base64.RawStdEncoding.DecodeString(p[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash gleicht Laufzeiten für unbekannte Nutzer an (Timing-Schutz).
var dummyHash, _ = HashPassword("dummy-password-for-timing")

func randToken(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

// Principal ist ein authentifizierter Nutzer im Kontext eines Workspaces.
type Principal struct {
	UserID      uuid.UUID `json:"user_id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	StepUpUntil time.Time `json:"step_up_until,omitzero"`
	Scopes      []string  `json:"scopes,omitempty"` // bei API-Tokens
	ViaToken    bool      `json:"via_token"`
}

// SteppedUp meldet eine frische Step-up-Authentifizierung (kritische Aktionen).
func (p *Principal) SteppedUp(now time.Time) bool { return now.Before(p.StepUpUntil) }

// Can prüft Rollenrechte (15.1).
func (p *Principal) Can(action string) bool {
	rank := map[string]int{"viewer": 1, "auditor": 1, "member": 2, "admin": 3, "owner": 4}[p.Role]
	switch action {
	case "read":
		return rank >= 1
	case "audit":
		return p.Role == "auditor" || rank >= 3
	case "work": // chatten, eigene Approvals
		return rank >= 2
	case "manage": // Fylgjur, Connectors, Regeln
		return rank >= 3
	case "own": // Budgets, Löschen, Approver-Gruppen
		return rank >= 4
	}
	return false
}

// Service kapselt Auth-Operationen.
type Service struct {
	Pool       *pgxpool.Pool
	SessionTTL time.Duration
	StepUpTTL  time.Duration
}

func (s *Service) ttl() time.Duration {
	if s.SessionTTL == 0 {
		return 14 * 24 * time.Hour
	}
	return s.SessionTTL
}

func (s *Service) stepTTL() time.Duration {
	if s.StepUpTTL == 0 {
		return 5 * time.Minute
	}
	return s.StepUpTTL
}

// CreateUser legt einen Nutzer an (optional mit Passwort) und fügt ihn dem Workspace hinzu.
func (s *Service) CreateUser(ctx context.Context, ws uuid.UUID, email, name, password, role string) (uuid.UUID, error) {
	var hash *string
	if password != "" {
		h, err := HashPassword(password)
		if err != nil {
			return uuid.Nil, err
		}
		hash = &h
	}
	id := ids.New()
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, display_name, pw_hash) VALUES ($1,$2,$3,$4)`, id, strings.ToLower(email), name, hash); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1,$2,$3)`, ws, id, role)
		return err
	})
	return id, err
}

// Login prüft E-Mail/Passwort und erzeugt eine Session. Liefert das Session-Token (nur einmal sichtbar).
func (s *Service) Login(ctx context.Context, email, password string) (string, *Principal, error) {
	var uid uuid.UUID
	var hash *string
	err := s.Pool.QueryRow(ctx, `SELECT id, pw_hash FROM users WHERE email=$1`, strings.ToLower(email)).Scan(&uid, &hash)
	if err != nil || hash == nil {
		VerifyPassword(dummyHash, password)
		return "", nil, ErrInvalid
	}
	if !VerifyPassword(*hash, password) {
		return "", nil, ErrInvalid
	}
	return s.NewSession(ctx, uid, true)
}

// NewSession erzeugt eine Session für den (ersten) Workspace des Nutzers. stepUp setzt Step-up sofort.
func (s *Service) NewSession(ctx context.Context, uid uuid.UUID, stepUp bool) (string, *Principal, error) {
	var ws uuid.UUID
	if err := s.Pool.QueryRow(ctx, `SELECT workspace_id FROM memberships WHERE user_id=$1 ORDER BY role='owner' DESC LIMIT 1`, uid).Scan(&ws); err != nil {
		return "", nil, ErrInvalid
	}
	tok := randToken("fys_")
	var step *time.Time
	if stepUp {
		t := time.Now().Add(s.stepTTL())
		step = &t
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO sessions (id_hash, user_id, workspace_id, step_up_until, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		hashToken(tok), uid, ws, step, time.Now().Add(s.ttl())); err != nil {
		return "", nil, err
	}
	p, err := s.Session(ctx, tok)
	return tok, p, err
}

// Session löst ein Session-Token auf.
func (s *Service) Session(ctx context.Context, tok string) (*Principal, error) {
	var p Principal
	var step *time.Time
	var exp time.Time
	err := s.Pool.QueryRow(ctx, `SELECT s.user_id, s.workspace_id, u.email, u.display_name, m.role, s.step_up_until, s.expires_at
		FROM sessions s JOIN users u ON u.id=s.user_id JOIN memberships m ON m.user_id=s.user_id AND m.workspace_id=s.workspace_id
		WHERE s.id_hash=$1`, hashToken(tok)).Scan(&p.UserID, &p.WorkspaceID, &p.Email, &p.DisplayName, &p.Role, &step, &exp)
	if err != nil {
		return nil, ErrInvalid
	}
	if time.Now().After(exp) {
		_, _ = s.Pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash=$1`, hashToken(tok))
		return nil, ErrExpired
	}
	if step != nil {
		p.StepUpUntil = *step
	}
	return &p, nil
}

// StepUp markiert eine Session als frisch bestätigt (nach Passkey oder Passwort).
func (s *Service) StepUp(ctx context.Context, tok string) (time.Time, error) {
	until := time.Now().Add(s.stepTTL())
	tag, err := s.Pool.Exec(ctx, `UPDATE sessions SET step_up_until=$2 WHERE id_hash=$1`, hashToken(tok), until)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrInvalid
	}
	return until, err
}

// StepUpPassword: Step-up per Passwort (Fallback, falls kein Passkey registriert ist).
func (s *Service) StepUpPassword(ctx context.Context, tok string, p *Principal, password string) (time.Time, error) {
	var hash *string
	if err := s.Pool.QueryRow(ctx, `SELECT pw_hash FROM users WHERE id=$1`, p.UserID).Scan(&hash); err != nil || hash == nil || !VerifyPassword(*hash, password) {
		return time.Time{}, ErrInvalid
	}
	return s.StepUp(ctx, tok)
}

// Logout löscht die Session.
func (s *Service) Logout(ctx context.Context, tok string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash=$1`, hashToken(tok))
	return err
}

// CreateToken erzeugt einen API-Token (nur Hash gespeichert; Klartext wird einmal zurückgegeben).
func (s *Service) CreateToken(ctx context.Context, uid uuid.UUID, name string, scopes []string, ttl time.Duration) (string, error) {
	tok := randToken("fyt_")
	var exp *time.Time
	if ttl > 0 {
		t := time.Now().Add(ttl)
		exp = &t
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO api_tokens (id, user_id, name, hash, scopes, expires_at) VALUES ($1,$2,$3,$4,$5,$6)`, ids.New(), uid, name, hashToken(tok), scopes, exp)
	return tok, err
}

// Token löst einen API-Token auf.
func (s *Service) Token(ctx context.Context, tok string) (*Principal, error) {
	var p Principal
	var exp *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT t.user_id, m.workspace_id, u.email, u.display_name, m.role, t.scopes, t.expires_at
		FROM api_tokens t JOIN users u ON u.id=t.user_id JOIN memberships m ON m.user_id=t.user_id WHERE t.hash=$1 LIMIT 1`, hashToken(tok)).
		Scan(&p.UserID, &p.WorkspaceID, &p.Email, &p.DisplayName, &p.Role, &p.Scopes, &exp)
	if err != nil {
		return nil, ErrInvalid
	}
	if exp != nil && time.Now().After(*exp) {
		return nil, ErrExpired
	}
	p.ViaToken = true
	return &p, nil
}

// HasScope prüft Token-Scopes (Sessions haben alle Scopes ihrer Rolle).
func (p *Principal) HasScope(s string) bool {
	if !p.ViaToken {
		return true
	}
	for _, x := range p.Scopes {
		if x == s || x == "*" || (strings.HasSuffix(x, ":*") && strings.HasPrefix(s, strings.TrimSuffix(x, "*"))) {
			return true
		}
	}
	return false
}
