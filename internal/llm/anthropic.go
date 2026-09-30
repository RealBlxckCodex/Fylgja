package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Anthropic spricht die native Messages-API.
type Anthropic struct {
	BaseURL string // Default https://api.anthropic.com
	APIKey  string
	Version string // anthropic-version Header
	HTTP    *http.Client
}

func (a *Anthropic) base() string {
	if a.BaseURL == "" {
		return "https://api.anthropic.com"
	}
	return strings.TrimRight(a.BaseURL, "/")
}

func (a *Anthropic) build(req Request, stream bool) map[string]any {
	var system []map[string]any
	var msgs []map[string]any
	for _, m := range req.Messages {
		switch m.Role {
		case System:
			blk := map[string]any{"type": "text", "text": m.Content}
			if m.CacheBreak {
				blk["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			system = append(system, blk)
		case User:
			var content []map[string]any
			for _, img := range m.Images {
				if strings.HasPrefix(img, "data:") {
					mt, data, _ := strings.Cut(strings.TrimPrefix(img, "data:"), ";base64,")
					content = append(content, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": mt, "data": data}})
				} else {
					content = append(content, map[string]any{"type": "image", "source": map[string]string{"type": "url", "url": img}})
				}
			}
			blk := map[string]any{"type": "text", "text": m.Content}
			if m.CacheBreak {
				blk["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			content = append(content, blk)
			msgs = appendMerged(msgs, "user", content)
		case Assistant:
			var content []map[string]any
			if m.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Arguments
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				content = append(content, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": args})
			}
			msgs = appendMerged(msgs, "assistant", content)
		case Tool:
			msgs = appendMerged(msgs, "user", []map[string]any{{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content}})
		}
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	b := map[string]any{"model": req.Model, "messages": msgs, "max_tokens": maxTokens, "stream": stream}
	if len(system) > 0 {
		b["system"] = system
	}
	if req.Temperature != nil {
		b["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for i, t := range req.Tools {
			params := t.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			td := map[string]any{"name": t.Name, "description": t.Description, "input_schema": params}
			if i == len(req.Tools)-1 {
				td["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			tools = append(tools, td)
		}
		b["tools"] = tools
	}
	return b
}

// appendMerged fasst aufeinanderfolgende Nachrichten gleicher Rolle zusammen (API verlangt Alternierung).
func appendMerged(msgs []map[string]any, role string, content []map[string]any) []map[string]any {
	if n := len(msgs); n > 0 && msgs[n-1]["role"] == role {
		msgs[n-1]["content"] = append(msgs[n-1]["content"].([]map[string]any), content...)
		return msgs
	}
	return append(msgs, map[string]any{"role": role, "content": content})
}

type antUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u antUsage) toUsage() Usage {
	return Usage{In: u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens, Out: u.OutputTokens, Cached: u.CacheReadInputTokens}
}

// Chat implementiert Client.
func (a *Anthropic) Chat(ctx context.Context, req Request, onDelta DeltaFunc) (*Response, error) {
	start := time.Now()
	buf, _ := json.Marshal(a.build(req, onDelta != nil))
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base()+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("content-type", "application/json")
	hreq.Header.Set("x-api-key", a.APIKey)
	v := a.Version
	if v == "" {
		v = "2023-06-01"
	}
	hreq.Header.Set("anthropic-version", v)
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		return nil, &APIError{Status: 503, Body: err.Error(), BeforeFirstToken: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &APIError{Status: resp.StatusCode, Body: string(b), BeforeFirstToken: true}
	}
	if onDelta == nil {
		var r struct {
			Model   string `json:"model"`
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
			StopReason string   `json:"stop_reason"`
			Usage      antUsage `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		msg := Message{Role: Assistant}
		for _, c := range r.Content {
			switch c.Type {
			case "text":
				msg.Content += c.Text
			case "tool_use":
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Input})
			}
		}
		return &Response{Message: msg, Usage: r.Usage.toUsage(), FinishReason: r.StopReason, Model: r.Model, LatencyMS: int(time.Since(start).Milliseconds())}, nil
	}
	return readAnthropicStream(resp.Body, onDelta, start)
}

func readAnthropicStream(body io.Reader, onDelta DeltaFunc, start time.Time) (*Response, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	out := &Response{Message: Message{Role: Assistant}}
	type block struct {
		typ, id, name string
		args          strings.Builder
	}
	blocks := map[int]*block{}
	var order []int
	var text strings.Builder
	var usage antUsage
	got := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Model string   `json:"model"`
				Usage antUsage `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage antUsage `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			out.Model = ev.Message.Model
			usage = ev.Message.Usage
		case "content_block_start":
			blocks[ev.Index] = &block{typ: ev.ContentBlock.Type, id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
			order = append(order, ev.Index)
		case "content_block_delta":
			got = true
			b := blocks[ev.Index]
			if b == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				text.WriteString(ev.Delta.Text)
				onDelta(ev.Delta.Text)
			case "input_json_delta":
				b.args.WriteString(ev.Delta.PartialJSON)
			}
		case "message_delta":
			out.FinishReason = ev.Delta.StopReason
			usage.OutputTokens = ev.Usage.OutputTokens
		case "error":
			return nil, &APIError{Status: 529, Body: ev.Error.Message, BeforeFirstToken: !got}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, &APIError{Status: 502, Body: fmt.Sprint("stream: ", err), BeforeFirstToken: !got}
	}
	out.Message.Content = text.String()
	for _, i := range order {
		b := blocks[i]
		if b.typ == "tool_use" {
			out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: b.id, Name: b.name, Arguments: json.RawMessage(normArgs(b.args.String()))})
		}
	}
	out.Usage = usage.toUsage()
	out.LatencyMS = int(time.Since(start).Milliseconds())
	return out, nil
}
