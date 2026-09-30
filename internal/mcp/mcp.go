// Package mcp bindet MCP-Server als Tool-Quellen an (Spec 12.3 "MCP-first").
// Fremde Tools werden konservativ klassifiziert (12.1): im Zweifel write_external.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/tools"
)

// ServerConfig beschreibt einen MCP-Server.
type ServerConfig struct {
	Name    string            `json:"name" yaml:"name"`
	Command []string          `json:"command,omitempty" yaml:"command"` // stdio (läuft in der Sandbox bzw. lokal)
	URL     string            `json:"url,omitempty" yaml:"url"`         // Streamable HTTP
	Headers map[string]string `json:"-" yaml:"-"`
	// Overrides: Tool → Klasse (Admin-Überschreibung der Auto-Klassifikation).
	Overrides map[string]policy.Class `json:"overrides,omitempty" yaml:"overrides"`
	Egress    []string                `json:"egress,omitempty" yaml:"egress"`
}

// Client hält eine Verbindung zu einem MCP-Server.
type Client struct {
	cfg  ServerConfig
	mu   sync.Mutex
	sess *sdk.ClientSession
}

type headerRT struct {
	h    map[string]string
	base http.RoundTripper
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	for k, v := range h.h {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

// Connect verbindet (stdio oder HTTP).
func Connect(ctx context.Context, cfg ServerConfig) (*Client, error) {
	c := sdk.NewClient(&sdk.Implementation{Name: "fylgja", Version: "1.0"}, nil)
	var t sdk.Transport
	switch {
	case len(cfg.Command) > 0:
		t = &sdk.CommandTransport{Command: exec.Command(cfg.Command[0], cfg.Command[1:]...)}
	case cfg.URL != "":
		t = &sdk.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: &http.Client{Transport: headerRT{h: cfg.Headers, base: http.DefaultTransport}}}
	default:
		return nil, errors.New("mcp: command oder url nötig")
	}
	s, err := c.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %w", cfg.Name, err)
	}
	return &Client{cfg: cfg, sess: s}, nil
}

// Close beendet die Verbindung.
func (c *Client) Close() error { return c.sess.Close() }

// Call ruft ein Tool auf und liefert Text (+ Fehlerflag).
func (c *Client) Call(ctx context.Context, name string, args any) (string, bool, error) {
	res, err := c.sess.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", true, err
	}
	var sb strings.Builder
	for _, ct := range res.Content {
		switch v := ct.(type) {
		case *sdk.TextContent:
			sb.WriteString(v.Text)
			sb.WriteString("\n")
		case *sdk.ImageContent:
			fmt.Fprintf(&sb, "[bild %s, %d bytes]\n", v.MIMEType, len(v.Data))
		}
	}
	if res.StructuredContent != nil && sb.Len() == 0 {
		b, _ := json.Marshal(res.StructuredContent)
		sb.Write(b)
	}
	return strings.TrimSpace(sb.String()), res.IsError, nil
}

var (
	reRead     = regexp.MustCompile(`(?i)(^|[_.\-])(get|list|search|read|fetch|query|find|lookup|describe|show|view|snapshot|status)($|[_.\-])`)
	reComm     = regexp.MustCompile(`(?i)(send|post|message|email|mail|reply|comment|notify|publish|tweet|dm)`)
	reDestroy  = regexp.MustCompile(`(?i)(delete|remove|drop|destroy|purge|wipe|truncate|archive)`)
	reSpend    = regexp.MustCompile(`(?i)(pay|charge|purchase|buy|order|checkout|transfer|invoice)`)
	reCred     = regexp.MustCompile(`(?i)(password|credential|secret|2fa|mfa|recovery|api[_-]?key|permission|role|grant)`)
)

// Classify ordnet ein MCP-Tool konservativ einer Klasse zu.
func Classify(name string, ann *sdk.ToolAnnotations) policy.Class {
	switch {
	case reCred.MatchString(name):
		return policy.Credential
	case reSpend.MatchString(name):
		return policy.Spend
	case reDestroy.MatchString(name) || (ann != nil && ann.DestructiveHint != nil && *ann.DestructiveHint && !ann.ReadOnlyHint):
		return policy.Destructive
	case reComm.MatchString(name):
		return policy.Communicate
	case ann != nil && ann.ReadOnlyHint && reRead.MatchString(name):
		return policy.Read
	case ann != nil && ann.ReadOnlyHint:
		return policy.Read
	case reRead.MatchString(name):
		return policy.Read
	}
	return policy.WriteExternal
}

var nameClean = regexp.MustCompile(`[^a-z0-9_]+`)

// Register lädt alle Tools eines Servers in die Registry ("mcp_<server>.<tool>").
func Register(ctx context.Context, reg *tools.Registry, c *Client) (int, error) {
	res, err := c.sess.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		return 0, err
	}
	prefix := "mcp_" + nameClean.ReplaceAllString(strings.ToLower(c.cfg.Name), "_")
	n := 0
	for _, t := range res.Tools {
		remote := t.Name
		class := Classify(remote, t.Annotations)
		if o, ok := c.cfg.Overrides[remote]; ok {
			class = o
		}
		schema, _ := json.Marshal(t.InputSchema)
		if len(schema) == 0 || string(schema) == "null" {
			schema = []byte(`{"type":"object","properties":{}}`)
		}
		idem := t.Annotations != nil && t.Annotations.IdempotentHint
		tool := &tools.Tool{
			Name:        prefix + "." + nameClean.ReplaceAllString(strings.ToLower(remote), "_"),
			Description: fmt.Sprintf("[MCP %s] %s", c.cfg.Name, t.Description),
			Class:       class, Schema: schema, Idempotent: idem || class == policy.Read, Source: "mcp:" + c.cfg.Name, Egress: c.cfg.Egress,
			TaintSensitive: class.SideEffect(),
			Handler: func(ctx context.Context, call tools.Call) (tools.Result, error) {
				var args map[string]any
				_ = json.Unmarshal(call.Args, &args)
				out, isErr, err := c.Call(ctx, remote, args)
				if err != nil {
					return tools.Result{Content: err.Error(), IsError: true}, nil
				}
				// Ergebnisse fremder Server sind immer untrusted.
				return tools.Result{Content: out, IsError: isErr, Untrusted: true, Source: "mcp:" + c.cfg.Name}, nil
			},
		}
		if err := reg.Register(tool); err == nil {
			n++
		}
	}
	return n, nil
}
