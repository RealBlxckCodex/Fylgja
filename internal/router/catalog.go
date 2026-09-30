// Package router ist der eigene Inference-Router (Spec Kapitel 16).
//
// Agents sprechen logische Modelle; der Router löst sie in Deployments auf,
// verteilt Last (Score mit Prefix-Affinität), erzwingt Privacy-Klassen,
// priorisiert (interactive > review > task > background), öffnet Circuit-Breaker
// und fällt entlang der Fallback-Kette zurück – ohne je Privacy zu verletzen.
package router

import (
	"strings"

	"github.com/realblxckcodex/fylgja/internal/llm"
)

// Model ist ein Eintrag im Model Catalog.
type Model struct {
	Name         string   `json:"name"`
	PrivacyClass string   `json:"privacy_class"` // self_hosted|eu|any
	QualityRank  int      `json:"quality_rank"`
	Capabilities []string `json:"capabilities"` // tools, vision, json_mode
	MaxContext   int      `json:"max_context"`
	// Fallbacks: logische Modelle (absteigende Qualität), falls keine Deployments verfügbar sind.
	Fallbacks []string `json:"fallbacks"`
}

func (m Model) Has(cap string) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// DeploymentState (16, 7.3).
type DeploymentState string

const (
	Loading  DeploymentState = "loading"
	Ready    DeploymentState = "ready"
	Draining DeploymentState = "draining"
	Failed   DeploymentState = "failed"
	Stopped  DeploymentState = "stopped"
)

// Deployment ist eine konkrete Instanz eines logischen Modells.
type Deployment struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Model          string          `json:"model"`
	Provider       string          `json:"provider"` // runpod|local|external
	Engine         string          `json:"engine"`
	Endpoint       string          `json:"endpoint"`
	ServedModel    string          `json:"served_model"`
	Region         string          `json:"region"`
	NodeID         string          `json:"node_id,omitempty"`
	MaxConcurrency int             `json:"max_concurrency"`
	Weight         float64         `json:"weight"`
	State          DeploymentState `json:"state"`
	// Preise (µ€): extern pro MTok, GPU pro Stunde (Schattenpreis, 16.10).
	PriceInPerMTok  int64 `json:"price_in_micro_eur_per_mtok"`
	PriceOutPerMTok int64 `json:"price_out_micro_eur_per_mtok"`
	CostPerHour     int64 `json:"cost_per_hour_micro_eur"`

	Client llm.Client `json:"-"`
}

var euRegions = []string{"eu", "europe", "de", "fr", "nl", "se", "fi", "no", "pl", "cz", "is", "ro", "it", "es", "at", "be", "ie", "dk", "ch"}

// IsEU prüft, ob eine Region in Europa liegt (z. B. "EU-RO-1", "eu-central-1", "de-fra").
func IsEU(region string) bool {
	r := strings.ToLower(region)
	for _, p := range euRegions {
		if r == p || strings.HasPrefix(r, p+"-") || strings.HasPrefix(r, p+"_") {
			return true
		}
	}
	return false
}

// SatisfiesPrivacy prüft die Privacy-Anforderung (16.7).
// RunPod-Pods und lokale Nodes gelten als self_hosted; eu_only prüft die Region
// (lokale Nodes ohne Region gelten als eigene Infrastruktur und damit zulässig).
func (d *Deployment) SatisfiesPrivacy(p llm.Privacy) bool {
	selfHosted := d.Provider == "runpod" || d.Provider == "local"
	switch p {
	case llm.SelfHostedOnly, "":
		return selfHosted
	case llm.EUOnly:
		if d.Provider == "local" && d.Region == "" {
			return true
		}
		return IsEU(d.Region)
	default:
		return true
	}
}

// Strictest liefert die strengste von mehreren Privacy-Anforderungen.
func Strictest(ps ...llm.Privacy) llm.Privacy {
	rank := func(p llm.Privacy) int {
		switch p {
		case llm.SelfHostedOnly:
			return 2
		case llm.EUOnly:
			return 1
		case llm.AnyPrivacy:
			return 0
		}
		return 2 // unbekannt/leer = am strengsten
	}
	best := llm.AnyPrivacy
	for _, p := range ps {
		if rank(p) > rank(best) {
			best = p
			if p == "" {
				best = llm.SelfHostedOnly
			}
		}
	}
	return best
}
