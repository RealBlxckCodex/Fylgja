package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/aurora"
	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/platform/config"
	"github.com/realblxckcodex/fylgja/internal/router"
)

// Voice bündelt Sprache-zu-Text und Text-zu-Sprache. Beide sind optional.
type Voice struct {
	Router *router.Router
	// Models: logische Namen im Katalog (Standard "stt" und "tts").
	STT, TTS string
	// Aurora: optionale selbst gehostete Engine (Status und Modelle für die Oberfläche).
	Aurora *aurora.Client
	// Defaults aus der Konfiguration (Stimme, Sprache).
	Defaults config.Aurora
}

func (v Voice) stt() string {
	if v.STT != "" {
		return v.STT
	}
	return "stt"
}
func (v Voice) tts() string {
	if v.TTS != "" {
		return v.TTS
	}
	return "tts"
}

func (v Voice) hasSTT() bool { return v.Router != nil && v.Router.HasModel(v.stt()) }
func (v Voice) hasTTS() bool { return v.Router != nil && v.Router.HasModel(v.tts()) }

// VoiceSettings sind die Voreinstellungen eines Workspaces (workspaces.settings.voice).
type VoiceSettings struct {
	Voice    string  `json:"voice"`
	Language string  `json:"language"`
	Speed    float64 `json:"speed"`
}

var (
	voiceRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	langRe  = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2})?$`)
)

func (s *Server) voiceSettings(ctx context.Context, ws uuid.UUID) VoiceSettings {
	out := VoiceSettings{Voice: s.Voice.Defaults.Voice, Language: s.Voice.Defaults.Language}
	if out.Voice == "" {
		out.Voice = "alloy"
	}
	var raw []byte
	if s.Pool.QueryRow(ctx, `SELECT coalesce(settings->'voice','{}'::jsonb) FROM workspaces WHERE id=$1`, ws).Scan(&raw) == nil {
		var st VoiceSettings
		if json.Unmarshal(raw, &st) == nil {
			if voiceRe.MatchString(st.Voice) {
				out.Voice = st.Voice
			}
			if st.Language == "" || langRe.MatchString(st.Language) {
				out.Language = st.Language
			}
			if st.Speed >= 0.5 && st.Speed <= 2.0 {
				out.Speed = st.Speed
			}
		}
	}
	return out
}

const (
	maxVoiceBytes = 10 << 20
	maxSpeakChars = 1500
)

func (s *Server) voiceStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"stt": s.Voice.hasSTT(), "tts": s.Voice.hasTTS(), "aurora": s.Voice.Aurora != nil})
}

// voiceConfig: Zustand von Aurora, Voreinstellungen und Stimmen (für die Einstellungsseite).
func (s *Server) voiceConfig(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"stt": s.Voice.hasSTT(), "tts": s.Voice.hasTTS(), "settings": s.voiceSettings(r.Context(), principal(r).WorkspaceID),
		"models": map[string]string{"stt": s.Voice.Defaults.STTModel, "tts": s.Voice.Defaults.TTSModel}}
	if s.Voice.Aurora != nil {
		info := s.Voice.Aurora.Info(r.Context())
		out["aurora"] = info
		voices := map[string][]string{}
		for _, m := range info.Models {
			if v, ok := aurora.KnownVoices[m.ID]; ok {
				voices[m.ID] = v
			}
		}
		out["voices"] = voices
	}
	writeJSON(w, 200, out)
}

func (s *Server) putVoiceSettings(w http.ResponseWriter, r *http.Request) {
	var in VoiceSettings
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if in.Voice != "" && !voiceRe.MatchString(in.Voice) {
		problem(w, 400, "ungültige stimme")
		return
	}
	if in.Language != "" && !langRe.MatchString(in.Language) {
		problem(w, 400, "ungültiger sprachcode (z. B. de oder en)")
		return
	}
	if in.Speed != 0 && (in.Speed < 0.5 || in.Speed > 2.0) {
		problem(w, 400, "tempo muss zwischen 0,5 und 2,0 liegen")
		return
	}
	b, _ := json.Marshal(in)
	if _, err := s.Pool.Exec(r.Context(), `UPDATE workspaces SET settings = jsonb_set(coalesce(settings,'{}'::jsonb), '{voice}', $2::jsonb) WHERE id=$1`, principal(r).WorkspaceID, string(b)); err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.audit(r, "voice.settings", "", map[string]any{"voice": in.Voice, "language": in.Language, "speed": in.Speed})
	writeJSON(w, 200, s.voiceSettings(r.Context(), principal(r).WorkspaceID))
}

func (s *Server) transcribe(w http.ResponseWriter, r *http.Request) {
	if !s.Voice.hasSTT() {
		problem(w, 501, "keine spracherkennung konfiguriert (logisches modell \"stt\")")
		return
	}
	p := principal(r)
	if !s.limiter.allow("voice:" + p.UserID.String()) {
		problem(w, 429, "zu viele sprachanfragen")
		return
	}
	audio, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxVoiceBytes))
	if err != nil || len(audio) < 200 {
		problem(w, 400, "aufnahme fehlt oder ist zu groß (max. 10 MB)")
		return
	}
	mime := r.Header.Get("Content-Type")
	if !strings.HasPrefix(mime, "audio/") {
		problem(w, 415, "content-type muss audio/* sein")
		return
	}
	set := s.voiceSettings(r.Context(), p.WorkspaceID)
	ctx := llm.WithAudioOpts(r.Context(), llm.AudioOpts{Language: set.Language})
	text, err := s.Voice.Router.Transcribe(ctx, s.Voice.stt(), audio, mime)
	if err != nil {
		problem(w, 502, "spracherkennung fehlgeschlagen")
		return
	}
	writeJSON(w, 200, map[string]any{"text": text})
}

func (s *Server) speak(w http.ResponseWriter, r *http.Request) {
	if !s.Voice.hasTTS() {
		problem(w, 501, "keine sprachausgabe konfiguriert (logisches modell \"tts\")")
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
		problem(w, 400, "text fehlt")
		return
	}
	if utf8.RuneCountInString(in.Text) > maxSpeakChars {
		problem(w, 413, "text zu lang für die sprachausgabe")
		return
	}
	p := principal(r)
	if !s.limiter.allow("voice:" + p.UserID.String()) {
		problem(w, 429, "zu viele sprachanfragen")
		return
	}
	set := s.voiceSettings(r.Context(), p.WorkspaceID)
	ctx := llm.WithAudioOpts(r.Context(), llm.AudioOpts{Speed: set.Speed})
	b, err := s.Voice.Router.Speak(ctx, s.Voice.tts(), set.Voice, in.Text, "mp3")
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		problem(w, 502, "sprachausgabe fehlgeschlagen")
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// voiceTranscribe und voiceSpeak: Varianten je Fylgja (Berechtigung über die Fylgja).
func (s *Server) voiceTranscribe(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	s.transcribe(w, r)
}

func (s *Server) voiceSpeak(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	s.speak(w, r)
}
