package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/realblxckcodex/fylgja/internal/channels"
)

type mock struct {
	mu    sync.Mutex
	calls []string
	sent  []map[string]any
	ups   []string
	failParse bool
}

func (m *mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	method := parts[len(parts)-1]
	if !strings.HasPrefix(parts[1], "botTOKEN") {
		w.WriteHeader(404)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	m.mu.Lock()
	m.calls = append(m.calls, method)
	m.mu.Unlock()
	switch method {
	case "getMe":
		w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"Hugin","username":"hugin_bot"}}`))
	case "getUpdates":
		m.mu.Lock()
		var u string
		if len(m.ups) > 0 {
			u, m.ups = m.ups[0], m.ups[1:]
		}
		m.mu.Unlock()
		if u == "" {
			time.Sleep(20 * time.Millisecond)
			w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":[` + u + `]}`))
	case "sendMessage":
		m.mu.Lock()
		m.sent = append(m.sent, body)
		fail := m.failParse && body["parse_mode"] != nil
		m.mu.Unlock()
		if fail {
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":55,"chat":{"id":1,"type":"private"}}}`))
	default:
		w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

func TestPollingAndConvert(t *testing.T) {
	m := &mock{ups: []string{
		`{"update_id":1,"message":{"message_id":10,"from":{"id":42,"first_name":"Sam","username":"sam"},"chat":{"id":42,"type":"private"},"text":"/queue später 😀 bitte","entities":[{"type":"bot_command","offset":0,"length":6}]}}`,
		`{"update_id":2,"message":{"message_id":11,"from":{"id":7,"first_name":"Eve"},"chat":{"id":-100,"type":"supergroup","is_forum":true},"message_thread_id":5,"text":"👋 @hugin_bot hilf","entities":[{"type":"mention","offset":3,"length":10}]}}`,
		`{"update_id":3,"callback_query":{"id":"cb1","from":{"id":42,"first_name":"Sam"},"message":{"message_id":55,"chat":{"id":42,"type":"private"}},"data":"a:xyz"}}`,
		`{"update_id":4,"message":{"message_id":12,"from":{"id":42,"first_name":"Sam"},"chat":{"id":42,"type":"private"},"voice":{"file_id":"v1","file_size":1000}}}`,
	}}
	srv := httptest.NewServer(m)
	defer srv.Close()
	b := &Bot{Token: "TOKEN", APIBase: srv.URL}
	ctx, cancel := context.WithCancel(context.Background())
	var evs []channels.InboundEvent
	var mu sync.Mutex
	go b.Start(ctx, func(_ context.Context, ev channels.InboundEvent) {
		mu.Lock()
		evs = append(evs, ev)
		mu.Unlock()
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(evs)
		mu.Unlock()
		if n == 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if len(evs) != 4 {
		t.Fatalf("events %d", len(evs))
	}
	if evs[0].Command != "queue" || evs[0].Args != "später 😀 bitte" || !evs[0].IsDM || evs[0].Sender.PlatformUserID != "42" {
		t.Fatalf("cmd %+v", evs[0])
	}
	if !evs[1].MentionsBot || evs[1].IsDM || evs[1].ThreadID != "5" {
		t.Fatalf("mention %+v", evs[1])
	}
	if evs[2].Callback != "a:xyz" || evs[2].CallbackID != "cb1" {
		t.Fatalf("callback %+v", evs[2])
	}
	if len(evs[3].Attachments) != 1 || evs[3].Attachments[0].Kind != "voice" {
		t.Fatalf("voice %+v", evs[3])
	}
	if b.ID() != "999" || !b.Health().OK {
		t.Fatal("health/id")
	}
}

func TestSendSplitAndFallback(t *testing.T) {
	m := &mock{failParse: true}
	srv := httptest.NewServer(m)
	defer srv.Close()
	b := &Bot{Token: "TOKEN", APIBase: srv.URL}
	ref, err := b.Send(context.Background(), channels.Target{ChatID: "1", ThreadID: "3"}, channels.Text("Hallo (Welt)!"))
	if err != nil || ref.MessageID != "55" {
		t.Fatal(err)
	}
	// Erst MarkdownV2 (scheitert), dann Fallback ohne parse_mode mit unmaskiertem Text.
	if len(m.sent) != 2 || m.sent[1]["text"] != "Hallo (Welt)!" || m.sent[0]["message_thread_id"].(float64) != 3 {
		t.Fatalf("%+v", m.sent)
	}
}

func TestWebhookSecret(t *testing.T) {
	b := &Bot{WebhookSecret: "s3cret"}
	got := make(chan channels.InboundEvent, 1)
	b.handler = func(_ context.Context, ev channels.InboundEvent) { got <- ev }
	body := `{"update_id":1,"message":{"message_id":1,"from":{"id":1,"first_name":"a"},"chat":{"id":1,"type":"private"},"text":"hi"}}`
	for _, tc := range []struct {
		secret string
		code   int
	}{{"", 403}, {"falsch", 403}, {"s3cret", 200}} {
		req := httptest.NewRequest("POST", "/hooks/telegram", strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", tc.secret)
		rec := httptest.NewRecorder()
		b.ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Fatalf("secret %q: %d", tc.secret, rec.Code)
		}
	}
	select {
	case ev := <-got:
		if ev.Text != "hi" {
			t.Fatal(ev.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("kein event")
	}
}
