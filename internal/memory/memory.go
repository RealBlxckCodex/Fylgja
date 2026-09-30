// Package memory implementiert das geschichtete Gedächtnis (Spec Kapitel 11).
//
// Schreibpfad mit Trust-Gating (kein core/procedural aus untrusted Inhalten),
// Secret-Scanner, hybrider Lesepfad (pgvector + Volltext → Reciprocal Rank Fusion,
// Recency/Importance/Pinned-Rerank), Hard-Delete und Markdown-Export.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/platform/clock"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/vault"
)

type Tier string

const (
	Core       Tier = "core"
	Semantic   Tier = "semantic"
	Episodic   Tier = "episodic"
	Procedural Tier = "procedural"
	Note       Tier = "note"
)

func (t Tier) Valid() bool {
	switch t {
	case Core, Semantic, Episodic, Procedural, Note:
		return true
	}
	return false
}

// Origin ist die Vertrauensstufe der Quelle.
type Origin string

const (
	FromOwner     Origin = "owner"
	FromSystem    Origin = "system"
	FromMember    Origin = "member"
	FromUntrusted Origin = "untrusted"
)

// Memory ist ein Eintrag.
type Memory struct {
	ID           uuid.UUID      `json:"id"`
	DotID        uuid.UUID      `json:"dot_id"`
	Tier         Tier           `json:"tier"`
	Content      string         `json:"content"`
	Importance   float64        `json:"importance"`
	Sensitivity  string         `json:"sensitivity"`
	Origin       Origin         `json:"origin"`
	Source       map[string]any `json:"source"`
	ValidFrom    time.Time      `json:"valid_from"`
	ValidTo      *time.Time     `json:"valid_to,omitempty"`
	SupersededBy *uuid.UUID     `json:"superseded_by,omitempty"`
	Pinned       bool           `json:"pinned"`
	AccessCount  int            `json:"access_count"`
	CreatedAt    time.Time      `json:"created_at"`
	// Score und Why werden beim Retrieval gesetzt (Transparenz, 11.3).
	Score float64 `json:"score,omitempty"`
	Why   string  `json:"why,omitempty"`
}

var (
	ErrSecret          = errors.New("memory: inhalt enthält ein secret und wird nicht gespeichert")
	ErrUntrustedTier   = errors.New("memory: aus untrusted inhalten dürfen keine core/procedural-einträge entstehen")
	ErrCoreNeedsReview = errors.New("memory: änderungen am core-memory sind proposals")
)

// Service ist der Memory-Dienst.
type Service struct {
	Pool     *pgxpool.Pool
	Embedder llm.Embedder
	Model    string // embedder-modell
	Redactor *vault.Redactor
	Clock    clock.Clock
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock.Now()
	}
	return time.Now()
}

// WriteRequest beschreibt einen Schreibwunsch.
type WriteRequest struct {
	DotID       uuid.UUID
	Tier        Tier
	Content     string
	Importance  float64
	Sensitivity string
	Origin      Origin
	Source      map[string]any
	Pinned      bool
	// Direct: explizite Nutzeraktion (UI/API). Nur dann sind core-Schreibzugriffe direkt erlaubt.
	Direct bool
	// Tainted: der schreibende Run ist getaintet.
	Tainted bool
}

// CheckWrite prüft die Schreibregeln (11.2, 14.5) ohne zu schreiben.
func CheckWrite(r *vault.Redactor, w WriteRequest) error {
	if !w.Tier.Valid() {
		return fmt.Errorf("memory: tier %q ungültig", w.Tier)
	}
	if strings.TrimSpace(w.Content) == "" {
		return errors.New("memory: leerer inhalt")
	}
	if r != nil && r.ContainsSecret(w.Content) {
		return ErrSecret
	}
	untrusted := w.Origin == FromUntrusted || w.Origin == FromMember || w.Tainted
	if untrusted && (w.Tier == Core || w.Tier == Procedural) {
		return ErrUntrustedTier
	}
	if w.Tier == Core && !w.Direct {
		return ErrCoreNeedsReview
	}
	return nil
}

func vecLiteral(v []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}

func (s *Service) embed(ctx context.Context, text string) (*string, error) {
	if s.Embedder == nil {
		return nil, nil
	}
	v, err := s.Embedder.Embed(ctx, s.Model, []string{text})
	if err != nil || len(v) == 0 || len(v[0]) == 0 {
		return nil, err
	}
	if len(v[0]) != 1024 {
		return nil, fmt.Errorf("memory: embedding-dimension %d, erwartet 1024", len(v[0]))
	}
	lit := vecLiteral(v[0])
	return &lit, nil
}

// Write speichert einen Eintrag nach Prüfung.
func (s *Service) Write(ctx context.Context, w WriteRequest) (*Memory, error) {
	if w.Origin == "" {
		w.Origin = FromOwner
	}
	if err := CheckWrite(s.Redactor, w); err != nil {
		return nil, err
	}
	if w.Sensitivity == "" {
		w.Sensitivity = "normal"
	}
	if w.Importance == 0 {
		w.Importance = 0.5
	}
	if w.Origin == FromUntrusted {
		w.Importance = math.Min(w.Importance, 0.3) // niedrigere Gewichtung
	}
	emb, err := s.embed(ctx, w.Content)
	if err != nil {
		return nil, err
	}
	m := &Memory{ID: ids.New(), DotID: w.DotID, Tier: w.Tier, Content: w.Content, Importance: w.Importance, Sensitivity: w.Sensitivity,
		Origin: w.Origin, Source: w.Source, Pinned: w.Pinned || w.Tier == Core, ValidFrom: s.now(), CreatedAt: s.now()}
	src, _ := json.Marshal(nullMap(w.Source))
	_, err = s.Pool.Exec(ctx, `INSERT INTO memories (id, dot_id, tier, content, embedding, importance, sensitivity, origin, source, valid_from, pinned, created_at)
		VALUES ($1,$2,$3,$4,$5::vector,$6,$7,$8,$9,$10,$11,$12)`,
		m.ID, m.DotID, m.Tier, m.Content, emb, m.Importance, m.Sensitivity, m.Origin, src, m.ValidFrom, m.Pinned, m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func nullMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// Supersede ersetzt einen Eintrag durch einen neuen (Widerspruchsauflösung, 11.4).
func (s *Service) Supersede(ctx context.Context, oldID uuid.UUID, w WriteRequest) (*Memory, error) {
	m, err := s.Write(ctx, w)
	if err != nil {
		return nil, err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE memories SET valid_to=$2, superseded_by=$3 WHERE id=$1 AND dot_id=$4`, oldID, s.now(), m.ID, w.DotID)
	return m, err
}

const cols = `id, dot_id, tier, content, importance, sensitivity, origin, source, valid_from, valid_to, superseded_by, pinned, access_count, created_at`

func scan(row pgx.Row) (*Memory, error) {
	var m Memory
	var src []byte
	if err := row.Scan(&m.ID, &m.DotID, &m.Tier, &m.Content, &m.Importance, &m.Sensitivity, &m.Origin, &src, &m.ValidFrom, &m.ValidTo, &m.SupersededBy, &m.Pinned, &m.AccessCount, &m.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(src, &m.Source)
	return &m, nil
}

// Get liefert einen Eintrag.
func (s *Service) Get(ctx context.Context, dot, id uuid.UUID) (*Memory, error) {
	return scan(s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM memories WHERE id=$1 AND dot_id=$2`, id, dot))
}

// CoreMemories liefert die gepinnten Kernfakten (immer im Prompt).
func (s *Service) CoreMemories(ctx context.Context, dot uuid.UUID) ([]*Memory, error) {
	return s.list(ctx, `SELECT `+cols+` FROM memories WHERE dot_id=$1 AND tier='core' AND valid_to IS NULL ORDER BY importance DESC, created_at`, dot)
}

// List liefert Einträge einer Stufe (Memory-Browser).
func (s *Service) List(ctx context.Context, dot uuid.UUID, tier Tier, limit int) ([]*Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	if tier == "" {
		return s.list(ctx, `SELECT `+cols+` FROM memories WHERE dot_id=$1 ORDER BY created_at DESC LIMIT $2`, dot, limit)
	}
	return s.list(ctx, `SELECT `+cols+` FROM memories WHERE dot_id=$1 AND tier=$2 ORDER BY created_at DESC LIMIT $3`, dot, tier, limit)
}

func (s *Service) list(ctx context.Context, q string, args ...any) ([]*Memory, error) {
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Memory
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SearchOptions steuern das Retrieval.
type SearchOptions struct {
	K int
	// At: temporale Frage ("Wo wohnte X im Mai?") – nur Einträge, die zu diesem Zeitpunkt gültig waren.
	At *time.Time
	// ExcludeSensitive: secret-adjacent nicht liefern (Team/Delegation).
	ExcludeSensitive bool
	Tiers            []Tier
}

func tsQuery(q string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 2 {
			terms = append(terms, w+":*")
		}
	}
	return strings.Join(terms, " | ")
}

// Search ist der hybride Lesepfad (11.3).
func (s *Service) Search(ctx context.Context, dot uuid.UUID, query string, o SearchOptions) ([]*Memory, error) {
	if o.K <= 0 {
		o.K = 8
	}
	const pool = 30
	where := `dot_id=$1`
	args := []any{dot}
	if o.At != nil {
		args = append(args, *o.At)
		where += fmt.Sprintf(` AND valid_from <= $%d AND (valid_to IS NULL OR valid_to > $%d)`, len(args), len(args))
	} else {
		where += ` AND valid_to IS NULL`
	}
	if o.ExcludeSensitive {
		where += ` AND sensitivity <> 'secret-adjacent'`
	}
	if len(o.Tiers) > 0 {
		args = append(args, o.Tiers)
		where += fmt.Sprintf(` AND tier = ANY($%d::text[])`, len(args))
	}
	ranks := map[uuid.UUID]*Memory{}
	why := map[uuid.UUID][]string{}
	rrf := map[uuid.UUID]float64{}
	add := func(list []*Memory, label string) {
		for i, m := range list {
			if _, ok := ranks[m.ID]; !ok {
				ranks[m.ID] = m
			}
			rrf[m.ID] += 1.0 / float64(60+i+1)
			why[m.ID] = append(why[m.ID], fmt.Sprintf("%s#%d", label, i+1))
		}
	}
	if emb, err := s.embed(ctx, query); err == nil && emb != nil {
		a := append(append([]any{}, args...), *emb)
		vec, err := s.list(ctx, `SELECT `+cols+` FROM memories WHERE `+where+fmt.Sprintf(` AND embedding IS NOT NULL AND (embedding <=> $%d::vector) < 0.85 ORDER BY embedding <=> $%d::vector LIMIT %d`, len(a), len(a), pool), a...)
		if err != nil {
			return nil, err
		}
		add(vec, "vektor")
	}
	if tq := tsQuery(query); tq != "" {
		a := append(append([]any{}, args...), tq)
		ft, err := s.list(ctx, `SELECT `+cols+` FROM memories WHERE `+where+fmt.Sprintf(` AND tsv @@ to_tsquery('simple', $%d) ORDER BY ts_rank(tsv, to_tsquery('simple', $%d)) DESC LIMIT %d`, len(a), len(a), pool), a...)
		if err != nil {
			return nil, err
		}
		add(ft, "volltext")
	}
	now := s.now()
	out := make([]*Memory, 0, len(ranks))
	for id, m := range ranks {
		ageDays := now.Sub(m.CreatedAt).Hours() / 24
		recency := math.Exp(-ageDays / 90) // Halbwertszeit ~ 62 Tage
		if m.Tier == Semantic || m.Tier == Procedural || m.Tier == Core {
			recency = 0.5 + 0.5*recency // Fakten altern langsamer
		}
		pinned := 1.0
		if m.Pinned {
			pinned = 1.5
		}
		m.Score = rrf[id] * recency * (0.5 + m.Importance) * pinned
		m.Why = strings.Join(why[id], ",")
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > o.K {
		out = out[:o.K]
	}
	if len(out) > 0 {
		idsList := make([]uuid.UUID, len(out))
		for i, m := range out {
			idsList[i] = m.ID
		}
		_, _ = s.Pool.Exec(ctx, `UPDATE memories SET access_count=access_count+1, last_accessed=$2 WHERE id = ANY($1)`, idsList, now)
	}
	return out, nil
}

// Forget löscht hart inklusive Embeddings und abgeleiteter Einträge (11.6).
// Liefert die Anzahl gelöschter Einträge; Inhalte erscheinen nicht im Audit.
func (s *Service) Forget(ctx context.Context, dot uuid.UUID, ids []uuid.UUID) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.Pool.Exec(ctx, `WITH RECURSIVE chain AS (
			SELECT id FROM memories WHERE dot_id=$1 AND id = ANY($2)
			UNION
			SELECT m.id FROM memories m JOIN chain c ON m.superseded_by = c.id OR (m.source->>'derived_from')::uuid = c.id
		) DELETE FROM memories WHERE id IN (SELECT id FROM chain) AND dot_id=$1`, dot, ids)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ForgetTopic sucht passende Einträge und löscht sie (/forget <thema>).
func (s *Service) ForgetTopic(ctx context.Context, dot uuid.UUID, topic string) (int, error) {
	res, err := s.Search(ctx, dot, topic, SearchOptions{K: 20})
	if err != nil {
		return 0, err
	}
	var idsList []uuid.UUID
	for _, m := range res {
		if strings.Contains(strings.ToLower(m.Content), strings.ToLower(topic)) || strings.Contains(m.Why, "volltext") {
			idsList = append(idsList, m.ID)
		}
	}
	return s.Forget(ctx, dot, idsList)
}

// Update ändert Inhalt/Pinned eines Eintrags (Memory-Browser) und berechnet das Embedding neu.
func (s *Service) Update(ctx context.Context, dot, id uuid.UUID, content *string, pinned *bool, importance *float64) (*Memory, error) {
	m, err := s.Get(ctx, dot, id)
	if err != nil {
		return nil, err
	}
	if content != nil {
		if s.Redactor != nil && s.Redactor.ContainsSecret(*content) {
			return nil, ErrSecret
		}
		emb, err := s.embed(ctx, *content)
		if err != nil {
			return nil, err
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE memories SET content=$3, embedding=$4::vector WHERE id=$1 AND dot_id=$2`, id, dot, *content, emb); err != nil {
			return nil, err
		}
		m.Content = *content
	}
	if pinned != nil {
		if _, err := s.Pool.Exec(ctx, `UPDATE memories SET pinned=$3 WHERE id=$1 AND dot_id=$2`, id, dot, *pinned); err != nil {
			return nil, err
		}
		m.Pinned = *pinned
	}
	if importance != nil {
		if _, err := s.Pool.Exec(ctx, `UPDATE memories SET importance=$3 WHERE id=$1 AND dot_id=$2`, id, dot, *importance); err != nil {
			return nil, err
		}
		m.Importance = *importance
	}
	return m, nil
}
