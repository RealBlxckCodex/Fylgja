package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/policy"
)

// Store ist die Postgres-Persistenz der Runtime.
type Store struct{ Pool *pgxpool.Pool }

var ErrNotFound = errors.New("runtime: nicht gefunden")

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// GetDot lädt eine Fylgja inkl. Owner-Daten und Tier-Zuordnung.
func (s *Store) GetDot(ctx context.Context, id uuid.UUID) (*Dot, error) {
	var d Dot
	var tiers []byte
	err := s.Pool.QueryRow(ctx, `SELECT d.id, d.workspace_id, d.name, d.kind, d.owner_user_id, coalesce(u.display_name, ''), d.persona, d.charter,
			d.autonomy_level, d.status, d.privacy_mode, coalesce(u.timezone, 'Europe/Berlin'), coalesce(u.locale, 'de'),
			coalesce(mp.tiers, '{}'::jsonb), d.quiet_hours, d.pulse_config, d.avatar_url, d.created_at
		FROM dots d LEFT JOIN users u ON u.id = d.owner_user_id LEFT JOIN model_profiles mp ON mp.id = d.model_profile_id
		WHERE d.id=$1`, id).Scan(&d.ID, &d.WorkspaceID, &d.Name, &d.Kind, &d.OwnerUserID, &d.OwnerName, &d.Persona, &d.Charter,
		&d.Autonomy, &d.Status, &d.PrivacyMode, &d.Timezone, &d.Locale, &tiers, &d.QuietHours, &d.PulseConfig, &d.AvatarURL, &d.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal(tiers, &d.Tiers)
	return &d, nil
}

// CreateRun legt einen Run an.
func (s *Store) CreateRun(ctx context.Context, r *Run) error {
	if r.ID == uuid.Nil {
		r.ID = ids.New()
	}
	if r.Status == "" {
		r.Status = Queued
	}
	if r.Scope == "" {
		r.Scope = policy.ScopeFull
	}
	if r.Tier == "" {
		r.Tier = "worker"
	}
	in, _ := json.Marshal(r.Input)
	return s.Pool.QueryRow(ctx, `INSERT INTO runs (id, dot_id, task_id, conversation_id, parent_run_id, kind, tool_scope, tainted, status, model_tier, input)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING created_at`,
		r.ID, r.DotID, r.TaskID, r.ConversationID, r.ParentRunID, r.Kind, r.Scope, r.Tainted, r.Status, r.Tier, in).Scan(&r.CreatedAt)
}

const runCols = `id, dot_id, task_id, conversation_id, parent_run_id, kind, tool_scope, tainted, status, model_tier, input, started_at, finished_at, coalesce(error,''), created_at`

func scanRun(row pgx.Row) (*Run, error) {
	var r Run
	var in []byte
	if err := row.Scan(&r.ID, &r.DotID, &r.TaskID, &r.ConversationID, &r.ParentRunID, &r.Kind, &r.Scope, &r.Tainted, &r.Status, &r.Tier, &in, &r.StartedAt, &r.FinishedAt, &r.Error, &r.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal(in, &r.Input)
	return &r, nil
}

func (s *Store) GetRun(ctx context.Context, id uuid.UUID) (*Run, error) {
	return scanRun(s.Pool.QueryRow(ctx, `SELECT `+runCols+` FROM runs WHERE id=$1`, id))
}

// ListRuns liefert Runs einer Fylgja (Activity View).
func (s *Store) ListRuns(ctx context.Context, dot uuid.UUID, limit int) ([]*Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+runCols+` FROM runs WHERE dot_id=$1 ORDER BY created_at DESC LIMIT $2`, dot, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveRuns liefert alle nicht terminierten Runs (Resume nach Neustart).
func (s *Store) ActiveRuns(ctx context.Context) ([]*Run, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+runCols+` FROM runs WHERE status IN ('queued','running') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetStatus setzt den Status (Zustandsübergänge nur über diese Funktion).
func (s *Store) SetStatus(ctx context.Context, id uuid.UUID, st Status, errText string) error {
	now := time.Now()
	switch st {
	case Running:
		_, err := s.Pool.Exec(ctx, `UPDATE runs SET status=$2, started_at=coalesce(started_at,$3) WHERE id=$1 AND status NOT IN ('succeeded','failed','cancelled')`, id, st, now)
		return err
	case Succeeded, Failed, Cancelled:
		_, err := s.Pool.Exec(ctx, `UPDATE runs SET status=$2, finished_at=$3, error=nullif($4,'') WHERE id=$1 AND status NOT IN ('succeeded','failed','cancelled')`, id, st, now, errText)
		return err
	default:
		_, err := s.Pool.Exec(ctx, `UPDATE runs SET status=$2 WHERE id=$1 AND status NOT IN ('succeeded','failed','cancelled')`, id, st)
		return err
	}
}

// MarkTainted setzt Taint monoton.
func (s *Store) MarkTainted(ctx context.Context, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE runs SET tainted=true WHERE id=$1`, id)
	return err
}

// Append schreibt ein Journal-Ereignis (write-ahead) mit strikt aufsteigender seq.
func (s *Store) Append(ctx context.Context, run uuid.UUID, typ string, payload any) (Event, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	var ev Event
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id=$1 FOR UPDATE`, run); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO run_events (run_id, seq, type, payload)
			VALUES ($1, coalesce((SELECT max(seq) FROM run_events WHERE run_id=$1), 0) + 1, $2, $3)
			RETURNING run_id, seq, type, payload, created_at`, run, typ, b).Scan(&ev.RunID, &ev.Seq, &ev.Type, &ev.Payload, &ev.At)
	})
	return ev, err
}

// Events lädt das Journal eines Runs.
func (s *Store) Events(ctx context.Context, run uuid.UUID) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `SELECT run_id, seq, type, payload, created_at FROM run_events WHERE run_id=$1 ORDER BY seq`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.RunID, &e.Seq, &e.Type, &e.Payload, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EnsureConversation findet oder erzeugt eine Konversation.
func (s *Store) EnsureConversation(ctx context.Context, dot uuid.UUID, platform, chatID, threadID, kind string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.Pool.QueryRow(ctx, `INSERT INTO conversations (id, dot_id, platform, platform_chat_id, platform_thread_id, kind)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (dot_id, platform, platform_chat_id, platform_thread_id) DO UPDATE SET platform=EXCLUDED.platform
		RETURNING id`, ids.New(), dot, platform, chatID, threadID, kind).Scan(&id)
	return id, err
}

// ConversationTarget liefert Plattform und Chat einer Konversation.
func (s *Store) ConversationTarget(ctx context.Context, conv uuid.UUID) (platform, chatID, threadID string, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT platform, platform_chat_id, platform_thread_id FROM conversations WHERE id=$1`, conv).Scan(&platform, &chatID, &threadID)
	return platform, chatID, threadID, notFound(err)
}

// SaveMessage speichert eine Nachricht (trust ist danach unveränderlich).
func (s *Store) SaveMessage(ctx context.Context, m *StoredMessage) error {
	if m.ID == uuid.Nil {
		m.ID = ids.New()
	}
	content, _ := json.Marshal(map[string]any{"text": m.Text, "author": m.Author})
	return s.Pool.QueryRow(ctx, `INSERT INTO messages (id, conversation_id, run_id, role, author_identity_id, content, platform_message_id, trust)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`, m.ID, m.ConversationID, m.RunID, m.Role, m.AuthorIdentityID, content, m.PlatformMessageID, m.Trust).Scan(&m.CreatedAt)
}

// RecentMessages liefert die letzten n Nachrichten chronologisch.
func (s *Store) RecentMessages(ctx context.Context, conv uuid.UUID, n int, before *uuid.UUID) ([]StoredMessage, error) {
	q := `SELECT id, conversation_id, run_id, role, content, trust, platform_message_id, created_at FROM messages WHERE conversation_id=$1`
	args := []any{conv}
	if before != nil {
		q += ` AND id < $3`
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $2`
	args = append(args, n)
	if before != nil {
		args = append(args, *before)
	}
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredMessage
	for rows.Next() {
		var m StoredMessage
		var content []byte
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.RunID, &m.Role, &content, &m.Trust, &m.PlatformMessageID, &m.CreatedAt); err != nil {
			return nil, err
		}
		var c struct{ Text, Author string }
		_ = json.Unmarshal(content, &c)
		m.Text, m.Author = c.Text, c.Author
		out = append(out, m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// ConversationSummary liefert die rollende Zusammenfassung.
func (s *Store) ConversationSummary(ctx context.Context, conv uuid.UUID) (string, error) {
	var sum string
	err := s.Pool.QueryRow(ctx, `SELECT summary FROM conversations WHERE id=$1`, conv).Scan(&sum)
	return sum, notFound(err)
}

func (s *Store) SetConversationSummary(ctx context.Context, conv uuid.UUID, summary string, upto uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE conversations SET summary=$2, summary_upto_msg=$3 WHERE id=$1`, conv, summary, upto)
	return err
}

// Rules lädt die aktiven Regeln (workspace-weit + dot-spezifisch).
func (s *Store) Rules(ctx context.Context, ws uuid.UUID) ([]policy.Rule, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, name, coalesce(dot_id::text,''), expr, effect, priority, enabled, allow_when_tainted, four_eyes, version
		FROM rules WHERE workspace_id=$1 AND enabled ORDER BY priority DESC`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []policy.Rule
	for rows.Next() {
		var r policy.Rule
		var id uuid.UUID
		if err := rows.Scan(&id, &r.Name, &r.DotID, &r.Expr, &r.Effect, &r.Priority, &r.Enabled, &r.AllowWhenTainted, &r.FourEyes, &r.Version); err != nil {
			return nil, err
		}
		r.ID = id.String()
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateApproval speichert eine Freigabeanfrage.
func (s *Store) CreateApproval(ctx context.Context, a *policy.Approval) error {
	if a.ID == "" {
		a.ID = ids.New().String()
	}
	args, _ := json.Marshal(a.ArgsRedacted)
	prev, _ := json.Marshal(a.Preview)
	var runID any
	if a.RunID != "" {
		runID = a.RunID
	}
	return s.Pool.QueryRow(ctx, `INSERT INTO approvals (id, run_id, dot_id, tool, class, args_redacted, preview, risk, reason, status, approver_group, required_approvals, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',nullif($10,''),$11,$12) RETURNING created_at`,
		a.ID, runID, a.DotID, a.Tool, a.Class, args, prev, a.Risk, a.Reason, a.ApproverGroup, max(a.RequiredApprovals, 1), a.ExpiresAt).Scan(&a.CreatedAt)
}

const apCols = `id, coalesce(run_id::text,''), dot_id, tool, class, args_redacted, preview, risk, reason, status, coalesce(approver_group,''), required_approvals,
	approvals_given, coalesce(resolved_by::text,''), coalesce(resolved_via,''), resolved_at, expires_at, created_at`

func scanApproval(row pgx.Row) (*policy.Approval, error) {
	var a policy.Approval
	var id, dot uuid.UUID
	var args, prev, given []byte
	var resolvedAt *time.Time
	if err := row.Scan(&id, &a.RunID, &dot, &a.Tool, &a.Class, &args, &prev, &a.Risk, &a.Reason, &a.Status, &a.ApproverGroup, &a.RequiredApprovals,
		&given, &a.ResolvedBy, &a.ResolvedVia, &resolvedAt, &a.ExpiresAt, &a.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	a.ID, a.DotID = id.String(), dot.String()
	_ = json.Unmarshal(args, &a.ArgsRedacted)
	_ = json.Unmarshal(prev, &a.Preview)
	_ = json.Unmarshal(given, &a.ApprovalsGiven)
	if resolvedAt != nil {
		a.ResolvedAt = *resolvedAt
	}
	if v, ok := a.Preview["deny_reason"].(string); ok {
		a.DenyReason = v
	}
	if v, ok := a.Preview["step_up"].(bool); ok {
		a.StepUp = v
	}
	return &a, nil
}

func (s *Store) GetApproval(ctx context.Context, id uuid.UUID) (*policy.Approval, error) {
	return scanApproval(s.Pool.QueryRow(ctx, `SELECT `+apCols+` FROM approvals WHERE id=$1`, id))
}

// ListApprovals liefert Approvals (optional nach Status) eines Workspaces.
func (s *Store) ListApprovals(ctx context.Context, ws uuid.UUID, status string, limit int) ([]*policy.Approval, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+apCols+` FROM approvals a WHERE dot_id IN (SELECT id FROM dots WHERE workspace_id=$1)
		AND ($2 = '' OR status=$2) ORDER BY created_at DESC LIMIT $3`, ws, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*policy.Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveApproval persistiert den Zustand nach Resolve/Expire (nur aus pending heraus).
func (s *Store) SaveApproval(ctx context.Context, a *policy.Approval) (bool, error) {
	given, _ := json.Marshal(a.ApprovalsGiven)
	prev := a.Preview
	if prev == nil {
		prev = map[string]any{}
	}
	if a.DenyReason != "" {
		prev["deny_reason"] = a.DenyReason
	}
	pb, _ := json.Marshal(prev)
	var resolvedBy any
	if a.ResolvedBy != "" {
		resolvedBy = a.ResolvedBy
	}
	var resolvedAt any
	if !a.ResolvedAt.IsZero() {
		resolvedAt = a.ResolvedAt
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE approvals SET status=$2, approvals_given=$3, resolved_by=$4, resolved_via=nullif($5,''), resolved_at=$6, preview=$7
		WHERE id=$1 AND status='pending'`, a.ID, a.Status, given, resolvedBy, a.ResolvedVia, resolvedAt, pb)
	return tag.RowsAffected() == 1, err
}

// ExpiredApprovals liefert abgelaufene offene Approvals.
func (s *Store) ExpiredApprovals(ctx context.Context, now time.Time) ([]*policy.Approval, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+apCols+` FROM approvals WHERE status='pending' AND expires_at <= $1`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*policy.Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ApprovedSameCount zählt unveränderte Freigaben desselben Tools (Trust Ladder, 10.5).
func (s *Store) ApprovedSameCount(ctx context.Context, dot uuid.UUID, tool string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE dot_id=$1 AND tool=$2 AND status='approved' AND created_at > now() - interval '30 days'`, dot, tool).Scan(&n)
	return n, err
}

// RecordUsage bucht Verbrauch.
func (s *Store) RecordUsage(ctx context.Context, u UsageEvent) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO usage_events (id, dot_id, run_id, model, tier, deployment, tokens_in, tokens_out, tokens_cached, cost_micro_eur, latency_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, ids.New(), u.DotID, u.RunID, u.Model, u.Tier, u.Deployment, u.TokensIn, u.TokensOut, u.TokensCached, u.CostMicroEUR, u.LatencyMS)
	return err
}

// RunUsage summiert Tokens und Kosten eines Runs inkl. Kind-Runs (Subagents zählen auf den Eltern-Run).
func (s *Store) RunUsage(ctx context.Context, run uuid.UUID) (tokens, cost int64, err error) {
	err = s.Pool.QueryRow(ctx, `WITH RECURSIVE t AS (SELECT id FROM runs WHERE id=$1 UNION SELECT r.id FROM runs r JOIN t ON r.parent_run_id=t.id)
		SELECT coalesce(sum(tokens_in+tokens_out),0), coalesce(sum(cost_micro_eur),0) FROM usage_events WHERE run_id IN (SELECT id FROM t)`, run).Scan(&tokens, &cost)
	return
}

// DotBudgetState liefert Tagesverbrauch und Budget einer Fylgja (20.2).
func (s *Store) DotBudgetState(ctx context.Context, dot uuid.UUID) (spent, limit int64, hard bool, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT coalesce(sum(cost_micro_eur),0) FROM usage_events WHERE dot_id=$1 AND created_at >= date_trunc('day', now())`, dot).Scan(&spent)
	if err != nil {
		return
	}
	err = s.Pool.QueryRow(ctx, `SELECT limit_micro_eur, hard FROM budgets WHERE scope='dot' AND scope_id=$1 AND period='day'`, dot).Scan(&limit, &hard)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return
}

// CreateProposal legt ein Proposal an.
func (s *Store) CreateProposal(ctx context.Context, dot uuid.UUID, typ string, payload, evidence any) (uuid.UUID, error) {
	id := ids.New()
	p, _ := json.Marshal(payload)
	e, _ := json.Marshal(evidence)
	if evidence == nil {
		e = []byte("[]")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO proposals (id, dot_id, type, payload, evidence, status) VALUES ($1,$2,$3,$4,$5,'open')`, id, dot, typ, p, e)
	return id, err
}
