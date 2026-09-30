// Package learn: Lernschleife (Spec 11.2, 11.5, 10.5) – implizite Memory-Extraktion nach Runs,
// Feedback-Auswertung zu Proposals, Trust Ladder und nächtliche Konsolidierung.
package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/memory"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/runtime"
)

// Learner bündelt die Lernjobs.
type Learner struct {
	Pool    *pgxpool.Pool
	LLM     llm.Client
	Memory  *memory.Service
	Runtime *runtime.Engine
	Log     *slog.Logger
	// TriageModel: logisches Modell für Extraktion (günstig).
	TriageModel string
	// TrustLadder: Anzahl unveränderter Freigaben bis zum Regel-Vorschlag (Default 5).
	TrustLadder int
}

func (l *Learner) log() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.Default()
}

const extractPrompt = `Extrahiere aus dem Gesprächsausschnitt dauerhafte, nützliche Fakten über den Owner, seine Personen, Projekte,
Termine oder Präferenzen. Keine Passwörter, keine Vermutungen, nichts Triviales. Antworte NUR mit JSON:
{"facts":[{"content":"...","importance":0.0-1.0,"sensitivity":"normal|private|secret-adjacent","kind":"fact|preference|core"}]}
Wenn nichts Relevantes: {"facts":[]}`

// Extract läuft nach einem erfolgreichen Chat-Run (Schreibpfad "implizit", 11.2).
func (l *Learner) Extract(ctx context.Context, run *runtime.Run, final string) {
	if run.Kind != runtime.KindChat || run.Input.Text == "" || l.LLM == nil || l.Memory == nil {
		return
	}
	temp := 0.0
	dot, err := l.Runtime.Store.GetDot(ctx, run.DotID)
	if err != nil {
		return
	}
	content := "Owner: " + run.Input.Text + "\nAssistent: " + final
	if run.Input.Trust != "owner" {
		content = runtime.WrapUntrusted("chat", "message", content)
	}
	resp, err := l.LLM.Chat(ctx, llm.Request{Model: l.TriageModel, JSONMode: true, Temperature: &temp, MaxTokens: 600,
		Messages: []llm.Message{{Role: llm.System, Content: extractPrompt}, {Role: llm.User, Content: content}},
		Meta:     llm.Meta{RunID: run.ID.String(), DotID: run.DotID.String(), Tier: "triage", Priority: llm.Background, Privacy: llm.Privacy(dot.PrivacyMode)}}, nil)
	if err != nil {
		return
	}
	_ = l.Runtime.Store.RecordUsage(ctx, runtime.UsageEvent{DotID: run.DotID, RunID: run.ID, Model: l.TriageModel, Tier: "triage", Deployment: resp.Deployment,
		TokensIn: resp.Usage.In, TokensOut: resp.Usage.Out, CostMicroEUR: resp.CostMicroEUR})
	var out struct {
		Facts []struct {
			Content     string  `json:"content"`
			Importance  float64 `json:"importance"`
			Sensitivity string  `json:"sensitivity"`
			Kind        string  `json:"kind"`
		} `json:"facts"`
	}
	s := resp.Message.Content
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	if json.Unmarshal([]byte(s), &out) != nil {
		return
	}
	origin := memory.FromOwner
	if run.Tainted || run.Input.Trust != "owner" {
		origin = memory.FromUntrusted
	}
	for i, f := range out.Facts {
		if i >= 5 || strings.TrimSpace(f.Content) == "" {
			break
		}
		// Duplikate vermeiden: sehr ähnlicher Eintrag vorhanden?
		if ex, err := l.Memory.Search(ctx, run.DotID, f.Content, memory.SearchOptions{K: 1}); err == nil && len(ex) > 0 && strings.EqualFold(ex[0].Content, f.Content) {
			continue
		}
		if f.Kind == "core" || f.Kind == "preference" {
			if origin == memory.FromOwner {
				_, _ = l.Memory.ProposeCoreEdit(ctx, run.DotID, nil, f.Content, []map[string]any{{"run_id": run.ID}})
			}
			continue
		}
		_, err := l.Memory.Write(ctx, memory.WriteRequest{DotID: run.DotID, Tier: memory.Semantic, Content: f.Content, Importance: f.Importance,
			Sensitivity: f.Sensitivity, Origin: origin, Tainted: run.Tainted, Source: map[string]any{"run_id": run.ID, "implicit": true}})
		if err != nil {
			l.log().Debug("extraktion verworfen", "err", err)
		}
	}
}

// TrustLadderProposals: wiederholt unverändert freigegebene Aktionen → eng gescopter Regel-Vorschlag (10.5).
func (l *Learner) TrustLadderProposals(ctx context.Context) int {
	n := l.TrustLadder
	if n <= 0 {
		n = 5
	}
	rows, err := l.Pool.Query(ctx, `SELECT dot_id, tool, count(*) FROM approvals WHERE status='approved' AND created_at > now()-interval '30 days'
		AND class NOT IN ('spend','destructive','credential','laptop')
		GROUP BY dot_id, tool HAVING count(*) >= $1`, n)
	if err != nil {
		return 0
	}
	type item struct {
		dot   uuid.UUID
		tool  string
		count int
	}
	var list []item
	for rows.Next() {
		var it item
		_ = rows.Scan(&it.dot, &it.tool, &it.count)
		list = append(list, it)
	}
	rows.Close()
	created := 0
	for _, it := range list {
		var exists int
		_ = l.Pool.QueryRow(ctx, `SELECT count(*) FROM proposals WHERE dot_id=$1 AND type='rule' AND payload->>'tool'=$2 AND status IN ('open','rejected')`, it.dot, it.tool).Scan(&exists)
		var ruleExists int
		_ = l.Pool.QueryRow(ctx, `SELECT count(*) FROM rules WHERE (dot_id=$1 OR dot_id IS NULL) AND expr LIKE '%' || $2 || '%' AND effect='allow'`, it.dot, it.tool).Scan(&ruleExists)
		if exists > 0 || ruleExists > 0 {
			continue
		}
		// Häufigste Empfänger-Domain als engen Scope verwenden (nie global).
		var domain string
		_ = l.Pool.QueryRow(ctx, `SELECT split_part(args_redacted->>'to','@',2) d FROM approvals WHERE dot_id=$1 AND tool=$2 AND status='approved'
			AND args_redacted ? 'to' GROUP BY d ORDER BY count(*) DESC LIMIT 1`, it.dot, it.tool).Scan(&domain)
		expr := fmt.Sprintf(`action.tool == %q`, it.tool)
		desc := fmt.Sprintf("Künftig automatisch: %s", it.tool)
		if domain != "" {
			expr += fmt.Sprintf(` && action.target.recipients.all(r, r.endsWith(%q))`, "@"+domain)
			desc += " an @" + domain
		}
		_, err := l.Runtime.Store.CreateProposal(ctx, it.dot, "rule", map[string]any{"name": desc, "expr": expr, "effect": "allow", "tool": it.tool},
			[]map[string]any{{"approved_count": it.count, "window_days": 30}})
		if err == nil {
			created++
		}
	}
	return created
}

// FeedbackProposals clustert Feedback (👎, Korrekturen, Ablehnungen) zu Präferenz-Vorschlägen.
func (l *Learner) FeedbackProposals(ctx context.Context) int {
	rows, err := l.Pool.Query(ctx, `SELECT a.dot_id, a.tool, count(*), array_agg(coalesce(a.preview->>'deny_reason','')) FROM approvals a
		WHERE a.status='denied' AND a.created_at > now()-interval '14 days' GROUP BY a.dot_id, a.tool HAVING count(*) >= 3`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	type item struct {
		dot     uuid.UUID
		tool    string
		n       int
		reasons []string
	}
	var list []item
	for rows.Next() {
		var it item
		_ = rows.Scan(&it.dot, &it.tool, &it.n, &it.reasons)
		list = append(list, it)
	}
	created := 0
	for _, it := range list {
		var exists int
		_ = l.Pool.QueryRow(ctx, `SELECT count(*) FROM proposals WHERE dot_id=$1 AND type='preference' AND payload->>'tool'=$2 AND created_at > now()-interval '14 days'`, it.dot, it.tool).Scan(&exists)
		if exists > 0 {
			continue
		}
		var reasons []string
		for _, r := range it.reasons {
			if r != "" {
				reasons = append(reasons, r)
			}
		}
		text := fmt.Sprintf("Der Owner lehnt %s häufig ab (%d× in 14 Tagen). Vor solchen Aktionen erst nachfragen oder Alternativen anbieten.", it.tool, it.n)
		if len(reasons) > 0 {
			text += " Gründe: " + strings.Join(reasons, "; ")
		}
		if _, err := l.Runtime.Store.CreateProposal(ctx, it.dot, "preference", map[string]any{"text": text, "tool": it.tool}, []map[string]any{{"denied": it.n}}); err == nil {
			created++
		}
	}
	return created
}

// Nightly führt Konsolidierung und Learner-Jobs für alle aktiven Fylgjur aus (11.4).
func (l *Learner) Nightly(ctx context.Context) {
	rows, err := l.Pool.Query(ctx, `SELECT id FROM dots WHERE status <> 'archived'`)
	if err != nil {
		return
	}
	var dots []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		_ = rows.Scan(&id)
		dots = append(dots, id)
	}
	rows.Close()
	for _, d := range dots {
		if l.Memory != nil {
			rep, err := l.Memory.Consolidate(ctx, d)
			if err == nil && (rep.Merged+rep.Archived) > 0 {
				l.log().Info("konsolidierung", "dot", d, "merged", rep.Merged, "archived", rep.Archived)
			}
		}
	}
	l.TrustLadderProposals(ctx)
	l.FeedbackProposals(ctx)
	// Retention (11.6): Journale > 90 Tage werden verdichtet (Payload geleert, Typ bleibt).
	_, _ = l.Pool.Exec(ctx, `ALTER TABLE run_events DISABLE TRIGGER run_events_append_only`)
	_, _ = l.Pool.Exec(ctx, `UPDATE run_events SET payload='{"compacted":true}' WHERE created_at < now()-interval '90 days' AND payload <> '{"compacted":true}'`)
	_, _ = l.Pool.Exec(ctx, `ALTER TABLE run_events ENABLE TRIGGER run_events_append_only`)
	_, _ = l.Pool.Exec(ctx, `DELETE FROM inbound_dedup WHERE received_at < now()-interval '7 days'`)
	_, _ = l.Pool.Exec(ctx, `DELETE FROM node_metrics WHERE ts < now()-interval '14 days'`)
}

// RunNightly startet den Nachtjob (03:30 Serverzeit, plus einmal beim Start nach 10 min).
func (l *Learner) RunNightly(ctx context.Context) {
	next := func() time.Duration {
		now := time.Now()
		t := time.Date(now.Year(), now.Month(), now.Day(), 3, 30, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		return time.Until(t)
	}
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			l.Nightly(ctx)
			timer.Reset(next())
		}
	}
}

var _ = ids.New
