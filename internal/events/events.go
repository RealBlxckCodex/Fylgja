// Package events ist der Fan-out für Live-Updates (SSE, Spec 19.4): Outbox-Tabelle +
// Postgres LISTEN/NOTIFY. Clients replayen verpasste Events über Last-Event-ID.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Event ist ein ausgeliefertes Ereignis.
type Event struct {
	ID      int64           `json:"id"`
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload"`
	At      time.Time       `json:"at"`
}

// Bus verteilt Events an Abonnenten (auch über Prozessgrenzen).
type Bus struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger

	mu   sync.RWMutex
	subs map[*Sub]struct{}
}

// Sub ist ein Abonnement auf Topic-Präfixe.
type Sub struct {
	prefixes []string
	C        chan Event
}

func (s *Sub) match(topic string) bool {
	for _, p := range s.prefixes {
		if p == "*" || topic == p || (strings.HasSuffix(p, "*") && strings.HasPrefix(topic, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

// Publish implementiert runtime.Publisher.
func (b *Bus) Publish(ctx context.Context, topic string, payload any) {
	p, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var id int64
	if err := b.Pool.QueryRow(context.WithoutCancel(ctx), `INSERT INTO events (topic, payload) VALUES ($1,$2) RETURNING id`, topic, p).Scan(&id); err != nil {
		return
	}
	_, _ = b.Pool.Exec(context.WithoutCancel(ctx), `SELECT pg_notify('fylgja_events', $1)`, strconv.FormatInt(id, 10))
}

// Subscribe registriert einen Abonnenten.
func (b *Bus) Subscribe(prefixes ...string) *Sub {
	s := &Sub{prefixes: prefixes, C: make(chan Event, 256)}
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[*Sub]struct{}{}
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Unsubscribe entfernt einen Abonnenten.
func (b *Bus) Unsubscribe(s *Sub) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

func (b *Bus) dispatch(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.match(e.Topic) {
			select {
			case s.C <- e:
			default: // langsamer Client: Event verwerfen, Client lädt Snapshot neu
			}
		}
	}
}

// Replay liefert Events seit afterID für die Präfixe (Retention 10 min).
func (b *Bus) Replay(ctx context.Context, afterID int64, prefixes []string) ([]Event, error) {
	rows, err := b.Pool.Query(ctx, `SELECT id, topic, payload, created_at FROM events WHERE id > $1 ORDER BY id LIMIT 1000`, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s := &Sub{prefixes: prefixes}
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Topic, &e.Payload, &e.At); err != nil {
			return nil, err
		}
		if s.match(e.Topic) {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// Run hört auf NOTIFY und verteilt; räumt alte Events auf.
func (b *Bus) Run(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = b.Pool.Exec(ctx, `DELETE FROM events WHERE created_at < now() - interval '10 minutes'`)
			}
		}
	}()
	for ctx.Err() == nil {
		conn, err := b.Pool.Acquire(ctx)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		if _, err := conn.Exec(ctx, "LISTEN fylgja_events"); err != nil {
			conn.Release()
			time.Sleep(time.Second)
			continue
		}
		for {
			n, err := conn.Conn().WaitForNotification(ctx)
			if err != nil {
				break
			}
			id, _ := strconv.ParseInt(n.Payload, 10, 64)
			var e Event
			if err := b.Pool.QueryRow(ctx, `SELECT id, topic, payload, created_at FROM events WHERE id=$1`, id).Scan(&e.ID, &e.Topic, &e.Payload, &e.At); err == nil {
				b.dispatch(e)
			}
		}
		conn.Release()
	}
}
