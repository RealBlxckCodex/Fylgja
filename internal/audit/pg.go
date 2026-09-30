package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisory-Lock-Schlüssel für die Audit-Kette.
const lockKey = 0x46594c47 // "FYLG"

// PG schreibt die Hashchain in Postgres.
type PG struct{ Pool *pgxpool.Pool }

func (p *PG) Log(ctx context.Context, e Entry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	e.CreatedAt = e.CreatedAt.UTC().Truncate(time.Microsecond)
	return pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
			return err
		}
		var prev []byte
		err := tx.QueryRow(ctx, `SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&prev)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		h, err := ComputeHash(prev, e)
		if err != nil {
			return err
		}
		detail, _ := json.Marshal(e.Detail)
		if e.Detail == nil {
			detail = []byte("{}")
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor, action, target, detail, prev_hash, hash, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, e.WorkspaceID, e.Actor, e.Action, e.Target, detail, prev, h, e.CreatedAt)
		return err
	})
}

// VerifyAll lädt die komplette Kette und prüft sie. Liefert Anzahl geprüfter Einträge.
func (p *PG) VerifyAll(ctx context.Context) (int, error) {
	rows, err := p.Pool.Query(ctx, `SELECT id, workspace_id, actor, action, target, detail, prev_hash, hash, created_at FROM audit_log ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var e Entry
		var detail []byte
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.Actor, &e.Action, &e.Target, &detail, &e.PrevHash, &e.Hash, &e.CreatedAt); err != nil {
			return 0, err
		}
		_ = json.Unmarshal(detail, &e.Detail)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return len(entries), Verify(entries)
}

// List liefert Einträge (neueste zuerst) für die UI.
func (p *PG) List(ctx context.Context, limit int, beforeID int64) ([]Entry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if beforeID <= 0 {
		beforeID = 1 << 62
	}
	rows, err := p.Pool.Query(ctx, `SELECT id, workspace_id, actor, action, target, detail, prev_hash, hash, created_at FROM audit_log WHERE id < $1 ORDER BY id DESC LIMIT $2`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var detail []byte
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.Actor, &e.Action, &e.Target, &detail, &e.PrevHash, &e.Hash, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(detail, &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}
