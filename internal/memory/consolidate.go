package memory

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ConsolidationReport fasst einen nächtlichen Lauf zusammen.
type ConsolidationReport struct {
	Merged    int `json:"merged"`
	Archived  int `json:"archived"`
	Unsourced int `json:"unsourced"`
}

// Consolidate führt die deterministischen Schritte der Konsolidierung aus (11.4):
// Dedup nahezu identischer Einträge, Decay alter unwichtiger Notizen, Markierung quellenloser Einträge.
// LLM-gestützte Schritte (Widersprüche, Episodisch → Semantisch) laufen als consolidation-Run.
func (s *Service) Consolidate(ctx context.Context, dot uuid.UUID) (ConsolidationReport, error) {
	var rep ConsolidationReport
	// 1. Dedup: Paare mit Kosinus-Ähnlichkeit > 0,97 in derselben Stufe → älterer wird ersetzt.
	rows, err := s.Pool.Query(ctx, `SELECT a.id, b.id FROM memories a JOIN memories b
		ON a.dot_id=b.dot_id AND a.tier=b.tier AND a.id < b.id
		WHERE a.dot_id=$1 AND a.valid_to IS NULL AND b.valid_to IS NULL
		  AND a.embedding IS NOT NULL AND b.embedding IS NOT NULL
		  AND a.tier <> 'core' AND (a.embedding <=> b.embedding) < 0.03
		LIMIT 500`, dot)
	if err != nil {
		return rep, err
	}
	type pair struct{ old, new uuid.UUID }
	var pairs []pair
	for rows.Next() {
		var a, b uuid.UUID
		if err := rows.Scan(&a, &b); err != nil {
			rows.Close()
			return rep, err
		}
		pairs = append(pairs, pair{a, b}) // UUIDv7: a < b ⇒ a ist älter
	}
	rows.Close()
	done := map[uuid.UUID]bool{}
	now := s.now()
	for _, p := range pairs {
		if done[p.old] || done[p.new] {
			continue
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE memories SET valid_to=$2, superseded_by=$3 WHERE id=$1`, p.old, now, p.new); err != nil {
			return rep, err
		}
		// Wichtigkeit und Zugriffe übernehmen.
		_, _ = s.Pool.Exec(ctx, `UPDATE memories n SET importance=GREATEST(n.importance, o.importance), access_count=n.access_count+o.access_count
			FROM memories o WHERE n.id=$2 AND o.id=$1`, p.old, p.new)
		done[p.old] = true
		rep.Merged++
	}
	// 4. Decay: Notizen älter als 60 Tage, unwichtig, selten genutzt → archivieren.
	tag, err := s.Pool.Exec(ctx, `UPDATE memories SET valid_to=$2 WHERE dot_id=$1 AND tier='note' AND valid_to IS NULL AND pinned=false
		AND importance < 0.4 AND access_count < 2 AND created_at < $3`, dot, now, now.Add(-60*24*time.Hour))
	if err != nil {
		return rep, err
	}
	rep.Archived = int(tag.RowsAffected())
	// 5. Einträge ohne Quelle markieren (Halluzinationsverdacht).
	tag, err = s.Pool.Exec(ctx, `UPDATE memories SET source = source || '{"unsourced": true}'::jsonb
		WHERE dot_id=$1 AND valid_to IS NULL AND source = '{}'::jsonb AND origin <> 'owner'`, dot)
	if err != nil {
		return rep, err
	}
	rep.Unsourced = int(tag.RowsAffected())
	return rep, nil
}

// ProposeCoreEdit legt ein Proposal für eine core-Änderung an (Diff im Payload).
func (s *Service) ProposeCoreEdit(ctx context.Context, dot uuid.UUID, oldID *uuid.UUID, newContent string, evidence []map[string]any) (uuid.UUID, error) {
	if s.Redactor != nil && s.Redactor.ContainsSecret(newContent) {
		return uuid.Nil, ErrSecret
	}
	payload := map[string]any{"tier": "core", "new": newContent}
	if oldID != nil {
		if m, err := s.Get(ctx, dot, *oldID); err == nil {
			payload["old"] = m.Content
			payload["old_id"] = m.ID
		}
	}
	p, _ := json.Marshal(payload)
	ev, _ := json.Marshal(evidence)
	if evidence == nil {
		ev = []byte("[]")
	}
	var id uuid.UUID
	err := s.Pool.QueryRow(ctx, `INSERT INTO proposals (id, dot_id, type, payload, evidence, status) VALUES ($1,$2,'memory_edit',$3,$4,'open') RETURNING id`,
		uuid.Must(uuid.NewV7()), dot, p, ev).Scan(&id)
	return id, err
}
