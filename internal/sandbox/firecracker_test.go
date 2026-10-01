package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeFC ersetzt den firecracker-Prozess: ein HTTP-Server auf dem Unix-Socket und ein harmloser Kindprozess für die PID.
type fakeFC struct {
	mu      sync.Mutex
	puts    []string
	bodies  map[string]map[string]any
	state   string
	spawns  int
	cmds    []string
	procs   map[string]*exec.Cmd
	servers []*http.Server
}

func newFake(t *testing.T) (*fakeFC, *Firecracker) {
	dir := t.TempDir()
	fc := &fakeFC{bodies: map[string]map[string]any{}, procs: map[string]*exec.Cmd{}}
	f := &Firecracker{Kernel: "/k/vmlinux", Rootfs: "/r/rootfs.ext4", DataDir: dir, HomeMB: 64}
	f.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		fc.mu.Lock()
		fc.cmds = append(fc.cmds, name+" "+strings.Join(args, " "))
		fc.mu.Unlock()
		if name == "truncate" {
			return nil, os.WriteFile(args[len(args)-1], nil, 0o600)
		}
		return nil, nil
	}
	f.Spawn = func(_ context.Context, bin string, args []string, logPath string) (int, error) {
		sock := args[1]
		fc.mu.Lock()
		fc.spawns++
		fc.state = "Not started"
		fc.mu.Unlock()
		l, err := net.Listen("unix", sock)
		if err != nil {
			return 0, err
		}
		cmd := exec.Command("sleep", "120")
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		fc.procs[sock] = cmd
		go func() { _ = cmd.Wait() }() // wie im echten spawn: Zombies vermeiden
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fc.mu.Lock()
			defer fc.mu.Unlock()
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode(map[string]any{"state": fc.state, "id": "x"})
				return
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			fc.puts = append(fc.puts, r.URL.Path)
			fc.bodies[r.URL.Path] = body
			switch {
			case r.URL.Path == "/actions" && body["action_type"] == "InstanceStart":
				fc.state = "Running"
			case r.URL.Path == "/actions" && body["action_type"] == "SendCtrlAltDel":
				_ = cmd.Process.Kill()
			}
			w.WriteHeader(204)
		})
		srv := &http.Server{Handler: mux}
		fc.servers = append(fc.servers, srv)
		go srv.Serve(l)
		return cmd.Process.Pid, nil
	}
	t.Cleanup(func() {
		for _, s := range fc.servers {
			s.Close()
		}
		for _, c := range fc.procs {
			_ = c.Process.Kill()
		}
	})
	return fc, f
}

func TestFirecrackerLifecycle(t *testing.T) {
	fc, f := newFake(t)
	ctx := context.Background()
	dot := uuid.New()
	inst, err := f.Ensure(ctx, Spec{DotID: dot, Token: "tok-geheim", MemoryMB: 2048, CPUs: 2, Egress: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Ref != dot.String() || !strings.HasPrefix(inst.Endpoint, "http://172.31.0.2:") {
		t.Fatalf("%+v", inst)
	}
	// Reihenfolge der API-Aufrufe: Start zuletzt.
	want := []string{"/machine-config", "/boot-source", "/drives/rootfs", "/drives/home", "/network-interfaces/eth0", "/actions"}
	if strings.Join(fc.puts, ",") != strings.Join(want, ",") {
		t.Fatalf("api-aufrufe: %v", fc.puts)
	}
	if fc.bodies["/machine-config"]["mem_size_mib"].(float64) != 2048 || fc.bodies["/machine-config"]["vcpu_count"].(float64) != 2 {
		t.Fatalf("machine-config: %v", fc.bodies["/machine-config"])
	}
	if rf := fc.bodies["/drives/rootfs"]; rf["is_read_only"] != true || rf["is_root_device"] != true {
		t.Fatalf("rootfs muss read-only sein: %v", rf)
	}
	if h := fc.bodies["/drives/home"]; h["is_read_only"] != false || h["path_on_host"] != filepath.Join(f.DataDir, dot.String(), "home.ext4") {
		t.Fatalf("home: %v", h)
	}
	// Kernel-Kommandozeile: statische IP, Konfiguration mit Token und Proxy.
	args := fc.bodies["/boot-source"]["boot_args"].(string)
	if !strings.Contains(args, "ip=172.31.0.2::172.31.0.1:255.255.255.252::eth0:off") || !strings.Contains(args, "root=/dev/vda ro") {
		t.Fatalf("boot_args: %s", args)
	}
	var cfg guestConfig
	for _, a := range strings.Fields(args) {
		if v, ok := strings.CutPrefix(a, "fylgja.cfg="); ok {
			b, _ := base64.RawURLEncoding.DecodeString(v)
			_ = json.Unmarshal(b, &cfg)
		}
	}
	if cfg.Token != "tok-geheim" || cfg.Proxy != "http://172.31.0.1:3128" || cfg.Egress != "open" {
		t.Fatalf("gast-konfiguration: %+v", cfg)
	}
	joined := strings.Join(fc.cmds, "\n")
	for _, w := range []string{"truncate -s 64M", "mkfs.ext4", "root_owner=1000:1000", "ip tuntap add dev fc0 mode tap", "ip addr add 172.31.0.1/30 dev fc0", "ip link set dev fc0 up"} {
		if !strings.Contains(joined, w) {
			t.Errorf("befehl fehlt: %s\n%s", w, joined)
		}
	}
	// Zweiter Ensure startet keine zweite VM.
	if _, err := f.Ensure(ctx, Spec{DotID: dot, Token: "tok-geheim", MemoryMB: 2048, CPUs: 2}); err != nil || fc.spawns != 1 {
		t.Fatalf("spawns=%d err=%v", fc.spawns, err)
	}
	// Schlafen: VM beendet, Home bleibt, Aufwecken nutzt dieselbe Adresse und formatiert nicht neu.
	if err := f.Sleep(ctx, inst.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.DataDir, dot.String(), "home.ext4")); err != nil {
		t.Fatal("home-image weg")
	}
	mk := strings.Count(strings.Join(fc.cmds, "\n"), "mkfs.ext4")
	inst2, err := f.Ensure(ctx, Spec{DotID: dot, Token: "tok-geheim", MemoryMB: 2048, CPUs: 2, Egress: "offline"})
	if err != nil {
		t.Fatal(err)
	}
	if inst2.Endpoint != inst.Endpoint || fc.spawns != 2 || strings.Count(strings.Join(fc.cmds, "\n"), "mkfs.ext4") != mk {
		t.Fatalf("wake: %+v spawns=%d", inst2, fc.spawns)
	}
	// Offline: kein Proxy in der Gast-Konfiguration.
	args = fc.bodies["/boot-source"]["boot_args"].(string)
	for _, a := range strings.Fields(args) {
		if v, ok := strings.CutPrefix(a, "fylgja.cfg="); ok {
			b, _ := base64.RawURLEncoding.DecodeString(v)
			cfg = guestConfig{}
			_ = json.Unmarshal(b, &cfg)
		}
	}
	if cfg.Proxy != "" || cfg.Egress != "offline" {
		t.Fatalf("offline-konfiguration: %+v", cfg)
	}
	// Destroy ohne Volume entfernt alles.
	if err := f.Destroy(ctx, inst.Ref, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.DataDir, dot.String())); !os.IsNotExist(err) {
		t.Fatal("verzeichnis noch da")
	}
}

func TestFirecrackerDistinctNetworks(t *testing.T) {
	_, f := newFake(t)
	ctx := context.Background()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		inst, err := f.Ensure(ctx, Spec{DotID: uuid.New(), Token: "t", MemoryMB: 512, CPUs: 1})
		if err != nil {
			t.Fatal(err)
		}
		if seen[inst.Endpoint] {
			t.Fatalf("adresse doppelt vergeben: %s", inst.Endpoint)
		}
		seen[inst.Endpoint] = true
	}
	// /30-Raster: .2, .6, .10 …
	if !seen["http://172.31.0.6:7070"] || !seen["http://172.31.0.18:7070"] {
		t.Fatalf("%v", seen)
	}
}

func TestFirecrackerNeedsConfig(t *testing.T) {
	f := &Firecracker{}
	if _, err := f.Ensure(context.Background(), Spec{DotID: uuid.New()}); err == nil {
		t.Fatal("fehlende konfiguration nicht erkannt")
	}
	_ = time.Second
}
