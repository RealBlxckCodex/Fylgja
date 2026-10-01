package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/auth"
	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/router"
	"github.com/realblxckcodex/fylgja/internal/store/testdb"
)

func TestVoiceEndpoints(t *testing.T) {
	var gotTTS map[string]any
	var gotLang string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/audio/transcriptions":
			r.ParseMultipartForm(1 << 20)
			gotLang = r.FormValue("language")
			if r.FormValue("model") != "whisper-x" {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"text":" Hallo Welt "}`))
		case "/v1/audio/speech":
			json.NewDecoder(r.Body).Decode(&gotTTS)
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write([]byte("ID3fake-mp3"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	rt := router.New(router.DefaultConfig(), nil, nil)
	for _, m := range []string{"stt", "tts"} {
		rt.SetModel(router.Model{Name: m, PrivacyClass: "self_hosted"})
	}
	rt.Upsert(&router.Deployment{Name: "stt-1", Model: "stt", Provider: "local", ServedModel: "whisper-x", State: router.Ready, Client: &llm.OpenAI{BaseURL: fake.URL + "/v1"}})
	rt.Upsert(&router.Deployment{Name: "tts-1", Model: "tts", Provider: "local", ServedModel: "kokoro", State: router.Ready, Client: &llm.OpenAI{BaseURL: fake.URL + "/v1"}})

	pool, _ := testdb.New(t)
	ctx := context.Background()
	svc := &auth.Service{Pool: pool}
	ws, dot := uuid.New(), uuid.New()
	pool.Exec(ctx, `INSERT INTO workspaces (id,name) VALUES ($1,'w')`, ws)
	pool.Exec(ctx, `INSERT INTO dots (id,workspace_id,name,kind) VALUES ($1,$2,'Hugin','personal')`, dot, ws)
	uid, _ := svc.CreateUser(ctx, ws, "sam@example.org", "Sam", "sehr-geheimes-passwort", "owner")
	_, p, _ := svc.NewSession(ctx, uid, false)
	s := &Server{Pool: pool, Auth: svc, Voice: Voice{Router: rt}, limiter: newLimiter(1000, 1000)}

	mux := chi.NewRouter()
	mux.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
		})
	})
	mux.Post("/dots/{id}/voice/transcribe", s.voiceTranscribe)
	mux.Post("/dots/{id}/voice/speak", s.voiceSpeak)
	do := func(path string, body []byte, ct string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/dots/"+dot.String()+path, bytes.NewReader(body))
		r.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	audio := bytes.Repeat([]byte{1, 2, 3}, 200)
	w := do("/voice/transcribe", audio, "audio/webm;codecs=opus")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Hallo Welt"`) {
		t.Fatalf("transcribe: %d %s", w.Code, w.Body.String())
	}
	if w := do("/voice/transcribe", audio, "text/plain"); w.Code != 415 {
		t.Fatalf("falscher content-type: %d", w.Code)
	}
	if w := do("/voice/transcribe", []byte("x"), "audio/webm"); w.Code != 400 {
		t.Fatalf("zu kurze aufnahme: %d", w.Code)
	}
	if w := do("/voice/transcribe", bytes.Repeat([]byte{1}, maxVoiceBytes+10), "audio/webm"); w.Code == 200 {
		t.Fatal("zu große aufnahme akzeptiert")
	}
	w = do("/voice/speak", []byte(`{"text":"Hallo Sam"}`), "application/json")
	if w.Code != 200 || w.Header().Get("Content-Type") != "audio/mpeg" || !strings.HasPrefix(w.Body.String(), "ID3") {
		t.Fatalf("speak: %d %q", w.Code, w.Header())
	}
	_ = gotLang
	if gotTTS["model"] != "kokoro" || gotTTS["input"] != "Hallo Sam" || gotTTS["response_format"] != "mp3" || gotTTS["voice"] != "alloy" {
		t.Fatalf("tts-anfrage: %v", gotTTS)
	}
	if w := do("/voice/speak", []byte(`{"text":"`+strings.Repeat("a", maxSpeakChars+1)+`"}`), "application/json"); w.Code != 413 {
		t.Fatalf("zu langer text: %d", w.Code)
	}
	if w := do("/voice/speak", []byte(`{"text":"  "}`), "application/json"); w.Code != 400 {
		t.Fatalf("leerer text: %d", w.Code)
	}
	// Fremde Fylgja (anderer Workspace) bleibt verschlossen.
	other := uuid.New()
	r := httptest.NewRequest("POST", "/dots/"+other.String()+"/voice/speak", strings.NewReader(`{"text":"x"}`))
	ww := httptest.NewRecorder()
	mux.ServeHTTP(ww, r)
	if ww.Code != 404 {
		t.Fatalf("unbekannte fylgja: %d", ww.Code)
	}
	// Voreinstellungen: Stimme, Sprache und Tempo kommen aus den Workspace-Einstellungen.
	put := func(body string) int {
		r := httptest.NewRequest("PUT", "/voice/settings", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), principalKey, p))
		w := httptest.NewRecorder()
		s.putVoiceSettings(w, r)
		return w.Code
	}
	for _, bad := range []string{`{"voice":"a b"}`, `{"voice":"../x"}`, `{"language":"Deutsch"}`, `{"speed":3}`, `{"speed":0.1}`} {
		if c := put(bad); c != 400 {
			t.Fatalf("%s: %d", bad, c)
		}
	}
	if c := put(`{"voice":"martin","language":"de","speed":1.25}`); c != 200 {
		t.Fatalf("settings: %d", c)
	}
	gotTTS, gotLang = nil, ""
	if w := do("/voice/speak", []byte(`{"text":"Hallo"}`), "application/json"); w.Code != 200 || gotTTS["voice"] != "martin" || gotTTS["speed"] != 1.25 {
		t.Fatalf("einstellungen nicht genutzt: %d %v", w.Code, gotTTS)
	}
	if w := do("/voice/transcribe", audio, "audio/webm"); w.Code != 200 || gotLang != "de" {
		t.Fatalf("sprache nicht übergeben: %d %q", w.Code, gotLang)
	}
	// Ohne Modelle: 501 statt Absturz.
	s.Voice = Voice{Router: router.New(router.DefaultConfig(), nil, nil)}
	if w := do("/voice/speak", []byte(`{"text":"x"}`), "application/json"); w.Code != 501 {
		t.Fatalf("ohne tts: %d", w.Code)
	}
	if w := do("/voice/transcribe", audio, "audio/webm"); w.Code != 501 {
		t.Fatalf("ohne stt: %d", w.Code)
	}
}
