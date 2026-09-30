package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Scripted liefert vorgegebene Antworten nacheinander (deterministische Tests).
type Scripted struct {
	mu        sync.Mutex
	Responses []Response
	Requests  []Request
	// Func kann statt Responses dynamisch antworten.
	Func func(req Request) (*Response, error)
}

func (s *Scripted) Chat(_ context.Context, req Request, onDelta DeltaFunc) (*Response, error) {
	s.mu.Lock()
	s.Requests = append(s.Requests, req)
	var r *Response
	var err error
	if s.Func != nil {
		s.mu.Unlock()
		r, err = s.Func(req)
		s.mu.Lock()
	} else if len(s.Responses) == 0 {
		err = errors.New("scripted: keine antworten mehr")
	} else {
		cp := s.Responses[0]
		s.Responses = s.Responses[1:]
		r = &cp
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if r.Message.Role == "" {
		r.Message.Role = Assistant
	}
	if onDelta != nil && r.Message.Content != "" {
		for _, w := range strings.SplitAfter(r.Message.Content, " ") {
			onDelta(w)
		}
	}
	return r, nil
}

// Text ist eine Hilfsfunktion für eine reine Textantwort.
func Text(s string) Response { return Response{Message: Message{Role: Assistant, Content: s}, FinishReason: "stop"} }

// Call ist eine Hilfsfunktion für eine Tool-Call-Antwort.
func Call(id, name string, args any) Response {
	b, _ := json.Marshal(args)
	return Response{Message: Message{Role: Assistant, ToolCalls: []ToolCall{{ID: id, Name: name, Arguments: b}}}, FinishReason: "tool_calls"}
}

// Cassette zeichnet Antworten eines echten Clients auf bzw. spielt sie ab (Spec 20.3).
// Mode "replay" (Default in CI) schlägt fehl, wenn keine Aufnahme existiert.
type Cassette struct {
	Path  string
	Mode  string // record|replay
	Inner Client
	mu    sync.Mutex
	data  map[string]Response
}

func RequestKey(req Request) string {
	b, _ := json.Marshal(struct {
		M  string
		Ms []Message
		T  []ToolDef
		J  bool
	}{req.Model, req.Messages, req.Tools, req.JSONMode})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:12])
}

func (c *Cassette) load() {
	if c.data != nil {
		return
	}
	c.data = map[string]Response{}
	if b, err := os.ReadFile(c.Path); err == nil {
		_ = json.Unmarshal(b, &c.data)
	}
}

func (c *Cassette) Chat(ctx context.Context, req Request, onDelta DeltaFunc) (*Response, error) {
	c.mu.Lock()
	c.load()
	key := RequestKey(req)
	r, ok := c.data[key]
	c.mu.Unlock()
	if ok {
		if onDelta != nil && r.Message.Content != "" {
			onDelta(r.Message.Content)
		}
		return &r, nil
	}
	if c.Mode != "record" || c.Inner == nil {
		return nil, fmt.Errorf("cassette: keine aufnahme für %s in %s", key, c.Path)
	}
	resp, err := c.Inner.Chat(ctx, req, onDelta)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.data[key] = *resp
	b, _ := json.MarshalIndent(c.data, "", " ")
	_ = os.WriteFile(c.Path, b, 0o644)
	c.mu.Unlock()
	return resp, nil
}

// HashEmbedder ist ein deterministischer Embedder für Tests und den Betrieb ohne
// Embedding-Modell (Bag-of-Words-Hashing). Qualitativ schwach, aber stabil.
type HashEmbedder struct{ Dim int }

func (h HashEmbedder) Embed(_ context.Context, _ string, inputs []string) ([][]float32, error) {
	dim := h.Dim
	if dim == 0 {
		dim = 1024
	}
	out := make([][]float32, len(inputs))
	for i, s := range inputs {
		v := make([]float32, dim)
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == 'ä' || r == 'ö' || r == 'ü' || r == 'ß')
		}) {
			sum := sha256.Sum256([]byte(w))
			idx := int(sum[0])<<8 | int(sum[1])
			v[idx%dim] += 1
		}
		var n float32
		for _, x := range v {
			n += x * x
		}
		if n > 0 {
			inv := 1 / sqrt32(n)
			for j := range v {
				v[j] *= inv
			}
		}
		out[i] = v
	}
	return out, nil
}

func sqrt32(x float32) float32 {
	z := x
	for i := 0; i < 20; i++ {
		z = (z + x/z) / 2
	}
	return z
}
