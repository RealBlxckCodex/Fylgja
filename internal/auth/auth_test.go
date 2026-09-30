package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/store/testdb"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("korrektes-pferd-batterie")
	if err != nil || !strings.HasPrefix(h, "$argon2id$") {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "korrektes-pferd-batterie") || VerifyPassword(h, "falsch-falsch-falsch") {
		t.Fatal("verify")
	}
	if _, err := HashPassword("kurz"); err == nil {
		t.Fatal("kurzes passwort akzeptiert")
	}
}

func TestSessionsTokensRoles(t *testing.T) {
	pool, _ := testdb.New(t)
	ctx := context.Background()
	ws := uuid.New()
	pool.Exec(ctx, `INSERT INTO workspaces (id,name) VALUES ($1,'w')`, ws)
	s := &Service{Pool: pool}
	uid, err := s.CreateUser(ctx, ws, "Sam@Example.org", "Sam", "ein-langes-passwort", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, "sam@example.org", "falsches-passwort!"); err != ErrInvalid {
		t.Fatal("falsches passwort akzeptiert")
	}
	tok, p, err := s.Login(ctx, "SAM@example.org", "ein-langes-passwort")
	if err != nil || p.UserID != uid || p.Role != "owner" || !p.Can("own") {
		t.Fatalf("%v %+v", err, p)
	}
	// Session-Token wird nur als Hash gespeichert.
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE id_hash::text LIKE '%' || $1 || '%'`, tok).Scan(&n)
	if n != 0 {
		t.Fatal("klartext-token in db")
	}
	api, _ := s.CreateToken(ctx, uid, "ci", []string{"tasks:*"}, 0)
	tp, err := s.Token(ctx, api)
	if err != nil || !tp.HasScope("tasks:create") || tp.HasScope("rules:write") {
		t.Fatalf("scopes: %v %+v", err, tp)
	}
	s.Logout(ctx, tok)
	if _, err := s.Session(ctx, tok); err == nil {
		t.Fatal("session nach logout gültig")
	}
	viewer := &Principal{Role: "viewer"}
	if viewer.Can("work") || !viewer.Can("read") || (&Principal{Role: "auditor"}).Can("manage") {
		t.Fatal("rollen")
	}
}
