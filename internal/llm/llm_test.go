package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIStreamingToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"stream":true`) {
			t.Errorf("kein stream: %s", b)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"model":"m","choices":[{"delta":{"content":"Hal"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"web.search","arguments":"{\"q\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := &OpenAI{BaseURL: srv.URL, APIKey: "k"}
	var deltas []string
	r, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: User, Content: "hi"}}}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.Content != "Hallo" || strings.Join(deltas, "") != "Hallo" {
		t.Fatalf("content %q", r.Message.Content)
	}
	if len(r.Message.ToolCalls) != 1 || r.Message.ToolCalls[0].Name != "web.search" || string(r.Message.ToolCalls[0].Arguments) != `{"q":"x"}` {
		t.Fatalf("toolcalls %+v", r.Message.ToolCalls)
	}
	if r.Usage != (Usage{In: 10, Out: 5, Cached: 4}) || r.FinishReason != "tool_calls" {
		t.Fatalf("usage %+v", r.Usage)
	}
}

func TestOpenAIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	_, err := (&OpenAI{BaseURL: srv.URL}).Chat(context.Background(), Request{Model: "m"}, nil)
	if !Retryable(err) {
		t.Fatal(err)
	}
}

func TestAnthropicNonStreamingAndBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		msgs := body["messages"].([]any)
		// Tool-Ergebnis muss als user/tool_result übertragen werden, Rollen alternieren.
		if len(msgs) != 3 || msgs[2].(map[string]any)["role"] != "user" {
			t.Errorf("messages %v", msgs)
		}
		if _, ok := body["system"]; !ok {
			t.Error("system fehlt")
		}
		w.Write([]byte(`{"model":"claude","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"t1","name":"fs.read","input":{"path":"/a"}}],"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":7}}`))
	}))
	defer srv.Close()
	c := &Anthropic{BaseURL: srv.URL, APIKey: "k"}
	r, err := c.Chat(context.Background(), Request{Model: "claude", Messages: []Message{
		{Role: System, Content: "sys", CacheBreak: true},
		{Role: User, Content: "hi"},
		{Role: Assistant, ToolCalls: []ToolCall{{ID: "t0", Name: "x", Arguments: json.RawMessage(`{}`)}}},
		{Role: Tool, ToolCallID: "t0", Content: "res"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.Content != "ok" || r.Message.ToolCalls[0].Name != "fs.read" || r.Usage.Cached != 7 || r.Usage.In != 10 {
		t.Fatalf("%+v", r)
	}
}

func TestAnthropicStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, ev := range []string{
			`{"type":"message_start","message":{"model":"c","usage":{"input_tokens":5}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"n"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		} {
			fmt.Fprintf(w, "event: x\ndata: %s\n\n", ev)
		}
	}))
	defer srv.Close()
	r, err := (&Anthropic{BaseURL: srv.URL}).Chat(context.Background(), Request{Model: "c", Messages: []Message{{Role: User, Content: "x"}}}, func(string) {})
	if err != nil || r.Message.Content != "Hi" || string(r.Message.ToolCalls[0].Arguments) != `{"a":1}` || r.Usage.Out != 9 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestCassetteReplay(t *testing.T) {
	p := t.TempDir() + "/c.json"
	inner := &Scripted{Responses: []Response{Text("aufgenommen")}}
	rec := &Cassette{Path: p, Mode: "record", Inner: inner}
	req := Request{Model: "m", Messages: []Message{{Role: User, Content: "x"}}}
	if _, err := rec.Chat(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	rep := &Cassette{Path: p}
	r, err := rep.Chat(context.Background(), req, nil)
	if err != nil || r.Message.Content != "aufgenommen" {
		t.Fatal(err)
	}
	if _, err := rep.Chat(context.Background(), Request{Model: "other"}, nil); err == nil {
		t.Fatal("fehlende aufnahme akzeptiert")
	}
}

func TestHashEmbedderSimilarity(t *testing.T) {
	v, _ := HashEmbedder{Dim: 256}.Embed(context.Background(), "", []string{"Anna wohnt in Berlin", "Wo wohnt Anna?", "Rezept für Pfannkuchen"})
	dot := func(a, b []float32) (s float32) {
		for i := range a {
			s += a[i] * b[i]
		}
		return
	}
	if dot(v[0], v[1]) <= dot(v[0], v[2]) {
		t.Fatal("ähnlichkeit falsch")
	}
}
