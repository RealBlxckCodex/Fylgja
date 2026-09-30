// Package link ist der Laptop-Link (Spec 13.9): ein kleiner Agent auf dem Nutzergerät,
// nur ausgehend (WSS), mit gescopten Fähigkeiten und Kill-Switch. Jede Aktion ist im
// Control Plane Klasse "laptop" (Default: ask) – der Agent erzwingt zusätzlich lokal seine Scopes.
package link

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Scopes werden lokal (Gerät) erzwungen (Defense in Depth).
type Scopes struct {
	Read      []string `json:"read"`       // lesbare Ordner
	Write     []string `json:"write"`      // beschreibbare Ordner
	ExecAllow []string `json:"exec_allow"` // erlaubte Kommando-Präfixe (leer = keine Shell)
}

func within(path string, roots []string) bool {
	p, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	} else if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		p = filepath.Join(r, filepath.Base(p))
	}
	for _, root := range roots {
		ra, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if r, err := filepath.EvalSymlinks(ra); err == nil {
			ra = r
		}
		if p == ra || strings.HasPrefix(p, ra+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// AllowedCommand prüft ein Kommando gegen die Präfix-Allowlist (keine Verkettung).
func AllowedCommand(cmd string, allow []string) bool {
	if strings.ContainsAny(cmd, ";&|`$<>\n") {
		return false
	}
	c := strings.Join(strings.Fields(cmd), " ")
	for _, a := range allow {
		a = strings.Join(strings.Fields(a), " ")
		if a != "" && (c == a || strings.HasPrefix(c, a+" ")) {
			return true
		}
	}
	return false
}

// Local ist der lokale API-Server des Agents (nur 127.0.0.1, nur über den Tunnel erreichbar).
type Local struct {
	Scopes Scopes
	Log    func(action, detail string)
}

func (l *Local) log(a, d string) {
	if l.Log != nil {
		l.Log(a, d)
	}
}

func (l *Local) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/fs/read", func(w http.ResponseWriter, r *http.Request) {
		var a struct{ Path string }
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&a)
		if !within(a.Path, l.Scopes.Read) {
			http.Error(w, "pfad nicht freigegeben", 403)
			return
		}
		l.log("read", a.Path)
		f, err := os.Open(a.Path)
		if err != nil {
			http.Error(w, "nicht lesbar", 404)
			return
		}
		defer f.Close()
		b, _ := io.ReadAll(io.LimitReader(f, 5<<20))
		_ = json.NewEncoder(w).Encode(map[string]any{"data": b})
	})
	mux.HandleFunc("POST /v1/fs/list", func(w http.ResponseWriter, r *http.Request) {
		var a struct{ Path string }
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&a)
		if !within(a.Path, l.Scopes.Read) {
			http.Error(w, "pfad nicht freigegeben", 403)
			return
		}
		l.log("list", a.Path)
		ents, err := os.ReadDir(a.Path)
		if err != nil {
			http.Error(w, "nicht lesbar", 404)
			return
		}
		var out []map[string]any
		for _, e := range ents {
			out = append(out, map[string]any{"name": e.Name(), "dir": e.IsDir()})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /v1/fs/write", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			Path string
			Data []byte
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 10<<20)).Decode(&a)
		if !within(a.Path, l.Scopes.Write) {
			http.Error(w, "schreiben nicht freigegeben", 403)
			return
		}
		l.log("write", a.Path)
		if err := os.WriteFile(a.Path, a.Data, 0o644); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/exec", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			Cmd string `json:"cmd"`
			Cwd string `json:"cwd"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&a)
		if !AllowedCommand(a.Cmd, l.Scopes.ExecAllow) {
			http.Error(w, "kommando nicht in der lokalen allowlist", 403)
			return
		}
		if a.Cwd != "" && !within(a.Cwd, append(l.Scopes.Read, l.Scopes.Write...)) {
			http.Error(w, "arbeitsverzeichnis nicht freigegeben", 403)
			return
		}
		l.log("exec", a.Cmd)
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		f := strings.Fields(a.Cmd)
		c := exec.CommandContext(ctx, f[0], f[1:]...)
		c.Dir = a.Cwd
		out, err := c.CombinedOutput()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = 127
		}
		if len(out) > 1<<20 {
			out = out[:1<<20]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"output": string(out), "exit": code})
	})
	return mux
}

// Serve startet den lokalen Server auf einem zufälligen Loopback-Port und liefert die Adresse.
func (l *Local) Serve(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: l.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), nil
}
