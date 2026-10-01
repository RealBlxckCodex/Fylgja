// Package aurora spricht mit Aurora (github.com/RealBlxckCodex/Aurora), der selbst gehosteten Audio-Engine
// für Sprache-zu-Text und Text-zu-Sprache mit OpenAI-kompatibler API. Fylgja nutzt Aurora über die normalen
// Router-Deployments "stt" und "tts"; dieses Paket ergänzt Status, Modell- und Sprachliste für die Oberfläche.
package aurora

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client ist ein schlanker Client für Aurora-Verwaltungsendpunkte.
type Client struct {
	BaseURL string // z. B. http://aurora:11435 (ohne /v1)
	APIKey  string
	HTTP    *http.Client
}

// Model ist ein von Aurora geladenes Modell.
type Model struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // tts|stt
	Backend string `json:"backend"`
	Loaded  bool   `json:"loaded"`
}

// Language ist eine unterstützte Sprache mit passenden Modellen.
type Language struct {
	Code       string   `json:"code"`
	Name       string   `json:"name"`
	NativeName string   `json:"native_name"`
	TTSModels  []string `json:"tts_models"`
	STTModels  []string `json:"stt_models"`
}

// Info fasst den Zustand für die Oberfläche zusammen.
type Info struct {
	Endpoint  string     `json:"endpoint"`
	OK        bool       `json:"ok"`
	Version   string     `json:"version,omitempty"`
	Models    []Model    `json:"models"`
	Languages []Language `json:"languages"`
	Error     string     `json:"error,omitempty"`
	LatencyMS int64      `json:"latency_ms,omitempty"`
}

var ErrNotConfigured = errors.New("aurora: nicht konfiguriert")

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 8 * time.Second}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	if c == nil || c.BaseURL == "" {
		return ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("aurora: API-Schlüssel abgelehnt")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("aurora: %s: status %d", path, resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

// Info fragt Status, Modelle und Sprachen ab. Teilausfälle (z. B. fehlendes /v1/languages bei älteren
// Versionen) werden toleriert; nur ein nicht erreichbarer Status macht OK=false.
func (c *Client) Info(ctx context.Context) Info {
	info := Info{Models: []Model{}, Languages: []Language{}}
	if c != nil {
		info.Endpoint = c.BaseURL
	}
	start := time.Now()
	var st struct {
		Version string  `json:"version"`
		Models  []Model `json:"models"`
	}
	if err := c.get(ctx, "/v1/status", &st); err != nil {
		info.Error = err.Error()
		return info
	}
	info.OK, info.Version, info.LatencyMS = true, st.Version, time.Since(start).Milliseconds()
	var ms struct {
		Models []Model `json:"models"`
	}
	if err := c.get(ctx, "/v1/models", &ms); err == nil && len(ms.Models) > 0 {
		info.Models = ms.Models
	} else {
		info.Models = st.Models
	}
	var ls struct {
		Languages []Language `json:"languages"`
	}
	if err := c.get(ctx, "/v1/languages", &ls); err == nil {
		info.Languages = ls.Languages
	}
	if info.Models == nil {
		info.Models = []Model{}
	}
	return info
}

// Voices-Liste: Aurora liefert Stimmen nur über die Modelldokumentation (Registry); wir kennen die der mitgelieferten Modelle.
var KnownVoices = map[string][]string{
	"kokoro-v1":   {"af_heart", "am_adam", "bf_emma", "bm_daniel", "ef_dora", "em_alex", "ff_siwis", "hf_alpha", "hm_omega", "if_sara", "im_nicola", "jf_alpha", "jm_kumo", "pf_dora", "pm_alex", "zf_xiaobei", "zm_yunxi"},
	"kokoro-de":   {"martin"},
	"piper-de_DE": {"thorsten"},
	"orpheus-en":  {"tara", "leah", "jesper"},
	"orpheus-de":  {"default"},
}
