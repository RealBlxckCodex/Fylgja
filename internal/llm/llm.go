// Package llm normalisiert Modellaufrufe (Streaming, Tool-Calling, Vision) über Provider hinweg.
//
// Adapter: OpenAI-kompatibel (vLLM, Ollama, llama.cpp, externe APIs) und Anthropic-nativ.
// Der Inference-Router (internal/router) implementiert ebenfalls Client und ist das,
// was die Runtime benutzt; Agents sprechen nur logische Modelle.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type Role string

const (
	System    Role = "system"
	User      Role = "user"
	Assistant Role = "assistant"
	Tool      Role = "tool"
)

// Message ist eine normalisierte Chat-Nachricht.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Images     []string   `json:"images,omitempty"` // data:- oder https-URLs
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	// CacheBreak markiert das Ende eines stabilen Präfixes (Prompt-Caching, 8.3).
	CacheBreak bool `json:"cache_break,omitempty"`
}

// ToolCall ist ein vom Modell angeforderter Werkzeugaufruf.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolDef beschreibt ein Werkzeug für das Modell (JSON-Schema).
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Priority-Klassen des Routers (16.5).
type Priority string

const (
	Interactive Priority = "interactive"
	TaskPrio    Priority = "task"
	ReviewPrio  Priority = "review"
	Background  Priority = "background"
)

// Privacy-Anforderung eines Runs (16.7).
type Privacy string

const (
	SelfHostedOnly Privacy = "self_hosted_only"
	EUOnly         Privacy = "eu_only"
	AnyPrivacy     Privacy = "any"
)

// Meta trägt Routing-Informationen, die nicht an den Provider gehen.
type Meta struct {
	RunID       string   `json:"run_id,omitempty"`
	DotID       string   `json:"dot_id,omitempty"`
	WorkspaceID string   `json:"workspace_id,omitempty"`
	Tier        string   `json:"tier,omitempty"`
	Priority    Priority `json:"priority,omitempty"`
	Privacy     Privacy  `json:"privacy,omitempty"`
	PrefixHash  string   `json:"prefix_hash,omitempty"`
	// NoDegrade verbietet Fallback auf schwächere Modelle (Reviewer, 16.6).
	NoDegrade bool `json:"no_degrade,omitempty"`
}

// Request ist eine Chat-Anfrage.
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	JSONMode    bool      `json:"json_mode,omitempty"`
	Meta        Meta      `json:"-"`
}

// Usage zählt Tokens.
type Usage struct {
	In     int `json:"in"`
	Out    int `json:"out"`
	Cached int `json:"cached"`
}

// Response ist die vollständige Antwort.
type Response struct {
	Message      Message `json:"message"`
	Usage        Usage   `json:"usage"`
	FinishReason string  `json:"finish_reason"`
	Model        string  `json:"model"`
	// Deployment, das die Anfrage bedient hat (vom Router gesetzt).
	Deployment   string `json:"deployment,omitempty"`
	CostMicroEUR int64  `json:"cost_micro_eur,omitempty"`
	LatencyMS    int    `json:"latency_ms,omitempty"`
	Degraded     bool   `json:"degraded,omitempty"`
}

// DeltaFunc empfängt Text-Deltas beim Streaming. Nil = kein Streaming.
type DeltaFunc func(delta string)

// Client ist die gemeinsame Schnittstelle.
type Client interface {
	Chat(ctx context.Context, req Request, onDelta DeltaFunc) (*Response, error)
}

// Embedder erzeugt Vektoren.
type Embedder interface {
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}

// APIError ist ein Provider-Fehler mit Statuscode.
type APIError struct {
	Status int
	Body   string
	// BeforeFirstToken: Fehler trat auf, bevor ein Token gestreamt wurde (Router darf umlenken).
	BeforeFirstToken bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm: http %d: %s", e.Status, truncate(e.Body, 300))
}

// Retryable meldet transiente Fehler (Timeout/5xx/429).
func Retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 429 || ae.Status >= 500
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
