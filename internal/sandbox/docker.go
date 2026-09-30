package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Docker ist der Provider "docker-gvisor" (Docker Engine API, Runtime runsc).
type Docker struct {
	Host        string // unix:///var/run/docker.sock oder tcp://host:2376
	Runtime     string // runsc (gVisor); leer = Docker-Default (nur Entwicklung)
	Network     string // internes Netz ohne Default-Route; Ausgang nur über egressd
	EgressProxy string // http://egressd:3128
	HTTP        *http.Client
}

func (d *Docker) Name() string { return "docker-gvisor" }

func (d *Docker) client() (*http.Client, string) {
	if d.HTTP != nil {
		return d.HTTP, "http://docker"
	}
	host := d.Host
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	if strings.HasPrefix(host, "unix://") {
		path := strings.TrimPrefix(host, "unix://")
		return &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}}, "http://docker"
	}
	return &http.Client{Timeout: 2 * time.Minute}, "http://" + strings.TrimPrefix(host, "tcp://")
}

func (d *Docker) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	hc, base := d.client()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/v1.43"+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("docker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 304 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return resp.StatusCode, fmt.Errorf("docker %s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out != nil {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

func name(s Spec) string { return "fylgja-dot-" + s.DotID.String()[:13] }

// ContainerConfig baut die gehärtete Container-Konfiguration (13.3). Exportiert für Tests.
func (d *Docker) ContainerConfig(s Spec) map[string]any {
	env := []string{"COMPUTERD_TOKEN=" + s.Token, "COMPUTERD_LISTEN=:7070", "HOME=/home/dot"}
	if s.Egress != "offline" && d.EgressProxy != "" {
		env = append(env, "HTTP_PROXY="+d.EgressProxy, "HTTPS_PROXY="+d.EgressProxy, "http_proxy="+d.EgressProxy, "https_proxy="+d.EgressProxy, "NO_PROXY=localhost,127.0.0.1")
	}
	host := map[string]any{
		"Mounts":         []map[string]any{{"Type": "volume", "Source": "fylgja-home-" + s.DotID.String(), "Target": "/home/dot"}},
		"ReadonlyRootfs": true,
		"Tmpfs":          map[string]string{"/tmp": "rw,noexec,nosuid,size=1g", "/run": "rw,noexec,nosuid,size=64m"},
		"CapDrop":        []string{"ALL"},
		"SecurityOpt":    []string{"no-new-privileges:true"},
		"Memory":         int64(s.MemoryMB) << 20,
		"NanoCpus":       int64(s.CPUs * 1e9),
		"PidsLimit":      s.PidsLimit,
		"NetworkMode":    d.Network,
		"RestartPolicy":  map[string]any{"Name": "no"},
		"Privileged":     false,
	}
	if s.Egress == "offline" {
		host["NetworkMode"] = "none"
	}
	if d.Runtime != "" {
		host["Runtime"] = d.Runtime
	}
	return map[string]any{
		"Image":        s.Image,
		"Env":          env,
		"User":         "1000:1000",
		"Labels":       map[string]string{"fylgja.dot": s.DotID.String(), "fylgja.egress": s.Egress},
		"HostConfig":   host,
		"ExposedPorts": map[string]any{"7070/tcp": map[string]any{}, "6080/tcp": map[string]any{}},
	}
}

func (d *Docker) Ensure(ctx context.Context, s Spec) (Instance, error) {
	n := name(s)
	var info struct {
		ID              string
		State           struct{ Running bool }
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	code, err := d.do(ctx, http.MethodGet, "/containers/"+n+"/json", nil, &info)
	if code == 404 {
		var created struct{ Id string }
		if _, err := d.do(ctx, http.MethodPost, "/containers/create?name="+n, d.ContainerConfig(s), &created); err != nil {
			return Instance{}, err
		}
		info.ID = created.Id
	} else if err != nil {
		return Instance{}, err
	}
	if !info.State.Running {
		if _, err := d.do(ctx, http.MethodPost, "/containers/"+info.ID+"/start", nil, nil); err != nil {
			return Instance{}, err
		}
		if _, err := d.do(ctx, http.MethodGet, "/containers/"+info.ID+"/json", nil, &info); err != nil {
			return Instance{}, err
		}
	}
	ip := ""
	for _, nw := range info.NetworkSettings.Networks {
		if nw.IPAddress != "" {
			ip = nw.IPAddress
		}
	}
	if ip == "" {
		return Instance{}, fmt.Errorf("docker: sandbox %s ohne ip", n)
	}
	return Instance{Ref: info.ID, Endpoint: "http://" + ip + ":7070", VNC: "http://" + ip + ":6080", Volume: "fylgja-home-" + s.DotID.String()}, nil
}

func (d *Docker) Sleep(ctx context.Context, ref string) error {
	_, err := d.do(ctx, http.MethodPost, "/containers/"+ref+"/stop?t=10", nil, nil)
	return err
}

func (d *Docker) Destroy(ctx context.Context, ref string, keepVolume bool) error {
	q := "?force=true"
	if !keepVolume {
		q += "&v=true"
	}
	_, err := d.do(ctx, http.MethodDelete, "/containers/"+ref+q, nil, nil)
	return err
}

func (d *Docker) Stats(ctx context.Context, ref string) (map[string]any, error) {
	var out map[string]any
	_, err := d.do(ctx, http.MethodGet, "/containers/"+ref+"/stats?stream=false", nil, &out)
	return out, err
}
