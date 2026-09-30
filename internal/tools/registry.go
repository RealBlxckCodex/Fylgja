// Package tools ist die Tool-Registry mit Klassifikation (Spec 12.1, 8.6).
//
// Die Klasse eines Tools ist der zentrale Hebel der Policy-Engine. Der Executor kennt
// immer alle Tools und prüft Berechtigungen unabhängig davon, ob ein Tool im Prompt sichtbar ist.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/policy"
)

// Env gibt Tools Zugriff auf Laufzeitkontext, ohne dass sie die Runtime kennen.
type Env struct {
	WorkspaceID string
	DotID       string
	RunID       string
	TaskID      string
	Tainted     bool
	Scope       policy.Scope
	// Services, die eingebaute Tools nutzen (nil = nicht verfügbar).
	Services any
}

// Call ist ein konkreter Tool-Aufruf.
type Call struct {
	ID             string          `json:"id"`
	Tool           string          `json:"tool"`
	Args           json.RawMessage `json:"args"`
	IdempotencyKey string          `json:"idempotency_key"`
	Env            *Env            `json:"-"`
}

// Result ist das Ergebnis eines Tool-Aufrufs.
type Result struct {
	Content string `json:"content"`
	// Untrusted: Inhalt stammt aus nicht vertrauenswürdiger Quelle → Run wird getaintet.
	Untrusted bool   `json:"untrusted"`
	Source    string `json:"source,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	// Data: strukturierte Daten (z. B. schema-validierte Subagent-Ergebnisse).
	Data      any      `json:"data,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
	// Egress: tatsächlich kontaktierte Domains (für Taint-Regel 2).
	Egress []string `json:"egress,omitempty"`
}

// Handler führt ein Tool aus.
type Handler func(ctx context.Context, c Call) (Result, error)

// Tool beschreibt ein Werkzeug inklusive Metadaten.
type Tool struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Class          policy.Class    `json:"class"`
	Idempotent     bool            `json:"idempotent"`
	Schema         json.RawMessage `json:"schema"`
	Preview        string          `json:"preview"`
	Egress         []string        `json:"egress"`
	Secrets        []string        `json:"secrets"`
	TaintSensitive bool            `json:"taint_sensitive"`
	// Base: gehört zum Basis-Set, das in jedem Run geladen wird.
	Base bool `json:"base"`
	// Source: builtin|mcp:<server>|connector:<kind>|openapi:<name>|skill:<name>
	Source  string  `json:"source"`
	Handler Handler `json:"-"`
	// Recipients/Domain aus Args extrahieren (für Policy-Kontext).
	Extract func(args map[string]any) (recipients []string, domain string, amountMicroEUR int64) `json:"-"`
}

// Def liefert die Modell-Definition.
func (t *Tool) Def() llm.ToolDef {
	desc := t.Description
	return llm.ToolDef{Name: t.Name, Description: desc, Parameters: t.Schema}
}

// Registry hält alle Tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

func NewRegistry() *Registry { return &Registry{tools: map[string]*Tool{}} }

var validName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// Register fügt ein Tool hinzu. Unbekannte Klassen werden konservativ zu write_external.
func (r *Registry) Register(t *Tool) error {
	if !validName.MatchString(t.Name) {
		return fmt.Errorf("tools: ungültiger name %q (erwartet gruppe.aktion)", t.Name)
	}
	if !t.Class.Valid() {
		t.Class = policy.WriteExternal
	}
	if len(t.Schema) == 0 {
		t.Schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var probe map[string]any
	if err := json.Unmarshal(t.Schema, &probe); err != nil {
		return fmt.Errorf("tools: schema von %s ungültig: %w", t.Name, err)
	}
	if t.Handler == nil {
		return fmt.Errorf("tools: %s ohne handler", t.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
	return nil
}

// MustRegister registriert oder paniced (nur für eingebaute Tools).
func (r *Registry) MustRegister(t *Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// All liefert alle Tools sortiert.
func (r *Registry) All() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BaseSet liefert die Tools, die in jedem Run sichtbar sind (8.6), gefiltert nach Scope.
func (r *Registry) BaseSet(scope policy.Scope) []*Tool {
	var out []*Tool
	for _, t := range r.All() {
		if t.Base && Visible(t, scope) {
			out = append(out, t)
		}
	}
	return out
}

// Visible: Im Readonly-Scope werden nur lesende Tools angezeigt (die Durchsetzung erfolgt trotzdem im Executor).
func Visible(t *Tool, scope policy.Scope) bool {
	switch scope {
	case policy.ScopeNone:
		return false
	case policy.ScopeReadonly:
		return t.Class == policy.Read || t.Class == policy.WriteInternal
	}
	return true
}

// Search implementiert tools.search: einfache Relevanz über Name/Beschreibung.
func (r *Registry) Search(query string, scope policy.Scope, limit int) []*Tool {
	if limit <= 0 {
		limit = 8
	}
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		t *Tool
		s int
	}
	var res []scored
	for _, t := range r.All() {
		if !Visible(t, scope) {
			continue
		}
		hay := strings.ToLower(t.Name + " " + t.Description)
		s := 0
		for _, term := range terms {
			if strings.Contains(strings.ToLower(t.Name), term) {
				s += 3
			} else if strings.Contains(hay, term) {
				s++
			}
		}
		if s > 0 {
			res = append(res, scored{t, s})
		}
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].s > res[j].s })
	out := make([]*Tool, 0, limit)
	for i := 0; i < len(res) && i < limit; i++ {
		out = append(out, res[i].t)
	}
	return out
}

// RenderPreview füllt das Preview-Template mit Argumenten ("Issue '{title}' in {repo}").
func RenderPreview(tpl string, args map[string]any) string {
	if tpl == "" {
		b, _ := json.Marshal(args)
		return string(b)
	}
	re := regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)
	return re.ReplaceAllStringFunc(tpl, func(m string) string {
		k := m[1 : len(m)-1]
		if v, ok := args[k]; ok {
			return fmt.Sprint(v)
		}
		return m
	})
}
