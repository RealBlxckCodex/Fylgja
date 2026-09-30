// Package fleet ist der Fleet Manager (Spec 16.8): Nodes bereitstellen, überwachen,
// skalieren, Kosten deckeln, verwaiste Pods abbauen.
package fleet

import (
	"context"
	"errors"
	"time"
)

// Tag markiert alle vom Fleet Manager gestarteten Pods (Leak-Schutz).
const Tag = "fylgja-fleet"

// PodSpec beschreibt einen zu startenden Node.
type PodSpec struct {
	Name            string            `json:"name"`
	GPUType         string            `json:"gpu_type"`
	GPUCount        int               `json:"gpu_count"`
	Image           string            `json:"image"`
	NetworkVolumeID string            `json:"network_volume_id"`
	CloudType       string            `json:"cloud_type"` // SECURE|COMMUNITY (Spot nur für background)
	Region          string            `json:"region"`
	Env             map[string]string `json:"env"`
	ContainerDiskGB int               `json:"container_disk_gb"`
}

// ProviderPod ist der Zustand eines Pods beim Provider.
type ProviderPod struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"` // RUNNING|EXITED|...
	GPUType     string    `json:"gpu_type"`
	GPUCount    int       `json:"gpu_count"`
	Region      string    `json:"region"`
	CostPerHour int64     `json:"cost_per_hour_micro_eur"`
	Tagged      bool      `json:"tagged"`
	CreatedAt   time.Time `json:"created_at"`
}

// Offer ist verfügbare Kapazität.
type Offer struct {
	GPUType     string `json:"gpu_type"`
	VRAMGB      int    `json:"vram_gb"`
	CostPerHour int64  `json:"cost_per_hour_micro_eur"`
	Available   bool   `json:"available"`
}

// Provider abstrahiert GPU-Anbieter (runpod, static, …).
type Provider interface {
	Name() string
	Provision(ctx context.Context, spec PodSpec) (ProviderPod, error)
	Status(ctx context.Context, id string) (ProviderPod, error)
	Terminate(ctx context.Context, id string) error
	List(ctx context.Context) ([]ProviderPod, error)
	ListOffers(ctx context.Context) ([]Offer, error)
}

var ErrNotFound = errors.New("fleet: pod nicht gefunden")

// Static ist ein Provider für manuell eingetragene Nodes (lokale Server). Provision/Terminate sind No-Ops.
type Static struct{ Pods []ProviderPod }

func (s *Static) Name() string { return "static" }
func (s *Static) Provision(context.Context, PodSpec) (ProviderPod, error) {
	return ProviderPod{}, errors.New("static: provisionierung nicht möglich")
}
func (s *Static) Status(_ context.Context, id string) (ProviderPod, error) {
	for _, p := range s.Pods {
		if p.ID == id {
			return p, nil
		}
	}
	return ProviderPod{}, ErrNotFound
}
func (s *Static) Terminate(context.Context, string) error          { return nil }
func (s *Static) List(context.Context) ([]ProviderPod, error)      { return nil, nil }
func (s *Static) ListOffers(context.Context) ([]Offer, error)      { return nil, nil }
