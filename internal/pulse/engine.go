package pulse

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/memory"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/runtime"
)

// Notifier stellt Nudges und Digests zu.
type Notifier interface {
	NotifyOwner(ctx context.Context, dot *runtime.Dot, text string)
}

// Engine ist der Pulse-Scheduler.
type Engine struct {
	Pool    *pgxpool.Pool
	Runtime *runtime.Engine
	Memory  *memory.Service
	Notify  Notifier
	HTTP    *http.Client // netguard-Client für Feeds
	Log     *slog.Logger
	Now     func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

// Signal ist ein gesammeltes Signal.
type Signal struct {
	ID        uuid.UUID      `json:"id"`
	Source    string         `json:"source"`
	Kind      string         `json:"kind"`
	DedupKey  string         `json:"dedup_key"`
	Payload   map[string]any `json:"payload"`
	Score     float64        `json:"score"`
	Untrusted bool           `json:"untrusted"`
}

// AddSignal speichert ein Signal (dedupliziert über dot/source/dedup_key).
func (e *Engine) AddSignal(ctx context.Context, dot uuid.UUID, s Signal) (bool, error) {
	if s.Payload == nil {
		s.Payload = map[string]any{}
	}
	s.Payload["untrusted"] = s.Untrusted
	b, _ := json.Marshal(s.Payload)
	tag, err := e.Pool.Exec(ctx, `INSERT INTO signals (id, dot_id, source, kind, dedup_key, payload, score) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (dot_id, source, dedup_key) WHERE dedup_key <> '' DO NOTHING`, ids.New(), dot, s.Source, s.Kind, s.DedupKey, b, s.Score)
	return err == nil && tag.RowsAffected() == 1, err
}

// Collect sammelt Signale ohne LLM (10.2 Schritt 1).
func (e *Engine) Collect(ctx context.Context, dot *runtime.Dot, cfg Config) int {
	n := 0
	now := e.now()
	// Fällige Aufgaben (nächste 48 h).
	rows, err := e.Pool.Query(ctx, `SELECT id, title, due_at FROM tasks WHERE dot_id=$1 AND status NOT IN ('done','cancelled','failed')
		AND due_at IS NOT NULL AND due_at < $2`, dot.ID, now.Add(48*time.Hour))
	if err == nil {
		for rows.Next() {
			var id uuid.UUID
			var title string
			var due time.Time
			_ = rows.Scan(&id, &title, &due)
			score := 0.5
			if due.Sub(now) < 6*time.Hour {
				score = 0.9
			}
			if ok, _ := e.AddSignal(ctx, dot.ID, Signal{Source: "tasks", Kind: "due", DedupKey: id.String() + due.Format("2006-01-02"),
				Payload: map[string]any{"title": title, "due_at": due}, Score: score}); ok {
				n++
			}
		}
		rows.Close()
	}
	// Offene Freigaben, die schon lange warten.
	var waiting int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE dot_id=$1 AND status='pending' AND created_at < $2`, dot.ID, now.Add(-2*time.Hour)).Scan(&waiting)
	if waiting > 0 {
		if ok, _ := e.AddSignal(ctx, dot.ID, Signal{Source: "approvals", Kind: "waiting", DedupKey: now.Format("2006-01-02T15"),
			Payload: map[string]any{"count": waiting}, Score: 0.7}); ok {
			n++
		}
	}
	// RSS/Atom-Feeds.
	for _, f := range cfg.Feeds {
		n += e.collectFeed(ctx, dot.ID, f, cfg.Interests)
	}
	return n
}

type feed struct {
	Items []struct {
		Title string `xml:"title"`
		Link  string `xml:"link"`
		GUID  string `xml:"guid"`
		Desc  string `xml:"description"`
	} `xml:"channel>item"`
	Entries []struct {
		Title string `xml:"title"`
		ID    string `xml:"id"`
		Link  struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Summary string `xml:"summary"`
	} `xml:"entry"`
}

func (e *Engine) collectFeed(ctx context.Context, dot uuid.UUID, url string, interests []string) int {
	if e.HTTP == nil {
		return 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		e.log().Info("feed", "url", url, "err", err)
		return 0
	}
	defer resp.Body.Close()
	var f feed
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&f); err != nil {
		return 0
	}
	type item struct{ title, link, key, text string }
	var items []item
	for _, i := range f.Items {
		items = append(items, item{i.Title, i.Link, firstNonEmpty(i.GUID, i.Link), i.Desc})
	}
	for _, en := range f.Entries {
		items = append(items, item{en.Title, en.Link.Href, firstNonEmpty(en.ID, en.Link.Href), en.Summary})
	}
	n := 0
	for i, it := range items {
		if i >= 30 {
			break
		}
		score := 0.2
		lt := strings.ToLower(it.title + " " + it.text)
		for _, kw := range interests {
			if kw != "" && strings.Contains(lt, strings.ToLower(kw)) {
				score += 0.3
			}
		}
		if ok, _ := e.AddSignal(ctx, dot, Signal{Source: "rss:" + url, Kind: "item", DedupKey: it.key,
			Payload: map[string]any{"title": it.title, "link": it.link, "summary": truncate(it.text, 500)}, Score: min(score, 1), Untrusted: true}); ok {
			n++
		}
	}
	return n
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// OutputSchema der Pulse-Runs: Ergebnisse als Note/Draft/Proposal/Nudge (10.2 Schritt 5).
var OutputSchema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{
	"type":{"type":"string","enum":["note","draft","proposal","nudge"]},
	"urgency":{"type":"string","enum":["low","normal","high","critical"]},
	"text":{"type":"string"},
	"signal_ids":{"type":"array","items":{"type":"string"}}},"required":["type","text"]}}},"required":["items"]}`)

// StartPulse wählt die Top-K Signale (Attention Queue) und startet einen Read-only-Run.
func (e *Engine) StartPulse(ctx context.Context, dot *runtime.Dot) (*runtime.Run, error) {
	cfg := ParseConfig(dot.PulseConfig)
	e.Collect(ctx, dot, cfg)
	rows, err := e.Pool.Query(ctx, `SELECT id, source, kind, payload, score FROM signals WHERE dot_id=$1 AND status='new' ORDER BY score DESC, seen_at LIMIT $2`, dot.ID, cfg.TopK)
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	var selected []uuid.UUID
	for rows.Next() {
		var s Signal
		var p []byte
		_ = rows.Scan(&s.ID, &s.Source, &s.Kind, &p, &s.Score)
		_ = json.Unmarshal(p, &s.Payload)
		body, _ := json.Marshal(s.Payload)
		entry := fmt.Sprintf("signal %s (%s/%s, score %.2f): %s", s.ID, s.Source, s.Kind, s.Score, body)
		if u, _ := s.Payload["untrusted"].(bool); u {
			entry = runtime.WrapUntrusted(s.Source, "signal", entry)
		}
		sb.WriteString("- " + entry + "\n")
		selected = append(selected, s.ID)
	}
	rows.Close()
	if len(selected) == 0 {
		return nil, nil // nichts Relevantes: kein LLM-Aufruf (Kosten sparen)
	}
	_, _ = e.Pool.Exec(ctx, `UPDATE signals SET status='triaged' WHERE id = ANY($1)`, selected)
	prompt := "PULSE (Hintergrund, nur lesend). Der Owner ist gerade nicht aktiv. Prüfe diese Signale, recherchiere bei Bedarf mit Lese-Werkzeugen " +
		"und entscheide je Signal: note (still merken), draft (Entwurf vorbereiten, nichts senden), proposal (Owner muss entscheiden) oder nudge (kurzer Hinweis). " +
		"Sei sparsam mit nudges. Antworte am Ende NUR mit JSON gemäß Schema.\n\nSignale:\n" + sb.String()
	run := &runtime.Run{DotID: dot.ID, Kind: runtime.KindPulse, Scope: policy.ScopeReadonly, Tier: "triage",
		Input: runtime.Input{Text: prompt, Trust: "system", OutputSchema: OutputSchema, MaxSteps: 12}}
	if err := e.Runtime.Submit(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

// AfterRun verarbeitet das Ergebnis eines Pulse-Runs (wird aus Engine.OnFinish aufgerufen).
func (e *Engine) AfterRun(ctx context.Context, run *runtime.Run) {
	if run.Kind != runtime.KindPulse {
		return
	}
	_, data, err := e.Runtime.Final(ctx, run.ID)
	if err != nil || data == nil {
		return
	}
	b, _ := json.Marshal(data)
	var out struct {
		Items []struct {
			Type      string   `json:"type"`
			Urgency   string   `json:"urgency"`
			Text      string   `json:"text"`
			SignalIDs []string `json:"signal_ids"`
		} `json:"items"`
	}
	if json.Unmarshal(b, &out) != nil {
		return
	}
	dot, err := e.Runtime.Store.GetDot(ctx, run.DotID)
	if err != nil {
		return
	}
	cfg, q := ParseConfig(dot.PulseConfig), ParseQuiet(dot.QuietHours)
	loc, err := time.LoadLocation(dot.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := e.now().In(loc)
	for _, it := range out.Items {
		switch it.Type {
		case "note":
			if e.Memory != nil {
				// Pulse ist getaintet, wenn Signale untrusted waren – Memory-Gating greift automatisch.
				_, _ = e.Memory.Write(ctx, memory.WriteRequest{DotID: dot.ID, Tier: memory.Note, Content: it.Text, Origin: originOf(run),
					Tainted: run.Tainted, Source: map[string]any{"run_id": run.ID, "pulse": true}})
			}
		case "draft", "proposal":
			typ := "pulse_item"
			if it.Type == "draft" {
				typ = "action"
			}
			_, _ = e.Runtime.Store.CreateProposal(ctx, dot.ID, typ, map[string]any{"kind": it.Type, "text": it.Text, "urgency": it.Urgency},
				[]map[string]any{{"run_id": run.ID, "signals": it.SignalIDs}})
		case "nudge":
			var sent int
			_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM signals WHERE dot_id=$1 AND source='pulse:nudge' AND status='sent' AND seen_at > $2`,
				dot.ID, now.Add(-24*time.Hour)).Scan(&sent)
			d := Decide(now, Urgency(firstNonEmpty(it.Urgency, "normal")), q, sent, cfg.MaxNudgesPerDay)
			status := "digest"
			if d == Now && e.Notify != nil {
				e.Notify.NotifyOwner(ctx, dot, "💡 "+it.Text)
				status = "sent"
			}
			_, _ = e.Pool.Exec(ctx, `INSERT INTO signals (id, dot_id, source, kind, payload, score, status) VALUES ($1,$2,'pulse:nudge',$3,$4,0,$5)`,
				ids.New(), dot.ID, firstNonEmpty(it.Urgency, "normal"), map[string]any{"text": it.Text}, status)
		}
	}
	_, _ = e.Pool.Exec(ctx, `UPDATE signals SET status='done' WHERE dot_id=$1 AND status='triaged'`, dot.ID)
}

func originOf(run *runtime.Run) memory.Origin {
	if run.Tainted {
		return memory.FromUntrusted
	}
	return memory.FromSystem
}

// SendDigest bündelt nicht dringende Nudges.
func (e *Engine) SendDigest(ctx context.Context, dot *runtime.Dot) int {
	rows, err := e.Pool.Query(ctx, `UPDATE signals SET status='sent' WHERE dot_id=$1 AND source='pulse:nudge' AND status='digest' RETURNING payload`, dot.ID)
	if err != nil {
		return 0
	}
	var lines []string
	for rows.Next() {
		var p []byte
		_ = rows.Scan(&p)
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		if t, ok := m["text"].(string); ok {
			lines = append(lines, "• "+t)
		}
	}
	rows.Close()
	var pending int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE dot_id=$1 AND status='pending'`, dot.ID).Scan(&pending)
	if pending > 0 {
		lines = append(lines, fmt.Sprintf("• %d Freigabe(n) warten auf dich (/approve)", pending))
	}
	if len(lines) == 0 || e.Notify == nil {
		return 0
	}
	sort.Strings(lines)
	e.Notify.NotifyOwner(ctx, dot, "🗞️ Digest von "+dot.Name+"\n"+strings.Join(lines, "\n"))
	return len(lines)
}

// Tick prüft fällige Pulses, Digests und Routinen (Scheduler, alle 30 s).
func (e *Engine) Tick(ctx context.Context) {
	now := e.now()
	rows, err := e.Pool.Query(ctx, `SELECT id FROM dots WHERE status='active'`)
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
	for _, id := range dots {
		dot, err := e.Runtime.Store.GetDot(ctx, id)
		if err != nil {
			continue
		}
		cfg, q := ParseConfig(dot.PulseConfig), ParseQuiet(dot.QuietHours)
		loc, err := time.LoadLocation(dot.Timezone)
		if err != nil {
			loc = time.UTC
		}
		local := now.In(loc)
		// Pulse
		var lastPulse, lastActivity *time.Time
		_ = e.Pool.QueryRow(ctx, `SELECT max(created_at) FILTER (WHERE kind='pulse'), max(created_at) FILTER (WHERE kind='chat') FROM runs WHERE dot_id=$1`, id).Scan(&lastPulse, &lastActivity)
		act := time.Time{}
		if lastActivity != nil {
			act = *lastActivity
		}
		if iv := Interval(cfg.IntervalMin, act, local, q); iv > 0 && (lastPulse == nil || now.Sub(*lastPulse) >= iv) {
			if _, err := e.StartPulse(ctx, dot); err != nil {
				e.log().Warn("pulse", "dot", id, "err", err)
			}
		}
		// Digest zu den konfigurierten Uhrzeiten (einmal pro Slot).
		for _, dt := range cfg.DigestTimes {
			m := minutes(dt)
			cur := local.Hour()*60 + local.Minute()
			if cur >= m && cur < m+2 {
				key := local.Format("2006-01-02") + "@" + dt
				if ok, _ := e.AddSignal(ctx, id, Signal{Source: "digest", Kind: "slot", DedupKey: key}); ok {
					e.SendDigest(ctx, dot)
				}
			}
		}
	}
	e.runRoutines(ctx, now)
	_ = e.Runtime.ExpireApprovals(ctx)
}

func (e *Engine) runRoutines(ctx context.Context, now time.Time) {
	rows, err := e.Pool.Query(ctx, `SELECT s.id, s.dot_id, s.cron, s.prompt, s.tool_scope, s.target, s.next_run_at, coalesce(u.timezone,'Europe/Berlin')
		FROM schedules s JOIN dots d ON d.id=s.dot_id LEFT JOIN users u ON u.id=d.owner_user_id
		WHERE s.enabled AND s.kind='routine' AND d.status='active' AND (s.next_run_at IS NULL OR s.next_run_at <= $1)`, now)
	if err != nil {
		return
	}
	type sched struct {
		id, dot                 uuid.UUID
		cron, prompt, scope, tz string
		target                  []byte
		next                    *time.Time
	}
	var list []sched
	for rows.Next() {
		var s sched
		_ = rows.Scan(&s.id, &s.dot, &s.cron, &s.prompt, &s.scope, &s.target, &s.next, &s.tz)
		list = append(list, s)
	}
	rows.Close()
	for _, s := range list {
		c, err := ParseCron(s.cron)
		if err != nil {
			_, _ = e.Pool.Exec(ctx, `UPDATE schedules SET enabled=false WHERE id=$1`, s.id)
			continue
		}
		loc, err := time.LoadLocation(s.tz)
		if err != nil {
			loc = time.UTC
		}
		next := c.Next(now.In(loc))
		// Compare-and-set, damit bei mehreren Instanzen nur einer ausführt.
		tag, err := e.Pool.Exec(ctx, `UPDATE schedules SET next_run_at=$2 WHERE id=$1 AND next_run_at IS NOT DISTINCT FROM $3`, s.id, next, s.next)
		if err != nil || tag.RowsAffected() == 0 || s.next == nil {
			continue // Erstinitialisierung: nur next_run_at setzen
		}
		target := json.RawMessage(s.target)
		if string(target) == "{}" {
			target = nil
		}
		run := &runtime.Run{DotID: s.dot, Kind: runtime.KindRoutine, Scope: policy.Scope(s.scope), Tier: "worker",
			Input: runtime.Input{Text: s.prompt, Trust: "owner", Channel: "routine", Target: target}}
		if err := e.Runtime.Submit(ctx, run); err != nil {
			e.log().Warn("routine", "schedule", s.id, "err", err)
		}
	}
}

// Run startet den Scheduler.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		e.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
