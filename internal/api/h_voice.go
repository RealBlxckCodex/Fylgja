package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/realblxckcodex/fylgja/internal/router"
)

// Voice bündelt Sprache-zu-Text und Text-zu-Sprache. Beide sind optional.
type Voice struct {
	Router *router.Router
	// Models: logische Namen im Katalog (Standard "stt" und "tts").
	STT, TTS string
	// VoiceName: Stimme für TTS (Standard "alloy", je nach Server anders).
	VoiceName string
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

const (
	maxVoiceBytes = 10 << 20
	maxSpeakChars = 1500
)

func (s *Server) voiceStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"stt": s.Voice.hasSTT(), "tts": s.Voice.hasTTS()})
}

// voiceTranscribe wandelt eine Aufnahme (Body, z. B. audio/webm) in Text um. Der Text wird erst vom Client als
// normale Chat-Nachricht gesendet; die Transkription hat selbst keine Wirkung.
func (s *Server) voiceTranscribe(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	if !s.Voice.hasSTT() {
		problem(w, 501, "keine spracherkennung konfiguriert (logisches modell \"stt\")")
		return
	}
	if !s.limiter.allow("voice:" + principal(r).UserID.String()) {
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
	text, err := s.Voice.Router.Transcribe(r.Context(), s.Voice.stt(), audio, mime)
	if err != nil {
		problem(w, 502, "spracherkennung fehlgeschlagen")
		return
	}
	writeJSON(w, 200, map[string]any{"text": text})
}

// voiceSpeak liefert Sprache (MP3) für einen Text.
func (s *Server) voiceSpeak(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
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
	if !s.limiter.allow("voice:" + principal(r).UserID.String()) {
		problem(w, 429, "zu viele sprachanfragen")
		return
	}
	voice := s.Voice.VoiceName
	if voice == "" {
		voice = "alloy"
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	b, err := s.Voice.Router.Speak(ctx, s.Voice.tts(), voice, in.Text, "mp3")
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
