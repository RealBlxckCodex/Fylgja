// Package config lädt und validiert fylgja.yaml + Umgebungsvariablen (Spec 21.1).
//
// Ebenen: Datei (Basis) → Umgebungsvariablen (Secrets, Overrides) → Datenbank
// (alles zur Laufzeit Änderbare, nicht Teil dieses Packages).
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen      string `yaml:"listen"`
	BaseURL     string `yaml:"base_url"`
	DatabaseURL string `yaml:"database_url"`
	LogLevel    string `yaml:"log_level"`
	Role        string `yaml:"role"` // all|api|worker|channels|scheduler|router

	// MasterKey kommt ausschließlich aus FYLGJA_MASTER_KEY oder MasterKeyFile.
	MasterKey     string `yaml:"-"`
	MasterKeyFile string `yaml:"master_key_file"`

	Auth     Auth           `yaml:"auth"`
	Channels Channels       `yaml:"channels"`
	Router   Router         `yaml:"router"`
	Fleet    Fleet          `yaml:"fleet"`
	Sandbox  Sandbox        `yaml:"sandbox"`
	Runtime  Runtime        `yaml:"runtime"`
	Features map[string]bool `yaml:"features"`
}

type Auth struct {
	SessionTTL      time.Duration `yaml:"session_ttl"`
	WebAuthnRPID    string        `yaml:"webauthn_rp_id"`
	WebAuthnOrigins []string      `yaml:"webauthn_origins"`
	// BootstrapAdmin legt beim ersten Start einen Owner an (nur wenn DB leer).
	BootstrapEmail string `yaml:"bootstrap_email"`
}

type Channels struct {
	Telegram TelegramCfg `yaml:"telegram"`
	Discord  DiscordCfg  `yaml:"discord"`
}

type TelegramCfg struct {
	Enabled       bool   `yaml:"enabled"`
	Token         string `yaml:"-"` // FYLGJA_TELEGRAM_TOKEN (nur Erststart, danach Vault)
	Mode          string `yaml:"mode"` // polling|webhook
	WebhookSecret string `yaml:"-"`
	APIBase       string `yaml:"api_base"`
}

type DiscordCfg struct {
	Enabled        bool   `yaml:"enabled"`
	Token          string `yaml:"-"` // FYLGJA_DISCORD_TOKEN
	MessageContent bool   `yaml:"message_content_intent"`
}

// Router: logische Modelle und Deployments für den Erststart (danach DB).
type Router struct {
	Enabled       bool              `yaml:"enabled"`
	ExternalToken string            `yaml:"-"` // FYLGJA_ROUTER_TOKEN für /router/v1
	Models        []ModelSeed       `yaml:"models"`
	Deployments   []DeploymentSeed  `yaml:"deployments"`
	Tiers         map[string]string `yaml:"tiers"` // tier → logisches Modell
}

type ModelSeed struct {
	Name         string   `yaml:"name"`
	PrivacyClass string   `yaml:"privacy_class"`
	QualityRank  int      `yaml:"quality_rank"`
	Capabilities []string `yaml:"capabilities"`
	MaxContext   int      `yaml:"max_context"`
	Fallbacks    []string `yaml:"fallbacks"`
}

type DeploymentSeed struct {
	Name           string  `yaml:"name"`
	Model          string  `yaml:"model"`
	Provider       string  `yaml:"provider"` // runpod|local|external
	Engine         string  `yaml:"engine"`   // vllm|ollama|llamacpp|remote_api
	Endpoint       string  `yaml:"endpoint"`
	ServedModel    string  `yaml:"served_model"`
	APIKeyEnv      string  `yaml:"api_key_env"`
	Kind           string  `yaml:"kind"` // openai|anthropic
	MaxConcurrency int     `yaml:"max_concurrency"`
	Weight         float64 `yaml:"weight"`
	Region         string  `yaml:"region"`
	PriceInPerMTok  int64  `yaml:"price_in_micro_eur_per_mtok"`
	PriceOutPerMTok int64  `yaml:"price_out_micro_eur_per_mtok"`
	CostPerHour     int64  `yaml:"cost_per_hour_micro_eur"`
}

type Fleet struct {
	Enabled       bool          `yaml:"enabled"`
	RunPodAPIKey  string        `yaml:"-"` // FYLGJA_RUNPOD_API_KEY (Erststart)
	RunPodAPIBase string        `yaml:"runpod_api_base"`
	EvalInterval  time.Duration `yaml:"eval_interval"`
	PodTemplate   PodTemplate   `yaml:"pod_template"`
}

type PodTemplate struct {
	GPUType         string `yaml:"gpu_type"`
	GPUCount        int    `yaml:"gpu_count"`
	Image           string `yaml:"image"`
	NetworkVolumeID string `yaml:"network_volume_id"`
	CloudType       string `yaml:"cloud_type"` // SECURE|COMMUNITY
	Region          string `yaml:"region"`
}

type Sandbox struct {
	Provider string       `yaml:"provider"` // docker-gvisor|none
	Hosts    []SandboxHost `yaml:"hosts"`
	Image    string       `yaml:"image"`
	IdleSleep time.Duration `yaml:"idle_sleep"`
}

type SandboxHost struct {
	Name     string `yaml:"name"`
	Endpoint string `yaml:"endpoint"`
}

type Runtime struct {
	MaxParallelRunsPerDot int           `yaml:"max_parallel_runs_per_dot"`
	MaxSteps              int           `yaml:"max_steps"`
	ModelTimeout          time.Duration `yaml:"model_timeout"`
	ToolTimeout           time.Duration `yaml:"tool_timeout"`
}

// Default liefert eine Konfiguration mit sinnvollen Standardwerten.
func Default() Config {
	return Config{
		Listen:   ":8080",
		BaseURL:  "http://localhost:8080",
		LogLevel: "info",
		Role:     "all",
		Auth:     Auth{SessionTTL: 14 * 24 * time.Hour, WebAuthnRPID: "localhost", WebAuthnOrigins: []string{"http://localhost:8080"}},
		Channels: Channels{Telegram: TelegramCfg{Mode: "polling", APIBase: "https://api.telegram.org"}},
		Router:   Router{Enabled: true, Tiers: map[string]string{}},
		Fleet:    Fleet{RunPodAPIBase: "https://rest.runpod.io/v1", EvalInterval: 15 * time.Second},
		Sandbox:  Sandbox{Provider: "none", Image: "ghcr.io/realblxckcodex/fylgja-computer:base", IdleSleep: 15 * time.Minute},
		Runtime:  Runtime{MaxParallelRunsPerDot: 4, MaxSteps: 40, ModelTimeout: 120 * time.Second, ToolTimeout: 120 * time.Second},
		Features: map[string]bool{},
	}
}

// Load liest die Datei (falls vorhanden), wendet Env-Overrides an und validiert.
func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("config: %w", err)
		}
		dec := yaml.NewDecoder(strings.NewReader(os.ExpandEnv(string(b))))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil {
			return c, fmt.Errorf("config %s: %w", path, err)
		}
	}
	applyEnv(&c)
	if c.MasterKey == "" && c.MasterKeyFile != "" {
		b, err := os.ReadFile(c.MasterKeyFile)
		if err != nil {
			return c, fmt.Errorf("config: master_key_file: %w", err)
		}
		c.MasterKey = strings.TrimSpace(string(b))
	}
	return c, c.Validate()
}

func applyEnv(c *Config) {
	set := func(dst *string, key string) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			*dst = v
		}
	}
	set(&c.DatabaseURL, "FYLGJA_DATABASE_URL")
	set(&c.Listen, "FYLGJA_LISTEN")
	set(&c.BaseURL, "FYLGJA_BASE_URL")
	set(&c.LogLevel, "FYLGJA_LOG_LEVEL")
	set(&c.MasterKey, "FYLGJA_MASTER_KEY")
	set(&c.Channels.Telegram.Token, "FYLGJA_TELEGRAM_TOKEN")
	set(&c.Channels.Telegram.WebhookSecret, "FYLGJA_TELEGRAM_WEBHOOK_SECRET")
	set(&c.Channels.Discord.Token, "FYLGJA_DISCORD_TOKEN")
	set(&c.Fleet.RunPodAPIKey, "FYLGJA_RUNPOD_API_KEY")
	set(&c.Router.ExternalToken, "FYLGJA_ROUTER_TOKEN")
	set(&c.Auth.BootstrapEmail, "FYLGJA_BOOTSTRAP_EMAIL")
}

var validRoles = map[string]bool{"all": true, "api": true, "worker": true, "channels": true, "scheduler": true, "router": true}

// Validate prüft die Konfiguration; Fehler sind klar und fatal (Spec 21.1).
func (c Config) Validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("database_url fehlt (oder FYLGJA_DATABASE_URL)"))
	}
	if !validRoles[c.Role] {
		errs = append(errs, fmt.Errorf("role %q ungültig", c.Role))
	}
	if c.MasterKey == "" {
		errs = append(errs, errors.New("FYLGJA_MASTER_KEY bzw. master_key_file fehlt"))
	}
	if c.Channels.Telegram.Enabled && c.Channels.Telegram.Token == "" {
		errs = append(errs, errors.New("telegram aktiviert, aber FYLGJA_TELEGRAM_TOKEN fehlt"))
	}
	if c.Channels.Telegram.Mode != "polling" && c.Channels.Telegram.Mode != "webhook" {
		errs = append(errs, fmt.Errorf("telegram.mode %q ungültig", c.Channels.Telegram.Mode))
	}
	if c.Channels.Discord.Enabled && c.Channels.Discord.Token == "" {
		errs = append(errs, errors.New("discord aktiviert, aber FYLGJA_DISCORD_TOKEN fehlt"))
	}
	models := map[string]bool{}
	for _, m := range c.Router.Models {
		if m.Name == "" {
			errs = append(errs, errors.New("router.models: name fehlt"))
		}
		switch m.PrivacyClass {
		case "self_hosted", "eu", "any":
		default:
			errs = append(errs, fmt.Errorf("router.models[%s]: privacy_class %q ungültig", m.Name, m.PrivacyClass))
		}
		models[m.Name] = true
	}
	for _, d := range c.Router.Deployments {
		if !models[d.Model] {
			errs = append(errs, fmt.Errorf("router.deployments[%s]: unbekanntes Modell %q", d.Name, d.Model))
		}
		switch d.Provider {
		case "runpod", "local", "external":
		default:
			errs = append(errs, fmt.Errorf("router.deployments[%s]: provider %q ungültig", d.Name, d.Provider))
		}
		if d.Endpoint == "" {
			errs = append(errs, fmt.Errorf("router.deployments[%s]: endpoint fehlt", d.Name))
		}
	}
	for tier, m := range c.Router.Tiers {
		if !models[m] {
			errs = append(errs, fmt.Errorf("router.tiers[%s]: unbekanntes Modell %q", tier, m))
		}
	}
	if c.Runtime.MaxSteps <= 0 {
		errs = append(errs, errors.New("runtime.max_steps muss > 0 sein"))
	}
	return errors.Join(errs...)
}
