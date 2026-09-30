package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/realblxckcodex/fylgja/internal/audit"
	"github.com/realblxckcodex/fylgja/internal/auth"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
)

func (s *Server) setCookie(w http.ResponseWriter, tok string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteStrictMode, MaxAge: int(ttl.Seconds())})
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	var n int
	_ = s.Pool.QueryRow(r.Context(), `SELECT count(*) FROM users`).Scan(&n)
	writeJSON(w, 200, map[string]any{"needs_setup": n == 0, "version": s.Version})
}

// setup legt beim ersten Start Workspace, Owner und die erste Fylgja an.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Workspace string `json:"workspace"`
		Email     string `json:"email"`
		Name      string `json:"name"`
		Password  string `json:"password"`
		DotName   string `json:"dot_name"`
		Timezone  string `json:"timezone"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if in.Email == "" || len(in.Password) < 12 {
		problem(w, 400, "e-mail und passwort (≥ 12 zeichen) nötig")
		return
	}
	ctx := r.Context()
	var ws, uid, dot uuid.UUID
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// Serialisiert und nur bei leerer DB.
		if _, err := tx.Exec(ctx, `LOCK TABLE users IN EXCLUSIVE MODE`); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil || n > 0 {
			return errors.New("setup bereits abgeschlossen")
		}
		hash, err := auth.HashPassword(in.Password)
		if err != nil {
			return err
		}
		ws, uid, dot = ids.New(), ids.New(), ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO workspaces (id, name) VALUES ($1,$2)`, ws, firstNonEmpty(in.Workspace, "Mein Workspace")); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, display_name, pw_hash, timezone) VALUES ($1,lower($2),$3,$4,$5)`, uid, in.Email, firstNonEmpty(in.Name, in.Email), hash, firstNonEmpty(in.Timezone, "Europe/Berlin")); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1,$2,'owner')`, ws, uid); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO dots (id, workspace_id, name, kind, owner_user_id, persona, autonomy_level, pulse_config, quiet_hours)
			VALUES ($1,$2,$3,'personal',$4,'Aufmerksam, knapp, ehrlich.',1,'{"interval_min":30,"top_k":5,"max_nudges_per_day":6,"digest_times":["08:00","18:00"]}','{"start":"22:00","end":"07:00"}')`,
			dot, ws, firstNonEmpty(in.DotName, "Hugin"), uid)
		return err
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	if s.Audit != nil {
		_ = s.Audit.Log(ctx, audit.Entry{WorkspaceID: ws, Actor: "user:" + uid.String(), Action: "setup", Target: ws.String(), Detail: map[string]any{"dot": dot.String()}})
	}
	tok, p, err := s.Auth.NewSession(ctx, uid, true)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.setCookie(w, tok, 14*24*time.Hour)
	writeJSON(w, 201, map[string]any{"principal": p, "dot_id": dot})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if !s.limiter.allow("login:"+clientIP(r)) || !s.limiter.allow("login:"+in.Email) {
		problem(w, 429, "zu viele anmeldeversuche – bitte warten")
		return
	}
	tok, p, err := s.Auth.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		problem(w, 401, "e-mail oder passwort falsch")
		return
	}
	if s.Audit != nil {
		_ = s.Audit.Log(r.Context(), audit.Entry{WorkspaceID: p.WorkspaceID, Actor: "user:" + p.UserID.String(), Action: "auth.login", Detail: map[string]any{"method": "password", "ip": clientIP(r)}})
	}
	s.setCookie(w, tok, 14*24*time.Hour)
	writeJSON(w, 200, map[string]any{"principal": p})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_ = s.Auth.Logout(r.Context(), sessionToken(r))
	s.setCookie(w, "", -time.Second)
	w.WriteHeader(204)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	has := false
	if s.Passkeys != nil {
		has = s.Passkeys.HasPasskey(r.Context(), p.UserID)
	}
	var ws string
	_ = s.Pool.QueryRow(r.Context(), `SELECT name FROM workspaces WHERE id=$1`, p.WorkspaceID).Scan(&ws)
	writeJSON(w, 200, map[string]any{"principal": p, "has_passkey": has, "workspace_name": ws, "stepped_up": p.SteppedUp(time.Now())})
}

func (s *Server) stepUpPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	p := principal(r)
	if !s.limiter.allow("stepup:" + p.UserID.String()) {
		problem(w, 429, "zu viele versuche")
		return
	}
	until, err := s.Auth.StepUpPassword(r.Context(), sessionToken(r), p, in.Password)
	if err != nil {
		problem(w, 401, "passwort falsch")
		return
	}
	s.audit(r, "auth.step_up", "", map[string]any{"method": "password"})
	writeJSON(w, 200, map[string]any{"step_up_until": until})
}

func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if s.Passkeys == nil {
		problem(w, 501, "passkeys nicht konfiguriert")
		return
	}
	opts, key, err := s.Passkeys.BeginRegistration(r.Context(), principal(r).UserID)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"options": opts, "key": key})
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if err := s.Passkeys.FinishRegistration(r.Context(), r.URL.Query().Get("key"), r); err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.audit(r, "auth.passkey_added", "", nil)
	writeJSON(w, 201, map[string]any{"ok": true})
}

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if s.Passkeys == nil {
		problem(w, 501, "passkeys nicht konfiguriert")
		return
	}
	opts, key, err := s.Passkeys.BeginLogin()
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"options": opts, "key": key})
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("login:" + clientIP(r)) {
		problem(w, 429, "zu viele anmeldeversuche")
		return
	}
	tok, p, err := s.Passkeys.FinishLogin(r.Context(), r.URL.Query().Get("key"), r)
	if err != nil {
		problem(w, 401, "passkey-anmeldung fehlgeschlagen")
		return
	}
	if s.Audit != nil {
		_ = s.Audit.Log(r.Context(), audit.Entry{WorkspaceID: p.WorkspaceID, Actor: "user:" + p.UserID.String(), Action: "auth.login", Detail: map[string]any{"method": "passkey"}})
	}
	s.setCookie(w, tok, 14*24*time.Hour)
	writeJSON(w, 200, map[string]any{"principal": p})
}

func (s *Server) passkeyStepUpBegin(w http.ResponseWriter, r *http.Request) {
	if s.Passkeys == nil {
		problem(w, 501, "passkeys nicht konfiguriert")
		return
	}
	opts, key, err := s.Passkeys.BeginStepUp(r.Context(), principal(r).UserID)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"options": opts, "key": key})
}

func (s *Server) passkeyStepUpFinish(w http.ResponseWriter, r *http.Request) {
	until, err := s.Passkeys.FinishStepUp(r.Context(), r.URL.Query().Get("key"), sessionToken(r), r)
	if err != nil {
		problem(w, 401, "bestätigung fehlgeschlagen")
		return
	}
	s.audit(r, "auth.step_up", "", map[string]any{"method": "passkey"})
	writeJSON(w, 200, map[string]any{"step_up_until": until})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string   `json:"name"`
		Scopes  []string `json:"scopes"`
		TTLDays int      `json:"ttl_days"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	for _, sc := range in.Scopes {
		if (sc == "stepup" || sc == "*") && !stepped(principal(r)) {
			w.Header().Set("X-Fylgja-Step-Up", "required")
			problem(w, 428, "tokens mit scope 'stepup' erfordern step-up")
			return
		}
	}
	tok, err := s.Auth.CreateToken(r.Context(), principal(r).UserID, in.Name, in.Scopes, time.Duration(in.TTLDays)*24*time.Hour)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.audit(r, "auth.token_created", in.Name, map[string]any{"scopes": in.Scopes})
	writeJSON(w, 201, map[string]any{"token": tok, "hint": "wird nur einmal angezeigt"})
}

var _ = json.Marshal
