// Package testdb stellt pro Test eine frische, migrierte Datenbank bereit.
//
// Erwartet FYLGJA_TEST_DATABASE_URL (Admin-Verbindung, z. B. postgres://postgres@localhost:5432/postgres).
// Ohne Variable werden Integrationstests übersprungen. In CI stellt ein Postgres-Service sie bereit.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/store"
)

// New erzeugt eine neue Datenbank, migriert sie und räumt nach dem Test auf.
func New(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	admin := os.Getenv("FYLGJA_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("FYLGJA_TEST_DATABASE_URL nicht gesetzt – Integrationstest übersprungen")
	}
	ctx := context.Background()
	name := "fylgja_test_" + strings.ReplaceAll(ids.New().String(), "-", "")[:20]
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("testdb: %v", err)
	}
	conn.Close(ctx)
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	dsn := u.String()
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatalf("testdb migrate: %v", err)
	}
	pool, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, err := pgx.Connect(ctx, admin)
		if err == nil {
			_, _ = c.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
			c.Close(ctx)
		}
	})
	return pool, dsn
}
