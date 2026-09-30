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

// OpenAI spricht jede OpenAI-kompatible API (/v1/chat/completions, /v1/embeddings).
type OpenAI struct {
	BaseURL string // z. B. http://pod:8000/v1
	APIKey  string
	HTTP    *http.Client
	Headers map[string]string
}

func (o *OpenAI) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

type oaMsg struct {
	Role       string       `json:"role"`
	Content    any          `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Name       string       `json:"name,omitempty"`
}

type oaToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func toOAMessages(ms []Message) []oaMsg {
	out := make([]oaMsg, 0, len(ms))
	for _, m := range ms {
		om := oaMsg{Role: string(m.Role), ToolCallID: m.ToolCallID, Name: m.Name}
		if len(m.Images) > 0 {
			parts := []map[string]any{{"type": "text", "text": m.Content}}
			for _, img := range m.Images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": img}})
			}
			om.Content = parts
		} else {
			om.Content = m.Content
		}
		for _, tc := range m.ToolCalls {
			var c oaToolCall
			c.ID, c.Type = tc.ID, "function"
			c.Function.Name = tc.Name
			c.Function.Arguments = string(tc.Arguments)
			if c.Function.Arguments == "" {
				c.Function.Arguments = "{}"
			}
			om.ToolCalls = append(om.ToolCalls, c)
		}
		if m.Role == Assistant && len(m.ToolCalls) > 0 && m.Content == "" {
			om.Content = nil
		}
		out = append(out, om)
	}
	return out
}

func (o *OpenAI) body(req Request, stream bool) map[string]any {
	b := map[string]any{"model": req.Model, "messages": toOAMessages(req.Messages), "stream": stream}
	if stream {
		b["stream_options"] = map[string]any{"include_usage": true}
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			params := t.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": params}})
		}
		b["tools"] = tools
	}
	if req.Temperature != nil {
		b["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		b["max_tokens"] = req.MaxTokens
	}
	if req.JSONMode {
		b["response_format"] = map[string]string{"type": "json_object"}
	}
	return b
}

func (o *OpenAI) post(ctx context.Context, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	for k, v := range o.Headers {
		hreq.Header.Set(k, v)
	}
	resp, err := o.client().Do(hreq)
	if err != nil {
		return nil, &APIError{Status: 503, Body: err.Error(), BeforeFirstToken: true}
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &APIError{Status: resp.StatusCode, Body: string(b), BeforeFirstToken: true}
	}
	return resp, nil
}

type oaResp struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      oaMsg  `json:"message"`
		Delta        oaMsg  `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (r *oaResp) usage() Usage {
	if r.Usage == nil {
		return Usage{}
	}
	u := Usage{In: r.Usage.PromptTokens, Out: r.Usage.CompletionTokens}
	if r.Usage.PromptTokensDetails != nil {
		u.Cached = r.Usage.PromptTokensDetails.CachedTokens
	}
	return u
}

func contentString(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// Chat implementiert Client.
func (o *OpenAI) Chat(ctx context.Context, req Request, onDelta DeltaFunc) (*Response, error) {
	start := time.Now()
	resp, err := o.post(ctx, "/chat/completions", o.body(req, onDelta != nil))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if onDelta == nil {
		var r oaResp
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, fmt.Errorf("llm: decode: %w", err)
		}
		if len(r.Choices) == 0 {
			return nil, &APIError{Status: 502, Body: "keine choices"}
		}
		c := r.Choices[0]
		msg := Message{Role: Assistant, Content: contentString(c.Message.Content)}
		for _, tc := range c.Message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(normArgs(tc.Function.Arguments))})
		}
		return &Response{Message: msg, Usage: r.usage(), FinishReason: c.FinishReason, Model: r.Model, LatencyMS: int(time.Since(start).Milliseconds())}, nil
	}
	return o.readStream(resp.Body, onDelta, start)
}

func normArgs(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}

func (o *OpenAI) readStream(body io.Reader, onDelta DeltaFunc, start time.Time) (*Response, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	out := &Response{Message: Message{Role: Assistant}}
	var text strings.Builder
	type partial struct {
		id, name string
		args     strings.Builder
	}
	var calls []*partial
	gotToken := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var r oaResp
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			continue
		}
		if r.Model != "" {
			out.Model = r.Model
		}
		if r.Usage != nil {
			out.Usage = r.usage()
		}
		for _, c := range r.Choices {
			if s := contentString(c.Delta.Content); s != "" {
				gotToken = true
				text.WriteString(s)
				onDelta(s)
			}
			for _, tc := range c.Delta.ToolCalls {
				gotToken = true
				idx := len(calls)
				if tc.Index != nil {
					idx = *tc.Index
				} else if tc.ID == "" && len(calls) > 0 {
					idx = len(calls) - 1
				}
				for len(calls) <= idx {
					calls = append(calls, &partial{})
				}
				p := calls[idx]
				if tc.ID != "" {
					p.id = tc.ID
				}
				if tc.Function.Name != "" {
					p.name = tc.Function.Name
				}
				p.args.WriteString(tc.Function.Arguments)
			}
			if c.FinishReason != "" {
				out.FinishReason = c.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, &APIError{Status: 502, Body: "stream abgebrochen: " + err.Error(), BeforeFirstToken: !gotToken}
	}
	out.Message.Content = text.String()
	for i, p := range calls {
		id := p.id
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: id, Name: p.name, Arguments: json.RawMessage(normArgs(p.args.String()))})
	}
	out.LatencyMS = int(time.Since(start).Milliseconds())
	return out, nil
}

// Embed implementiert Embedder.
func (o *OpenAI) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	resp, err := o.post(ctx, "/embeddings", map[string]any{"model": model, "input": inputs})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	out := make([][]float32, len(inputs))
	for _, d := range r.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	return out, nil
}
