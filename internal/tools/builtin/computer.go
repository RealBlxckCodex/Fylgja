package builtin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/platform/ids"
	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/tools"
)

func computer(c tools.Call) (Computer, error) {
	s, err := svc(c)
	if err != nil {
		return nil, err
	}
	if s.Computer == nil {
		return nil, errors.New("kein computer (sandbox) für diese fylgja konfiguriert")
	}
	return s.Computer, nil
}

func registerComputer(r *tools.Registry) {
	r.MustRegister(&tools.Tool{Name: "shell.run", Class: policy.Compute, Base: true, Source: "builtin", Preview: "$ {cmd}",
		Description: "Führt ein Shell-Kommando im eigenen Computer (Sandbox) aus. Arbeitsverzeichnis /home/dot/workspace.",
		Schema:      obj(`"cmd":{"type":"string"},"cwd":{"type":"string"},"timeout_s":{"type":"integer"}`, "cmd"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			cp, err := computer(c)
			if err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			a, _ := args[struct {
				Cmd      string `json:"cmd"`
				Cwd      string `json:"cwd"`
				TimeoutS int    `json:"timeout_s"`
			}](c)
			to := time.Duration(a.TimeoutS) * time.Second
			if to <= 0 || to > 30*time.Minute {
				to = 2 * time.Minute
			}
			out, errOut, code, err := cp.Exec(ctx, dotID(c), a.Cmd, a.Cwd, to)
			if err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			res := fmt.Sprintf("exit=%d\n%s", code, out)
			if errOut != "" {
				res += "\n[stderr]\n" + errOut
			}
			// Ausgaben von Programmen gelten als untrusted (können fremde Inhalte enthalten).
			return tools.Result{Content: res, IsError: code != 0, Untrusted: true, Source: "shell"}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "fs.read", Class: policy.Read, Base: true, Idempotent: true, Source: "builtin",
		Description: "Liest eine Datei aus dem eigenen Workspace.",
		Schema:      obj(`"path":{"type":"string"}`, "path"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			cp, err := computer(c)
			if err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			a, _ := args[map[string]string](c)
			b, err := cp.ReadFile(ctx, dotID(c), a["path"])
			if err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			return tools.Result{Content: string(b), Untrusted: true, Source: "file:" + a["path"]}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "fs.write", Class: policy.WriteInternal, Base: true, Source: "builtin", Preview: "Datei {path} schreiben",
		Description: "Schreibt eine Datei in den eigenen Workspace (überschreibt).",
		Schema:      obj(`"path":{"type":"string"},"content":{"type":"string"}`, "path", "content"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			cp, err := computer(c)
			if err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			a, _ := args[map[string]string](c)
			if err := cp.WriteFile(ctx, dotID(c), a["path"], []byte(a["content"])); err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			return tools.Result{Content: fmt.Sprintf("%d bytes geschrieben", len(a["content"]))}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "fs.list", Class: policy.Read, Base: true, Idempotent: true, Source: "builtin",
		Description: "Listet ein Verzeichnis im eigenen Workspace.",
		Schema:      obj(`"path":{"type":"string"}`),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			cp, err := computer(c)
			if err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			a, _ := args[map[string]string](c)
			list, err := cp.List(ctx, dotID(c), firstNonEmpty(a["path"], "."))
			if err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			var sb strings.Builder
			for _, f := range list {
				kind := "f"
				if f.Dir {
					kind = "d"
				}
				fmt.Fprintf(&sb, "%s %8d %s %s\n", kind, f.Size, f.MTime.Format("2006-01-02 15:04"), f.Name)
			}
			return tools.Result{Content: sb.String()}, nil
		}})
	browser := func(name, desc, props string, req ...string) {
		action := strings.TrimPrefix(name, "browser.")
		r.MustRegister(&tools.Tool{Name: name, Class: policy.Read, Source: "builtin", Idempotent: action == "snapshot" || action == "screenshot",
			Description: desc, Schema: obj(props, req...),
			Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
				cp, err := computer(c)
				if err != nil {
					return tools.Result{Content: err.Error(), IsError: true}, nil
				}
				a, _ := args[map[string]any](c)
				out, egress, err := cp.Browser(ctx, dotID(c), action, a)
				if err != nil {
					return tools.Result{Content: err.Error(), IsError: true}, nil
				}
				return tools.Result{Content: out, Untrusted: true, Source: "browser", Egress: egress}, nil
			}})
	}
	browser("browser.navigate", "Öffnet eine URL im Browser des eigenen Computers und liefert den Accessibility-Snapshot.", `"url":{"type":"string"}`, "url")
	browser("browser.snapshot", "Liefert den aktuellen Accessibility-Tree der Seite (mit Element-Referenzen).", ``)
	browser("browser.click", "Klickt ein Element (ref aus dem Snapshot).", `"ref":{"type":"string"},"element":{"type":"string"}`, "ref")
	browser("browser.type", "Tippt Text in ein Element (ref aus dem Snapshot). Keine Passwörter – dafür browser.login.", `"ref":{"type":"string"},"text":{"type":"string"},"submit":{"type":"boolean"}`, "ref", "text")
	browser("browser.screenshot", "Screenshot der Seite (für Vision).", ``)
	// Formularabsendungen auf fremden Seiten sind externe Änderungen.
	if t, ok := r.Get("browser.click"); ok {
		t.Class = policy.WriteExternal
		t.Preview = "Klick auf {element}"
	}
	if t, ok := r.Get("browser.type"); ok {
		t.Class = policy.WriteExternal
		t.Preview = "Eingabe in {ref}"
	}
	registerDesktop(r)
	r.MustRegister(&tools.Tool{Name: "browser.login", Class: policy.WriteExternal, Source: "builtin", Preview: "Login auf {site} mit hinterlegtem Zugang",
		Description: "Meldet sich mit einem hinterlegten Zugang (Credential-ID) auf einer Seite an. Das Passwort sieht das Modell nie; der Zugang ist an erlaubte Domains gebunden.",
		Schema:      obj(`"credential_id":{"type":"string"},"site":{"type":"string"}`, "credential_id", "site"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			cp, err := computer(c)
			if err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			a, _ := args[map[string]string](c)
			cid, err := uuid.Parse(a["credential_id"])
			if err != nil {
				return tools.Result{Content: "ungültige credential_id", IsError: true}, nil
			}
			out, err := cp.Login(ctx, dotID(c), cid, a["site"])
			if err != nil {
				return tools.Result{Content: err.Error(), IsError: true}, nil
			}
			return tools.Result{Content: out, Source: "browser.login"}, nil
		}})
}

func registerDelegation(r *tools.Registry) {
	r.MustRegister(&tools.Tool{Name: "dot.delegate", Class: policy.WriteInternal, Source: "builtin", Preview: "Aufgabe an {target}: {title}",
		Description: "Übergibt eine Aufgabe an eine andere Fylgja (Teammitglied). Sie arbeitet mit ihren eigenen Rechten. Kontext als Zusammenfassung, nie als Memory-Dump.",
		Schema:      obj(`"target":{"type":"string","description":"Name oder ID der Fylgja"},"title":{"type":"string"},"goal":{"type":"string"},"context":{"type":"string"}`, "target", "title", "goal"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			a, _ := args[map[string]string](c)
			var target uuid.UUID
			err = s.Pool.QueryRow(ctx, `SELECT d.id FROM dots d JOIN dots me ON me.workspace_id=d.workspace_id
				WHERE me.id=$1 AND (d.id::text=$2 OR lower(d.name)=lower($2)) AND d.status='active' AND d.id<>me.id LIMIT 1`, dotID(c), a["target"]).Scan(&target)
			if err != nil {
				return tools.Result{Content: "fylgja " + a["target"] + " nicht gefunden", IsError: true}, nil
			}
			task := ids.New()
			if _, err := s.Pool.Exec(ctx, `INSERT INTO tasks (id, dot_id, title, goal, status, priority, created_by, plan) VALUES ($1,$2,$3,$4,'queued',0,$5,$6)`,
				task, target, a["title"], a["goal"], "dot:"+c.Env.DotID, map[string]any{"context": a["context"], "delegated_by": c.Env.DotID}); err != nil {
				return tools.Result{}, err
			}
			_, _ = s.Pool.Exec(ctx, `INSERT INTO dot_messages (id, from_dot, to_dot, kind, body) VALUES ($1,$2,$3,'assign',$4)`, ids.New(), dotID(c), target,
				map[string]any{"task_id": task, "title": a["title"]})
			return tools.Result{Content: "delegiert als aufgabe " + task.String()}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "skills.list", Class: policy.Read, Idempotent: true, Source: "builtin",
		Description: "Listet verfügbare Skills.", Schema: obj(``),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			rows, err := s.Pool.Query(ctx, `SELECT name, description FROM skills WHERE status='active' AND (dot_id IS NULL OR dot_id=$1)
				AND workspace_id=(SELECT workspace_id FROM dots WHERE id=$1) ORDER BY name`, dotID(c))
			if err != nil {
				return tools.Result{}, err
			}
			defer rows.Close()
			var sb strings.Builder
			for rows.Next() {
				var n, d string
				_ = rows.Scan(&n, &d)
				fmt.Fprintf(&sb, "- %s: %s\n", n, d)
			}
			return tools.Result{Content: firstNonEmpty(sb.String(), "keine skills")}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "skills.read", Class: policy.Read, Idempotent: true, Source: "builtin",
		Description: "Liest den Volltext eines aktiven Skills.", Schema: obj(`"name":{"type":"string"}`, "name"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			a, _ := args[map[string]string](c)
			var body string
			err = s.Pool.QueryRow(ctx, `SELECT body_md FROM skills WHERE name=$2 AND status='active' AND (dot_id IS NULL OR dot_id=$1)
				AND workspace_id=(SELECT workspace_id FROM dots WHERE id=$1) ORDER BY version DESC LIMIT 1`, dotID(c), a["name"]).Scan(&body)
			if err != nil {
				return tools.Result{Content: "skill nicht gefunden oder nicht aktiv", IsError: true}, nil
			}
			return tools.Result{Content: body}, nil
		}})
	r.MustRegister(&tools.Tool{Name: "skills.propose", Class: policy.WriteInternal, Source: "builtin",
		Description: "Schlägt einen neuen Skill vor (Markdown). Aktivierung nur nach Freigabe durch den Owner.",
		Schema:      obj(`"name":{"type":"string"},"description":{"type":"string"},"body_md":{"type":"string"},"tools":{"type":"array","items":{"type":"string"}},"egress":{"type":"array","items":{"type":"string"}}`, "name", "description", "body_md"),
		Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			s, err := svc(c)
			if err != nil {
				return tools.Result{}, err
			}
			a, _ := args[map[string]any](c)
			_, err = s.Pool.Exec(ctx, `INSERT INTO proposals (id, dot_id, type, payload, evidence) VALUES ($1,$2,'skill',$3,$4)`, ids.New(), dotID(c), a,
				[]map[string]any{{"run_id": c.Env.RunID}})
			if err != nil {
				return tools.Result{}, err
			}
			return tools.Result{Content: "skill-vorschlag angelegt; der owner prüft und aktiviert ihn."}, nil
		}})
}

// registerDesktop: Der Bildschirm des eigenen Computers. Bildschirminhalt ist nicht vertrauenswürdig
// (eine Webseite oder ein Dokument kann Anweisungen enthalten) und taintet daher den Lauf.
func registerDesktop(r *tools.Registry) {
	const num = `{"type":"integer"}`
	def := func(name, desc string, class policy.Class, props, preview string, required ...string) {
		action := strings.TrimPrefix(name, "desktop.")
		r.MustRegister(&tools.Tool{Name: name, Class: class, Source: "builtin", Description: desc, Schema: obj(props, required...), Preview: preview,
			Base: action != "drag" && action != "wait",
			Handler: func(ctx context.Context, c tools.Call) (tools.Result, error) {
				cp, err := computer(c)
				if err != nil {
					return tools.Result{Content: err.Error(), IsError: true}, nil
				}
				a, _ := args[map[string]any](c)
				if a == nil {
					a = map[string]any{}
				}
				act := action
				if action == "click" {
					b, _ := a["button"].(string)
					dbl, _ := a["double"].(bool)
					switch {
					case b == "right":
						act = "right_click"
					case b == "middle":
						act = "middle_click"
					case dbl:
						act = "double_click"
					}
				}
				text, img, err := cp.Desktop(ctx, dotID(c), act, a)
				if err != nil {
					return tools.Result{Content: err.Error(), IsError: true}, nil
				}
				res := tools.Result{Content: text, Untrusted: true, Source: "desktop"}
				if img != "" {
					res.Images = []string{img}
				}
				return res, nil
			}})
	}
	def("desktop.screenshot", "Bildschirmfoto des eigenen Desktops. Koordinaten für Klicks beziehen sich auf dieses Bild.", policy.Read, ``, "Bildschirmfoto")
	def("desktop.click", "Klickt auf Bildschirmkoordinaten (x,y). button: left|right|middle, double: Doppelklick. Liefert danach ein neues Bildschirmfoto.", policy.Compute,
		`"x":`+num+`,"y":`+num+`,"button":{"type":"string","enum":["left","right","middle"]},"double":{"type":"boolean"}`, "Klick bei ({x},{y})", "x", "y")
	def("desktop.drag", "Zieht mit gedrückter linker Maustaste von (x,y) nach (to_x,to_y).", policy.Compute,
		`"x":`+num+`,"y":`+num+`,"to_x":`+num+`,"to_y":`+num, "Ziehen ({x},{y}) → ({to_x},{to_y})", "x", "y", "to_x", "to_y")
	def("desktop.type", "Tippt Text in das Fenster mit dem Fokus. Keine Passwörter tippen: dafür browser.login.", policy.Compute,
		`"text":{"type":"string"}`, "Tippen: {text}", "text")
	def("desktop.key", "Drückt Tasten oder Kombinationen, durch Leerzeichen getrennt, z. B. \"ctrl+l\", \"Return\", \"alt+Tab\".", policy.Compute,
		`"keys":{"type":"string"}`, "Tasten: {keys}", "keys")
	def("desktop.scroll", "Scrollt am Mauszeiger (optional an (x,y)). dy>0 nach unten, dy<0 nach oben, dx für seitlich.", policy.Compute,
		`"x":`+num+`,"y":`+num+`,"dx":`+num+`,"dy":`+num, "Scrollen")
	def("desktop.wait", "Wartet bis zu 10 Sekunden (ms), z. B. bis eine Seite geladen ist, und liefert dann ein Bildschirmfoto.", policy.Read,
		`"ms":`+num, "Warten")
}
