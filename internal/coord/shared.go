package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/realblxckcodex/fylgja/internal/platform/ids"
)

// ---- Locks (17.5): exklusive Ressourcen mit Lease. ----

var ErrLocked = errors.New("coord: ressource ist gesperrt")

// AcquireLocks reserviert mehrere Ressourcen in fester Reihenfolge (Deadlock-Vermeidung).
func (s *Store) AcquireLocks(ctx context.Context, holder string, resources []string, ttl time.Duration) error {
	res := append([]string(nil), resources...)
	sort.Strings(res)
	res = slices.Compact(res)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, r := range res {
			tag, err := tx.Exec(ctx, `INSERT INTO locks (resource, holder_node, expires_at) VALUES ($1,$2,now()+$3::interval)
				ON CONFLICT (resource) DO UPDATE SET holder_node=EXCLUDED.holder_node, expires_at=EXCLUDED.expires_at
				WHERE locks.expires_at < now() OR locks.holder_node = EXCLUDED.holder_node`, r, holder, fmt.Sprintf("%d seconds", int(ttl.Seconds())))
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("%w: %s", ErrLocked, r)
			}
		}
		return nil
	})
}

// RenewLocks verlängert alle Leases eines Halters (Heartbeat).
func (s *Store) RenewLocks(ctx context.Context, holder string, ttl time.Duration) error {
	_, err := s.Pool.Exec(ctx, `UPDATE locks SET expires_at=now()+$2::interval WHERE holder_node=$1`, holder, fmt.Sprintf("%d seconds", int(ttl.Seconds())))
	return err
}

// ReleaseLocks gibt alle Locks eines Halters frei.
func (s *Store) ReleaseLocks(ctx context.Context, holder string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM locks WHERE holder_node=$1`, holder)
	return err
}

// ---- Blackboard (17.5): versionierter KV-Speicher pro Team, optimistische Nebenläufigkeit. ----

var ErrVersionConflict = errors.New("coord: blackboard-versionskonflikt")

type Entry struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Trust     string          `json:"trust"`
	Version   int             `json:"version"`
	UpdatedBy string          `json:"updated_by"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// BBPut schreibt einen Wert; expectVersion 0 = neu anlegen. Vertrauensmarkierung bleibt erhalten ("untrusted" bleibt "untrusted").
func (s *Store) BBPut(ctx context.Context, team uuid.UUID, key string, value any, trust string, by uuid.UUID, expectVersion int) (int, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	if trust == "" {
		trust = "owner"
	}
	var v int
	if expectVersion == 0 {
		err = s.Pool.QueryRow(ctx, `INSERT INTO blackboard (team_id, key, value, trust, version, updated_by) VALUES ($1,$2,$3,$4,1,$5)
			ON CONFLICT DO NOTHING RETURNING version`, team, key, b, trust, by).Scan(&v)
	} else {
		err = s.Pool.QueryRow(ctx, `UPDATE blackboard SET value=$3, version=version+1, updated_by=$5, updated_at=now(),
				trust = CASE WHEN blackboard.trust='untrusted' OR $4='untrusted' THEN 'untrusted' ELSE $4 END
			WHERE team_id=$1 AND key=$2 AND version=$6 RETURNING version`, team, key, b, trust, by, expectVersion).Scan(&v)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrVersionConflict
	}
	return v, err
}

// BBList liefert alle Einträge eines Teams.
func (s *Store) BBList(ctx context.Context, team uuid.UUID) ([]Entry, error) {
	rows, err := s.Pool.Query(ctx, `SELECT key, value, trust, version, coalesce(updated_by::text,''), updated_at FROM blackboard WHERE team_id=$1 ORDER BY key`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Key, &e.Value, &e.Trust, &e.Version, &e.UpdatedBy, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Bus (17.5): typisierte Nachrichten, Stern-Topologie für Worker. ----

type MessageKind string

const (
	Assign   MessageKind = "assign"
	Question MessageKind = "question"
	Answer   MessageKind = "answer"
	Report   MessageKind = "report"
	Handoff  MessageKind = "handoff"
	CancelM  MessageKind = "cancel"
)

// Sender beschreibt den Absender.
type Sender struct {
	DotID    string
	IsWorker bool
	ParentID string // Auftraggeber eines Workers
}

var ErrTopology = errors.New("coord: worker dürfen nur an ihren auftraggeber schreiben")

// Send legt eine Nachricht auf den Bus. Worker dürfen nur an ihren Auftraggeber schreiben.
// Nachrichtenbudget: max. perGraph Nachrichten pro Graph (Loop-/Sturmschutz).
func (s *Store) Send(ctx context.Context, from Sender, to string, graphID, nodeID string, kind MessageKind, body any, perGraph int) (string, error) {
	if from.IsWorker && to != from.ParentID {
		return "", ErrTopology
	}
	if graphID != "" && perGraph > 0 {
		var n int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM dot_messages WHERE graph_id=$1`, graphID).Scan(&n); err != nil {
			return "", err
		}
		if n >= perGraph {
			return "", fmt.Errorf("coord: nachrichtenbudget des graphen (%d) erschöpft", perGraph)
		}
		// Ping-Pong-Erkennung: A→B→A→B… mit identischem Inhalt.
		var pingpong int
		b, _ := json.Marshal(body)
		_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT body FROM dot_messages WHERE graph_id=$1 AND ((from_dot=$2 AND to_dot=$3) OR (from_dot=$3 AND to_dot=$2))
			ORDER BY created_at DESC LIMIT 6) t WHERE body=$4::jsonb`, graphID, from.DotID, to, b).Scan(&pingpong)
		if pingpong >= 3 {
			return "", errors.New("coord: nachrichtenschleife erkannt")
		}
	}
	id := ids.New().String()
	b, _ := json.Marshal(body)
	var g, n any
	if graphID != "" {
		g = graphID
	}
	if nodeID != "" {
		n = nodeID
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO dot_messages (id, from_dot, to_dot, graph_id, node_id, kind, body, status) VALUES ($1,$2,$3,$4,$5,$6,$7,'pending')`,
		id, from.DotID, to, g, n, kind, b)
	return id, err
}

// Inbox liefert unzugestellte Nachrichten und markiert sie (At-least-once, idempotent über id).
func (s *Store) Inbox(ctx context.Context, dot string, limit int) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `UPDATE dot_messages SET status='delivered' WHERE id IN (
			SELECT id FROM dot_messages WHERE to_dot=$1 AND status='pending' ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING id, from_dot, kind, body, coalesce(graph_id::text,''), coalesce(node_id::text,''), created_at`, dot, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, from uuid.UUID
		var kind, g, n string
		var body []byte
		var at time.Time
		if err := rows.Scan(&id, &from, &kind, &body, &g, &n, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "from": from, "kind": kind, "body": json.RawMessage(body), "graph_id": g, "node_id": n, "at": at})
	}
	return out, rows.Err()
}
