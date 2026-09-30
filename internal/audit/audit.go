// Package audit implementiert das manipulationssichere Audit-Log (Spec 14.9).
//
// hash = SHA256(prev_hash || canonical_json(entry)). Die Kette wird pro Installation
// geführt; Einfügen ist über einen Advisory-Lock serialisiert.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Entry ist ein Audit-Ereignis.
type Entry struct {
	ID          int64          `json:"-"`
	WorkspaceID uuid.UUID      `json:"workspace_id"`
	Actor       string         `json:"actor"`  // "user:<id>", "dot:<id>", "system"
	Action      string         `json:"action"` // z. B. "rule.update", "approval.resolve"
	Target      string         `json:"target"`
	Detail      map[string]any `json:"detail"`
	CreatedAt   time.Time      `json:"created_at"`
	PrevHash    []byte         `json:"-"`
	Hash        []byte         `json:"-"`
}

// Canonical liefert die kanonische JSON-Form (sortierte Schlüssel, UTC-Zeit in µs).
func Canonical(e Entry) ([]byte, error) {
	detail, err := normalize(e.Detail)
	if err != nil {
		return nil, err
	}
	m := map[string]any{
		"workspace_id": e.WorkspaceID.String(),
		"actor":        e.Actor,
		"action":       e.Action,
		"target":       e.Target,
		"detail":       detail,
		"created_at":   e.CreatedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano),
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// normalize führt einen JSON-Roundtrip mit json.Number aus, damit der Hash
// unabhängig davon ist, ob der Wert aus Go oder aus Postgres-jsonb stammt.
func normalize(v map[string]any) (any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// ComputeHash berechnet den Kettenhash eines Eintrags.
func ComputeHash(prev []byte, e Entry) ([]byte, error) {
	c, err := Canonical(e)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(c)
	return h.Sum(nil), nil
}

// ErrBroken wird bei Verifikationsfehlern geliefert.
type ErrBroken struct {
	ID     int64
	Reason string
}

func (e *ErrBroken) Error() string { return fmt.Sprintf("audit: kette gebrochen bei id=%d: %s", e.ID, e.Reason) }

// Verify prüft eine geordnete Folge von Einträgen.
func Verify(entries []Entry) error {
	var prev []byte
	for i, e := range entries {
		if i > 0 && !bytes.Equal(e.PrevHash, prev) {
			return &ErrBroken{ID: e.ID, Reason: "prev_hash passt nicht"}
		}
		if i == 0 {
			prev = e.PrevHash
		}
		h, err := ComputeHash(prev, e)
		if err != nil {
			return err
		}
		if !bytes.Equal(h, e.Hash) {
			return &ErrBroken{ID: e.ID, Reason: "hash passt nicht (Eintrag verändert)"}
		}
		prev = h
	}
	return nil
}

// Logger ist die Schnittstelle, über die alle Subsysteme auditieren.
type Logger interface {
	Log(ctx context.Context, e Entry) error
}

// Memory ist eine In-Memory-Implementierung (Tests, Einzelprozess ohne DB).
type Memory struct {
	Entries []Entry
	Now     func() time.Time
}

func (m *Memory) Log(_ context.Context, e Entry) error {
	if e.CreatedAt.IsZero() {
		if m.Now != nil {
			e.CreatedAt = m.Now()
		} else {
			e.CreatedAt = time.Now()
		}
	}
	e.CreatedAt = e.CreatedAt.UTC().Truncate(time.Microsecond)
	var prev []byte
	if n := len(m.Entries); n > 0 {
		prev = m.Entries[n-1].Hash
	}
	h, err := ComputeHash(prev, e)
	if err != nil {
		return err
	}
	e.ID = int64(len(m.Entries) + 1)
	e.PrevHash, e.Hash = prev, h
	m.Entries = append(m.Entries, e)
	return nil
}

var ErrNoDB = errors.New("audit: keine datenbank")
