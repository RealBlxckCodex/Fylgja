package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RunPod spricht die RunPod-REST-API (https://rest.runpod.io/v1).
// Der API-Key liegt im Vault und wird vom Fleet Manager injiziert.
type RunPod struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func (r *RunPod) Name() string { return "runpod" }

func (r *RunPod) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	base := r.BaseURL
	if base == "" {
		base = "https://rest.runpod.io/v1"
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")
	hc := r.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("runpod: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("runpod: %s %s: http %d: %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type rpPod struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	DesiredStatus string            `json:"desiredStatus"`
	CostPerHr     json.Number       `json:"costPerHr"`
	GPUCount      int               `json:"gpuCount"`
	Env           map[string]string `json:"env"`
	Machine       struct {
		GPUTypeID    string `json:"gpuTypeId"`
		DataCenterID string `json:"dataCenterId"`
	} `json:"machine"`
	CreatedAt string `json:"createdAt"`
}

// usdToMicroEUR: RunPod rechnet in USD; für Budgets wird ein fester Kurs angesetzt
// (konfigurierbar wäre ein ADR-Thema; 1 USD ≈ 0,92 EUR).
func usdToMicroEUR(n json.Number) int64 {
	f, _ := n.Float64()
	return int64(f * 0.92 * 1_000_000)
}

func (p rpPod) toPod() ProviderPod {
	t, _ := time.Parse(time.RFC3339, p.CreatedAt)
	return ProviderPod{ID: p.ID, Name: p.Name, Status: p.DesiredStatus, GPUType: p.Machine.GPUTypeID, GPUCount: p.GPUCount,
		Region: p.Machine.DataCenterID, CostPerHour: usdToMicroEUR(p.CostPerHr),
		Tagged: p.Env["FYLGJA_FLEET_TAG"] == Tag || strings.HasPrefix(p.Name, Tag+"-"), CreatedAt: t}
}

func (r *RunPod) Provision(ctx context.Context, spec PodSpec) (ProviderPod, error) {
	env := map[string]string{"FYLGJA_FLEET_TAG": Tag}
	for k, v := range spec.Env {
		env[k] = v
	}
	body := map[string]any{
		"name":              Tag + "-" + spec.Name,
		"imageName":         spec.Image,
		"gpuTypeIds":        []string{spec.GPUType},
		"gpuCount":          max(spec.GPUCount, 1),
		"cloudType":         firstNonEmpty(spec.CloudType, "SECURE"),
		"env":               env,
		"containerDiskInGb": max(spec.ContainerDiskGB, 30),
		// Kein öffentlicher Inference-Port (D7): der Node baut einen ausgehenden Tunnel.
		"ports": []string{},
	}
	if spec.NetworkVolumeID != "" {
		body["networkVolumeId"] = spec.NetworkVolumeID
	}
	if spec.Region != "" {
		body["dataCenterIds"] = []string{spec.Region}
	}
	var out rpPod
	if err := r.do(ctx, http.MethodPost, "/pods", body, &out); err != nil {
		return ProviderPod{}, err
	}
	return out.toPod(), nil
}

func (r *RunPod) Status(ctx context.Context, id string) (ProviderPod, error) {
	var out rpPod
	if err := r.do(ctx, http.MethodGet, "/pods/"+id, nil, &out); err != nil {
		return ProviderPod{}, err
	}
	return out.toPod(), nil
}

func (r *RunPod) Terminate(ctx context.Context, id string) error {
	err := r.do(ctx, http.MethodDelete, "/pods/"+id, nil, nil)
	if err == ErrNotFound {
		return nil
	}
	return err
}

func (r *RunPod) List(ctx context.Context) ([]ProviderPod, error) {
	var out []rpPod
	if err := r.do(ctx, http.MethodGet, "/pods", nil, &out); err != nil {
		return nil, err
	}
	pods := make([]ProviderPod, 0, len(out))
	for _, p := range out {
		pods = append(pods, p.toPod())
	}
	return pods, nil
}

func (r *RunPod) ListOffers(ctx context.Context) ([]Offer, error) {
	// Die REST-API liefert GPU-Typen nicht einheitlich; Angebote werden per Konfiguration gepflegt.
	return nil, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
