package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// Firecracker ist der Provider "firecracker": jede Fylgja bekommt eine eigene microVM
// (eigener Kernel, KVM-Isolation) statt eines Containers. Das Home-Verzeichnis liegt auf einem
// eigenen ext4-Image, das Schlafen und Aufwecken überlebt. Der Control Plane muss auf einem Host
// mit /dev/kvm laufen und tap-Geräte anlegen dürfen (CAP_NET_ADMIN); siehe deploy/firecracker/.
type Firecracker struct {
	Binary   string // firecracker
	Kernel   string // vmlinux
	Rootfs   string // schreibgeschütztes ext4-Image mit dem Computer-Userland
	DataDir  string // pro Fylgja ein Unterverzeichnis (Socket, Log, home.ext4)
	HomeMB   int    // Größe des Home-Images (sparse), Standard 4096
	Subnet   string // Basis, z. B. "172.31" → je VM ein /30 aus 172.31.<n>.<m>
	BootArgs string // zusätzliche Kernel-Argumente

	// Für Tests austauschbar.
	Run   func(ctx context.Context, name string, args ...string) ([]byte, error)
	Spawn func(ctx context.Context, bin string, args []string, logPath string) (pid int, err error)

	mu sync.Mutex
}

func (f *Firecracker) Name() string { return "firecracker" }

func (f *Firecracker) bin() string    { return firstNonEmptyStr(f.Binary, "firecracker") }
func (f *Firecracker) homeMB() int    { return map[bool]int{true: f.HomeMB, false: 4096}[f.HomeMB > 0] }
func (f *Firecracker) subnet() string { return firstNonEmptyStr(f.Subnet, "172.31") }

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (f *Firecracker) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if f.Run != nil {
		return f.Run(ctx, name, args...)
	}
	var out, eb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &eb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(eb.String()))
	}
	return out.Bytes(), nil
}

func (f *Firecracker) spawn(ctx context.Context, bin string, args []string, logPath string) (int, error) {
	if f.Spawn != nil {
		return f.Spawn(ctx, bin, args, logPath)
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return 0, err
	}
	defer lf.Close()
	cmd := exec.Command(bin, args...) // bewusst ohne ctx: die VM überlebt einen Neustart des Control Planes
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

// ---- Layout ----

type vmDir struct{ root string }

func (f *Firecracker) dir(dot uuid.UUID) vmDir { return vmDir{filepath.Join(f.DataDir, dot.String())} }
func (d vmDir) sock() string                   { return filepath.Join(d.root, "api.sock") }
func (d vmDir) home() string                   { return filepath.Join(d.root, "home.ext4") }
func (d vmDir) log() string                    { return filepath.Join(d.root, "firecracker.log") }
func (d vmDir) pid() string                    { return filepath.Join(d.root, "pid") }
func (d vmDir) net() string                    { return filepath.Join(d.root, "net.json") }

type netInfo struct {
	Index int    `json:"index"`
	Tap   string `json:"tap"`
	Host  string `json:"host"`
	Guest string `json:"guest"`
	MAC   string `json:"mac"`
}

// netFor reserviert (stabil pro Fylgja) ein /30-Netz. Der Index kommt aus den bereits vergebenen Netzen.
func (f *Firecracker) netFor(dot uuid.UUID) (netInfo, error) {
	d := f.dir(dot)
	if b, err := os.ReadFile(d.net()); err == nil {
		var n netInfo
		if json.Unmarshal(b, &n) == nil && n.Guest != "" {
			return n, nil
		}
	}
	used := map[int]bool{}
	entries, _ := os.ReadDir(f.DataDir)
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join(f.DataDir, e.Name(), "net.json")); err == nil {
			var n netInfo
			if json.Unmarshal(b, &n) == nil {
				used[n.Index] = true
			}
		}
	}
	idx := -1
	for i := 0; i < 16384; i++ { // 172.31.0.0/16 enthält 16384 /30-Netze
		if !used[i] {
			idx = i
			break
		}
	}
	if idx < 0 {
		return netInfo{}, errors.New("firecracker: keine freien adressen mehr")
	}
	base := idx * 4
	ip := func(last int) string { return fmt.Sprintf("%s.%d.%d", f.subnet(), (base+last)/256, (base+last)%256) }
	n := netInfo{Index: idx, Tap: "fc" + strconv.Itoa(idx), Host: ip(1), Guest: ip(2),
		MAC: fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", 172, 31, (base+2)/256, (base+2)%256)}
	b, _ := json.Marshal(n)
	if err := os.MkdirAll(d.root, 0o750); err != nil {
		return n, err
	}
	return n, os.WriteFile(d.net(), b, 0o640)
}

// ---- Firecracker-API über Unix-Socket ----

func sockClient(path string) *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
}

func apiCall(ctx context.Context, hc *http.Client, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://fc"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("firecracker %s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(b))
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func alive(pidFile string) (int, bool) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	// Signal 0 prüft nur die Existenz. Ein wiederverwendeter PID wird zusätzlich über den Socket abgefangen.
	return pid, syscall.Kill(pid, 0) == nil
}

func (f *Firecracker) running(ctx context.Context, d vmDir) bool {
	if _, ok := alive(d.pid()); !ok {
		return false
	}
	var info struct {
		State string `json:"state"`
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return apiCall(c, sockClient(d.sock()), http.MethodGet, "/", nil, &info) == nil && info.State == "Running"
}

// guestConfig geht base64-kodiert auf der Kernel-Kommandozeile an die VM (Token, Proxy, Egress-Modus).
type guestConfig struct {
	Token  string `json:"token"`
	Proxy  string `json:"proxy,omitempty"`
	Egress string `json:"egress"`
}

// BootArgsFor baut die Kernel-Kommandozeile. Exportiert für Tests.
func (f *Firecracker) BootArgsFor(n netInfo, gc guestConfig) string {
	cfg, _ := json.Marshal(gc)
	args := []string{"console=ttyS0", "reboot=k", "panic=1", "pci=off", "8250.nr_uarts=1", "random.trust_cpu=on", "root=/dev/vda", "ro", "init=/sbin/fylgja-init",
		fmt.Sprintf("ip=%s::%s:255.255.255.252::eth0:off", n.Guest, n.Host), "fylgja.cfg=" + base64.RawURLEncoding.EncodeToString(cfg)}
	if f.BootArgs != "" {
		args = append(args, f.BootArgs)
	}
	return strings.Join(args, " ")
}

// Ensure legt die VM an oder startet sie wieder (das Home-Image bleibt erhalten).
func (f *Firecracker) Ensure(ctx context.Context, s Spec) (Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DataDir == "" || f.Kernel == "" || f.Rootfs == "" {
		return Instance{}, errors.New("firecracker: data_dir, kernel und rootfs müssen konfiguriert sein")
	}
	d := f.dir(s.DotID)
	if err := os.MkdirAll(d.root, 0o750); err != nil {
		return Instance{}, err
	}
	n, err := f.netFor(s.DotID)
	if err != nil {
		return Instance{}, err
	}
	inst := Instance{Ref: s.DotID.String(), Endpoint: "http://" + n.Guest + ":7070", VNC: "http://" + n.Guest + ":6080", Volume: d.home()}
	if f.running(ctx, d) {
		return inst, nil
	}
	// Reste einer toten VM aufräumen.
	_ = os.Remove(d.sock())
	if _, err := os.Stat(d.home()); errors.Is(err, os.ErrNotExist) {
		if _, err := f.run(ctx, "truncate", "-s", strconv.Itoa(f.homeMB())+"M", d.home()); err != nil {
			return Instance{}, err
		}
		// uid/gid 1000 gehört dem Benutzer "dot" in der VM.
		if _, err := f.run(ctx, "mkfs.ext4", "-q", "-F", "-E", "root_owner=1000:1000", "-L", "home", d.home()); err != nil {
			_ = os.Remove(d.home())
			return Instance{}, err
		}
	}
	if err := f.setupTap(ctx, n); err != nil {
		return Instance{}, err
	}
	pid, err := f.spawn(ctx, f.bin(), []string{"--api-sock", d.sock(), "--id", s.DotID.String()[:8]}, d.log())
	if err != nil {
		return Instance{}, err
	}
	if err := os.WriteFile(d.pid(), []byte(strconv.Itoa(pid)), 0o640); err != nil {
		return Instance{}, err
	}
	hc := sockClient(d.sock())
	if err := waitSock(ctx, d.sock()); err != nil {
		f.kill(d)
		return Instance{}, err
	}
	gc := guestConfig{Token: s.Token, Egress: s.Egress}
	if s.Egress != "offline" {
		gc.Proxy = f.proxyFor(n)
	}
	steps := []struct {
		path string
		body any
	}{
		{"/machine-config", map[string]any{"vcpu_count": max(1, int(s.CPUs+0.5)), "mem_size_mib": max(256, s.MemoryMB), "smt": false}},
		{"/boot-source", map[string]any{"kernel_image_path": f.Kernel, "boot_args": f.BootArgsFor(n, gc)}},
		{"/drives/rootfs", map[string]any{"drive_id": "rootfs", "path_on_host": f.Rootfs, "is_root_device": true, "is_read_only": true}},
		{"/drives/home", map[string]any{"drive_id": "home", "path_on_host": d.home(), "is_root_device": false, "is_read_only": false}},
		{"/network-interfaces/eth0", map[string]any{"iface_id": "eth0", "host_dev_name": n.Tap, "guest_mac": n.MAC}},
		{"/actions", map[string]any{"action_type": "InstanceStart"}},
	}
	for _, st := range steps {
		if err := apiCall(ctx, hc, http.MethodPut, st.path, st.body, nil); err != nil {
			f.kill(d)
			return Instance{}, err
		}
	}
	return inst, nil
}

// proxyFor: Der Egress-Proxy läuft auf dem Host und ist über die Host-Adresse des tap-Netzes erreichbar.
func (f *Firecracker) proxyFor(n netInfo) string { return "http://" + n.Host + ":3128" }

func waitSock(ctx context.Context, path string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return errors.New("firecracker: api-socket kommt nicht hoch (läuft /dev/kvm?)")
}

func (f *Firecracker) setupTap(ctx context.Context, n netInfo) error {
	// Existierendes Gerät ist kein Fehler (z. B. nach Neustart des Control Planes).
	_, _ = f.run(ctx, "ip", "link", "del", n.Tap)
	steps := [][]string{
		{"ip", "tuntap", "add", "dev", n.Tap, "mode", "tap"},
		{"ip", "addr", "add", n.Host + "/30", "dev", n.Tap},
		{"ip", "link", "set", "dev", n.Tap, "up"},
	}
	for _, s := range steps {
		if _, err := f.run(ctx, s[0], s[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (f *Firecracker) kill(d vmDir) {
	if pid, ok := alive(d.pid()); ok {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	_ = os.Remove(d.pid())
	_ = os.Remove(d.sock())
}

// stop fährt die VM sauber herunter (Ctrl-Alt-Del), nach 10 s wird sie beendet.
func (f *Firecracker) stop(ctx context.Context, dot uuid.UUID) {
	d := f.dir(dot)
	pid, ok := alive(d.pid())
	if !ok {
		_ = os.Remove(d.sock())
		return
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	_ = apiCall(c, sockClient(d.sock()), http.MethodPut, "/actions", map[string]any{"action_type": "SendCtrlAltDel"}, nil)
	cancel()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	f.kill(d)
	if b, err := os.ReadFile(d.net()); err == nil {
		var n netInfo
		if json.Unmarshal(b, &n) == nil && n.Tap != "" {
			_, _ = f.run(ctx, "ip", "link", "del", n.Tap)
		}
	}
}

func (f *Firecracker) Sleep(ctx context.Context, ref string) error {
	dot, err := uuid.Parse(ref)
	if err != nil {
		return err
	}
	f.stop(ctx, dot)
	return nil
}

func (f *Firecracker) Destroy(ctx context.Context, ref string, keepVolume bool) error {
	dot, err := uuid.Parse(ref)
	if err != nil {
		return err
	}
	f.stop(ctx, dot)
	d := f.dir(dot)
	if keepVolume {
		_ = os.Remove(d.net()) // Adresse wird freigegeben, das Home bleibt
		return nil
	}
	return os.RemoveAll(d.root)
}

func (f *Firecracker) Stats(ctx context.Context, ref string) (map[string]any, error) {
	dot, err := uuid.Parse(ref)
	if err != nil {
		return nil, err
	}
	d := f.dir(dot)
	var info map[string]any
	if err := apiCall(ctx, sockClient(d.sock()), http.MethodGet, "/", nil, &info); err != nil {
		return nil, err
	}
	if st, err := os.Stat(d.home()); err == nil {
		info["home_bytes_apparent"] = st.Size()
	}
	return info, nil
}
