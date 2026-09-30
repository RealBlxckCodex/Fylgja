// Package store öffnet die Postgres-Verbindung und führt Migrationen aus.
//
// Abweichung zur Spec (sqlc): siehe docs/adr/0002-pgx-handgeschriebene-queries.md.
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/realblxckcodex/fylgja/migrations"
)

// Open öffnet einen Verbindungspool und prüft die Verbindung.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return pool, nil
}

// Migrate bringt das Schema auf den neuesten Stand.
func Migrate(ctx context.Context, url string) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, db, ".")
}

// Status liefert die aktuelle Schema-Version.
func Status(ctx context.Context, url string) (int64, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return 0, err
	}
	return goose.GetDBVersionContext(ctx, db)
}
