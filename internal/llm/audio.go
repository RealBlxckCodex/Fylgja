package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// Transcriber: Sprache → Text (/v1/audio/transcriptions).
type Transcriber interface {
	Transcribe(ctx context.Context, model string, audio []byte, mime string) (string, error)
}

// Speaker: Text → Sprache (/v1/audio/speech).
type Speaker interface {
	Speak(ctx context.Context, model, voice, text, format string) ([]byte, error)
}

func (o *OpenAI) Transcribe(ctx context.Context, model string, audio []byte, mime string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	ext := "ogg"
	switch {
	case strings.Contains(mime, "mpeg"):
		ext = "mp3"
	case strings.Contains(mime, "wav"):
		ext = "wav"
	case strings.Contains(mime, "webm"):
		ext = "webm"
	}
	fw, _ := w.CreateFormFile("file", "audio."+ext)
	fw.Write(audio)
	_ = w.WriteField("model", model)
	w.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", &APIError{Status: resp.StatusCode, Body: string(b)}
	}
	var r struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("stt: %w", err)
	}
	return strings.TrimSpace(r.Text), nil
}

func (o *OpenAI) Speak(ctx context.Context, model, voice, text, format string) ([]byte, error) {
	if format == "" {
		format = "opus"
	}
	resp, err := o.post(ctx, "/audio/speech", map[string]any{"model": model, "voice": voice, "input": text, "response_format": format})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 25<<20))
}
