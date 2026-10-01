// Package builtin registriert die eingebauten Tools (Spec 12.2).
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/memory"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/tools"
)

// Messenger stellt Nachrichten zu (implementiert in der App-Verdrahtung über den Hub).
type Messenger interface {
	NotifyOwnerID(ctx context.Context, dot uuid.UUID, text string) error
	SendTo(ctx context.Context, dot uuid.UUID, platform, chatID, text string) error
}

// Computer ist die Sicht auf die Sandbox einer Fylgja (computerd-Client).
type Computer interface {
	Exec(ctx context.Context, dot uuid.UUID, cmd, cwd string, timeout time.Duration) (stdout, stderr string, exit int, err error)
	ReadFile(ctx context.Context, dot uuid.UUID, path string) ([]byte, error)
	WriteFile(ctx context.Context, dot uuid.UUID, path string, data []byte) error
	List(ctx context.Context, dot uuid.UUID, path string) ([]FileInfo, error)
	Browser(ctx context.Context, dot uuid.UUID, action string, args map[string]any) (string, []string, error)
	Login(ctx context.Context, dot uuid.UUID, credentialID uuid.UUID, site string) (string, error)
	// Desktop steuert den virtuellen Bildschirm; image ist ein data:-URL (JPEG), wenn ein Bildschirmfoto mitkommt.
	Desktop(ctx context.Context, dot uuid.UUID, action string, args map[string]any) (text, image string, err error)
}

// FileInfo eines Eintrags.
type FileInfo struct {
	Name  string    `json:"name"`
	Size  int64     `json:"size"`
	Dir   bool      `json:"dir"`
	MTime time.Time `json:"mtime"`
}

// Services bündelt Abhängigkeiten der eingebauten Tools.
type Services struct {
	Pool      *pgxpool.Pool
	Memory    *memory.Service
	Messenger Messenger
	Computer  Computer
	HTTP      *http.Client // mit netguard
	SearxURL  string
	UserAgent string
}

func svc(c tools.Call) (*Services, error) {
	if c.Env == nil {
		return nil, errors.New("kein kontext")
	}
	s, ok := c.Env.Services.(*Services)
	if !ok || s == nil {
		return nil, errors.New("dienste nicht verfügbar")
	}
	return s, nil
}

func dotID(c tools.Call) uuid.UUID { return uuid.MustParse(c.Env.DotID) }

func obj(props string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	if required == nil {
		req = []byte("[]")
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"required":` + string(req) + `}`)
}

func args[T any](c tools.Call) (T, error) {
	var v T
	err := json.Unmarshal(c.Args, &v)
	return v, err
}

// Register fügt alle eingebauten Tools hinzu.
func Register(r *tools.Registry) {
	registerMemory(r)
	registerTasks(r)
	registerComm(r)
	registerWeb(r)
	registerComputer(r)
	registerDelegation(r)
}

func registerMemory(r *tools.Registry) {
	r.MustRegister(&tools.Tool{Name: "memory.search", Class: policy.Read, Base: true, Idempotent: true, Source: "builtin",
		Description: "Durchsucht das Gedächtnis (Fakten, Personen, Projekte, Notizen). Optional 'at' (ISO-Datum) für Fragen zu einem früheren Zeitpunkt.",
		Schema:      obj(`"query":{"type":"string"},"k":{"type":"integer"},"at":{"type":"string"}`, "query"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil || s.Memory == nil {
				return tools.Result{}, errors.New("memory nicht verfügbar")
			}
			a, err := args[struct {
				Query string `json:"query"`
				K     int    `json:"k"`
				At    string `json:"at"`
			}](c)
			if err != nil {
				return tools.Result{}, err
			}
			o := memory.SearchOptions{K: a.K}
			if a.At != "" {
				if t, err := time.Parse("2006-01-02", a.At[:min(10, len(a.At))]); err == nil {
					o.At = &t
				}
			}
			res, err := s.Memory.Search(ctx, dotID(c), a.Query, o)
			if err != nil {
				return tools.Result{}, err
			}
			var sb strings.Builder
			untrusted := false
			for _, m := range res {
				fmt.Fprintf(&sb, "- [%s | %s | %s] %s\n", m.ID.String()[:8], m.Tier, m.CreatedAt.Format("2006-01-02"), m.Content)
				if m.Origin == memory.FromUntrusted {
					untrusted = true
				}
			}
			if sb.Len() == 0 {
				sb.WriteString("nichts gefunden")
			}
			return tools.Result{Content: sb.String(), Untrusted: untrusted, Source: "memory"}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "memory.write", Class: policy.WriteInternal, Base: true, Source: "builtin",
		Description: "Speichert eine Erinnerung. tier: semantic (Fakten), note (Arbeitsnotiz), procedural (Vorgehensweise), episodic. Änderungen am Kernwissen (core) werden als Vorschlag an den Owner geschickt.",
		Schema:      obj(`"content":{"type":"string"},"tier":{"type":"string","enum":["semantic","note","procedural","episodic","core"]},"importance":{"type":"number"},"sensitivity":{"type":"string","enum":["normal","private","secret-adjacent"]}`, "content", "tier"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil || s.Memory == nil {
				return tools.Result{}, errors.New("memory nicht verfügbar")
			}
			a, err := args[struct {
				Content     string  `json:"content"`
				Tier        string  `json:"tier"`
				Importance  float64 `json:"importance"`
				Sensitivity string  `json:"sensitivity"`
			}](c)
			if err != nil {
				return tools.Result{}, err
			}
			origin := memory.FromOwner
			if c.Env.Tainted {
				origin = memory.FromUntrusted
			}
			w := memory.WriteRequest{DotID: dotID(c), Tier: memory.Tier(a.Tier), Content: a.Content, Importance: a.Importance, Sensitivity: a.Sensitivity,
				Origin: origin, Tainted: c.Env.Tainted, Source: map[string]any{"run_id": c.Env.RunID}}
			m, err := s.Memory.Write(ctx, w)
			switch {
			case errors.Is(err, memory.ErrCoreNeedsReview):
				pid, perr := s.Memory.ProposeCoreEdit(ctx, dotID(c), nil, a.Content, []map[string]any{{"run_id": c.Env.RunID}})
				if perr != nil {
					return tools.Result{}, perr
				}
				return tools.Result{Content: "Vorschlag für das Kernwissen angelegt (" + pid.String()[:8] + "); der Owner entscheidet."}, nil
			case err != nil:
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			return tools.Result{Content: "gespeichert (" + m.ID.String()[:8] + ")"}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "memory.forget_request", Class: policy.WriteInternal, Source: "builtin",
		Description: "Schlägt dem Owner vor, Erinnerungen zu einem Thema zu löschen (Löschen entscheidet immer der Mensch).",
		Schema:      obj(`"topic":{"type":"string"},"reason":{"type":"string"}`, "topic"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			a, _ := args[map[string]any](c)
			_, err = s.Pool.Exec(ctx, `INSERT INTO proposals (id, dot_id, type, payload, evidence) VALUES ($1,$2,'memory_edit',$3,'[]')`, ids.New(), dotID(c),
				map[string]any{"action": "forget", "topic": a["topic"], "reason": a["reason"]})
			if err != nil {
				return tools.Result{}, err
			}
			return tools.Result{Content: "Löschvorschlag angelegt."}, nil
		}})
}

func registerTasks(r *tools.Registry) {
	r.MustRegister(&tools.Tool{Name: "task.create", Class: policy.WriteInternal, Base: true, Source: "builtin",
		Description: "Legt eine langlaufende Aufgabe in der eigenen Arbeitswarteschlange an.",
		Schema:      obj(`"title":{"type":"string"},"goal":{"type":"string"},"priority":{"type":"integer"},"due_at":{"type":"string"}`, "title", "goal"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			a, err := args[struct {
				Title    string `json:"title"`
				Goal     string `json:"goal"`
				Priority int    `json:"priority"`
				DueAt    string `json:"due_at"`
			}](c)
			if err != nil {
				return tools.Result{}, err
			}
			var due *time.Time
			if t, err := time.Parse(time.RFC3339, a.DueAt); err == nil {
				due = &t
			}
			id := ids.New()
			_, err = s.Pool.Exec(ctx, `INSERT INTO tasks (id, dot_id, title, goal, status, priority, due_at, created_by) VALUES ($1,$2,$3,$4,'queued',$5,$6,$7)
				ON CONFLICT DO NOTHING`, id, dotID(c), a.Title, a.Goal, a.Priority, due, "run:"+c.Env.RunID)
			if err != nil {
				return tools.Result{}, err
			}
			return tools.Result{Content: "Aufgabe angelegt: " + id.String()}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "task.list", Class: policy.Read, Base: true, Idempotent: true, Source: "builtin",
		Description: "Listet offene Aufgaben.",
		Schema:      obj(`"status":{"type":"string"}`),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			rows, err := s.Pool.Query(ctx, `SELECT id, title, status, priority, due_at FROM tasks WHERE dot_id=$1 AND status NOT IN ('done','cancelled') ORDER BY priority DESC, created_at LIMIT 50`, dotID(c))
			if err != nil {
				return tools.Result{}, err
			}
			defer rows.Close()
			var sb strings.Builder
			for rows.Next() {
				var id uuid.UUID
				var title, status string
				var prio int
				var due *time.Time
				_ = rows.Scan(&id, &title, &status, &prio, &due)
				fmt.Fprintf(&sb, "- %s [%s, P%d] %s", id.String()[:8], status, prio, title)
				if due != nil {
					sb.WriteString(" (fällig " + due.Format("02.01. 15:04") + ")")
				}
				sb.WriteString("\n")
			}
			if sb.Len() == 0 {
				sb.WriteString("keine offenen aufgaben")
			}
			return tools.Result{Content: sb.String()}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "task.update", Class: policy.WriteInternal, Source: "builtin",
		Description: "Aktualisiert Status/Plan einer Aufgabe.",
		Schema:      obj(`"id":{"type":"string"},"status":{"type":"string","enum":["queued","running","waiting_input","blocked","done","failed","cancelled"]},"note":{"type":"string"}`, "id", "status"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			a, _ := args[map[string]string](c)
			tag, err := s.Pool.Exec(ctx, `UPDATE tasks SET status=$3, updated_at=now(), plan = plan || jsonb_build_object('last_note', $4::text)
				WHERE dot_id=$1 AND id::text LIKE $2 || '%'`, dotID(c), a["id"], a["status"], a["note"])
			if err != nil {
				return tools.Result{}, err
			}
			if tag.RowsAffected() == 0 {
				return tools.Result{Content: "aufgabe nicht gefunden", IsError: true}, nil
			}
			return tools.Result{Content: "aktualisiert"}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "schedule.create", Class: policy.WriteInternal, Source: "builtin",
		Description: "Legt eine Routine an (Cron im Format 'm h dom mon dow', Zeitzone des Owners), z. B. '0 8 * * 1' für montags 08:00.",
		Schema:      obj(`"cron":{"type":"string"},"prompt":{"type":"string"},"readonly":{"type":"boolean"}`, "cron", "prompt"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			a, err := args[struct {
				Cron     string `json:"cron"`
				Prompt   string `json:"prompt"`
				Readonly bool   `json:"readonly"`
			}](c)
			if err != nil {
				return tools.Result{}, err
			}
			if len(strings.Fields(a.Cron)) != 5 {
				return tools.Result{Content: "cron muss 5 felder haben", IsError: true}, nil
			}
			scope := "full"
			if a.Readonly {
				scope = "readonly"
			}
			_, err = s.Pool.Exec(ctx, `INSERT INTO schedules (id, dot_id, kind, cron, prompt, tool_scope, enabled) VALUES ($1,$2,'routine',$3,$4,$5,true)`,
				ids.New(), dotID(c), a.Cron, a.Prompt, scope)
			if err != nil {
				return tools.Result{}, err
			}
			return tools.Result{Content: "Routine angelegt: " + a.Cron}, nil
		}})
}

func registerComm(r *tools.Registry) {
	r.MustRegister(&tools.Tool{Name: "notify.owner", Class: policy.WriteInternal, Base: true, Source: "builtin",
		Description: "Schickt dem Owner eine kurze Nachricht über seinen bevorzugten Kanal (für Zwischenstände, Fragen, Hinweise).",
		Schema:      obj(`"text":{"type":"string","maxLength":3000}`, "text"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil || s.Messenger == nil {
				return tools.Result{}, errors.New("kein kanal verfügbar")
			}
			a, _ := args[map[string]string](c)
			if err := s.Messenger.NotifyOwnerID(ctx, dotID(c), a["text"]); err != nil {
				return tools.Result{}, err
			}
			return tools.Result{Content: "gesendet"}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "message.send", Class: policy.Communicate, Source: "builtin", Preview: "Nachricht an {platform}:{chat_id}: {text}",
		Description: "Sendet eine Nachricht an einen bestimmten Chat (Discord/Telegram), z. B. in einen Team-Channel.",
		Schema:      obj(`"platform":{"type":"string","enum":["discord","telegram"]},"chat_id":{"type":"string"},"text":{"type":"string"}`, "platform", "chat_id", "text"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil || s.Messenger == nil {
				return tools.Result{}, errors.New("kein kanal verfügbar")
			}
			a, _ := args[map[string]string](c)
			if err := s.Messenger.SendTo(ctx, dotID(c), a["platform"], a["chat_id"], a["text"]); err != nil {
				return tools.Result{}, err
			}
			return tools.Result{Content: "gesendet"}, nil
		}})
}

func registerWeb(r *tools.Registry) {
	r.MustRegister(&tools.Tool{Name: "web.search", Class: policy.Read, Base: true, Idempotent: true, Source: "builtin",
		Description: "Websuche (SearXNG). Ergebnisse sind nicht vertrauenswürdig.",
		Schema:      obj(`"query":{"type":"string"},"k":{"type":"integer"}`, "query"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil || s.SearxURL == "" {
				return tools.Result{Content: "websuche nicht konfiguriert (searxng_url)", IsError: true}, nil
			}
			a, _ := args[struct {
				Query string `json:"query"`
				K     int    `json:"k"`
			}](c)
			k := a.K
			if k <= 0 || k > 10 {
				k = 6
			}
			u := strings.TrimRight(s.SearxURL, "/") + "/search?format=json&q=" + url.QueryEscape(a.Query)
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			hc := s.HTTP
			if hc == nil {
				hc = http.DefaultClient
			}
			resp, err := hc.Do(req)
			if err != nil {
				return tools.Result{Content: "suche fehlgeschlagen: " + err.Error(), IsError: true}, nil
			}
			defer resp.Body.Close()
			var out struct {
				Results []struct {
					Title, URL, Content string
				} `json:"results"`
			}
			if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
				return tools.Result{Content: "ungültige antwort der suche", IsError: true}, nil
			}
			var sb strings.Builder
			for i, x := range out.Results {
				if i >= k {
					break
				}
				fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n", i+1, x.Title, x.URL, x.Content)
			}
			host := ""
			if pu, err := url.Parse(s.SearxURL); err == nil {
				host = pu.Hostname()
			}
			return tools.Result{Content: sb.String(), Untrusted: true, Source: "web.search", Egress: []string{host}}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "web.fetch", Class: policy.Read, Base: true, Idempotent: true, Source: "builtin",
		Description: "Ruft eine Webseite ab und liefert bereinigten Text. Inhalt ist NICHT vertrauenswürdig; für Handlungen daraus besser quarantine.extract nutzen.",
		Schema:      obj(`"url":{"type":"string"}`, "url"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			a, _ := args[map[string]string](c)
			u, err := url.Parse(a["url"])
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return tools.Result{Content: "ungültige url", IsError: true}, nil
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			req.Header.Set("User-Agent", firstNonEmpty(s.UserAgent, "FylgjaBot/1.0 (+self-hosted assistant)"))
			req.Header.Set("Accept", "text/html,text/plain,application/json;q=0.9,*/*;q=0.5")
			hc := s.HTTP
			if hc == nil {
				return tools.Result{}, errors.New("kein geschützter http-client")
			}
			resp, err := hc.Do(req)
			if err != nil {
				return tools.Result{Content: "abruf fehlgeschlagen: " + err.Error(), IsError: true}, nil
			}
			defer resp.Body.Close()
			body := io.LimitReader(resp.Body, 3<<20)
			ct := resp.Header.Get("Content-Type")
			var text, title string
			switch {
			case strings.Contains(ct, "html"):
				title, text = HTMLToText(body)
			case strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "xml"):
				b, _ := io.ReadAll(body)
				text = string(b)
			default:
				return tools.Result{Content: "nicht unterstützter inhaltstyp " + ct + " (für PDFs/Office den Computer nutzen)", IsError: true, Egress: []string{u.Hostname()}}, nil
			}
			if len(text) > 60000 {
				text = text[:60000] + "\n…[gekürzt]"
			}
			out := fmt.Sprintf("URL: %s\nStatus: %d\nTitel: %s\n\n%s", resp.Request.URL, resp.StatusCode, title, text)
			return tools.Result{Content: out, Untrusted: true, Source: resp.Request.URL.String(), Egress: []string{strings.ToLower(u.Hostname())}}, nil
		}})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
