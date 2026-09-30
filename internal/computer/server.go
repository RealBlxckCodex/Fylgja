// Package computer ist computerd: der Tool-Server in der Sandbox einer Fylgja (Spec 13.4).
// Shell, Dateisystem (auf /home/dot begrenzt), Browser via Playwright-MCP, Login-Broker-Ziel.
package computer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/realblxckcodex/fylgja/internal/mcp"
)

// Server ist computerd.
type Server struct {
	Token   string
	Root    string   // /home/dot
	Shell   []string // ["bash","-lc"]
	Browser []string // Playwright-MCP-Kommando, z. B. ["npx","@playwright/mcp@latest","--headless=false"]
	Log     *slog.Logger

	mu  sync.Mutex
	pw  *mcp.Client
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Handler liefert die HTTP-Routen.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST /v1/exec", s.auth(s.exec))
	mux.HandleFunc("POST /v1/fs/read", s.auth(s.read))
	mux.HandleFunc("POST /v1/fs/write", s.auth(s.write))
	mux.HandleFunc("POST /v1/fs/list", s.auth(s.list))
	mux.HandleFunc("POST /v1/browser/{action}", s.auth(s.browser))
	return mux
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.Token == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Resolve begrenzt Pfade auf Root (kein Ausbruch per .. oder Symlink).
func (s *Server) Resolve(p string) (string, error) {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, "workspace", p)
	}
	clean := filepath.Clean(p)
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return "", errors.New("pfad außerhalb des workspace")
	}
	// Symlinks auflösen (existierender Teil des Pfads).
	dir := clean
	for {
		if _, err := os.Lstat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	real, err := filepath.EvalSymlinks(dir)
	if err == nil {
		rroot, _ := filepath.EvalSymlinks(root)
		if real != rroot && !strings.HasPrefix(real, rroot+string(os.PathSeparator)) {
			return "", errors.New("symlink zeigt aus dem workspace")
		}
	}
	return clean, nil
}

type limitedBuf struct {
	b   bytes.Buffer
	max int
}

func (l *limitedBuf) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
			l.b.WriteString("\n…[ausgabe gekürzt]")
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Cmd      string `json:"cmd"`
		Cwd      string `json:"cwd"`
		TimeoutS int    `json:"timeout_s"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil || strings.TrimSpace(a.Cmd) == "" {
		http.Error(w, "cmd fehlt", 400)
		return
	}
	cwd, err := s.Resolve(firstNonEmpty(a.Cwd, "."))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_ = os.MkdirAll(cwd, 0o755)
	to := time.Duration(a.TimeoutS) * time.Second
	if to <= 0 || to > 30*time.Minute {
		to = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), to)
	defer cancel()
	sh := s.Shell
	if len(sh) == 0 {
		sh = []string{"bash", "-lc"}
	}
	cmd := exec.CommandContext(ctx, sh[0], append(sh[1:], a.Cmd)...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "HOME="+s.Root)
	cmd.WaitDelay = 2 * time.Second
	stdout, stderr := &limitedBuf{max: 1 << 20}, &limitedBuf{max: 256 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		switch {
		case ctx.Err() != nil:
			code = 124
			stderr.Write([]byte("timeout nach " + to.String()))
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			code = 127
			stderr.Write([]byte(err.Error()))
		}
	}
	writeJSON(w, map[string]any{"stdout": stdout.b.String(), "stderr": stderr.b.String(), "exit": code})
}

func (s *Server) read(w http.ResponseWriter, r *http.Request) {
	var a struct{ Path string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&a)
	p, err := s.Resolve(a.Path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, "datei nicht lesbar: "+filepath.Base(p), 404)
		return
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 10<<20))
	writeJSON(w, map[string]any{"data": b})
}

func (s *Server) write(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Path string
		Data []byte
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 50<<20)).Decode(&a); err != nil {
		http.Error(w, "ungültig", 400)
		return
	}
	p, err := s.Resolve(a.Path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, a.Data, 0o644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	var a struct{ Path string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&a)
	p, err := s.Resolve(firstNonEmpty(a.Path, "."))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_ = os.MkdirAll(p, 0o755)
	entries, err := os.ReadDir(p)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var out []map[string]any
	for _, e := range entries {
		var info fs.FileInfo
		if info, err = e.Info(); err != nil {
			continue
		}
		out = append(out, map[string]any{"name": e.Name(), "size": info.Size(), "dir": e.IsDir(), "mtime": info.ModTime()})
	}
	writeJSON(w, out)
}

// ---- Browser (Playwright-MCP) ----

func (s *Server) playwright(ctx context.Context) (*mcp.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pw != nil {
		return s.pw, nil
	}
	cmd := s.Browser
	if len(cmd) == 0 {
		cmd = []string{"npx", "-y", "@playwright/mcp@latest", "--browser", "chromium", "--user-data-dir", filepath.Join(s.Root, ".browser")}
	}
	c, err := mcp.Connect(context.WithoutCancel(ctx), mcp.ServerConfig{Name: "playwright", Command: cmd})
	if err != nil {
		return nil, err
	}
	s.pw = c
	return c, nil
}

var toolFor = map[string]string{
	"navigate":   "browser_navigate",
	"snapshot":   "browser_snapshot",
	"click":      "browser_click",
	"type":       "browser_type",
	"screenshot": "browser_take_screenshot",
	"back":       "browser_navigate_back",
	"tabs":       "browser_tabs",
	"pdf":        "browser_pdf_save",
}

func (s *Server) browser(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	var args map[string]any
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&args)
	pw, err := s.playwright(r.Context())
	if err != nil {
		http.Error(w, "browser nicht verfügbar: "+err.Error(), 503)
		return
	}
	if action == "login" {
		s.login(w, r, pw, args)
		return
	}
	tool, ok := toolFor[action]
	if !ok {
		http.Error(w, "unbekannte aktion", 400)
		return
	}
	var egress []string
	if u, ok := args["url"].(string); ok {
		if pu, err := url.Parse(u); err == nil {
			egress = append(egress, strings.ToLower(pu.Hostname()))
		}
	}
	if action == "click" || action == "type" {
		if _, ok := args["element"]; !ok {
			args["element"] = args["ref"]
		}
	}
	out, isErr, err := pw.Call(r.Context(), tool, args)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	if isErr {
		out = "FEHLER: " + out
	}
	writeJSON(w, map[string]any{"text": out, "egress": egress})
}

var refRe = regexp.MustCompile(`(?m)^\s*-\s*textbox\s+"([^"]*)"[^\n]*\[ref=([^\]]+)\]`)

// FindLoginFields sucht Benutzer- und Passwortfeld im Accessibility-Snapshot.
func FindLoginFields(snapshot string) (userRef, passRef string) {
	for _, m := range refRe.FindAllStringSubmatch(snapshot, -1) {
		label := strings.ToLower(m[1])
		switch {
		case passRef == "" && (strings.Contains(label, "passw") || strings.Contains(label, "kennwort")):
			passRef = m[2]
		case userRef == "" && (strings.Contains(label, "mail") || strings.Contains(label, "user") || strings.Contains(label, "benutzer") || strings.Contains(label, "login") || strings.Contains(label, "name")):
			userRef = m[2]
		}
	}
	return
}

// login befüllt Felder, ohne dass Secrets jemals in der Antwort landen.
func (s *Server) login(w http.ResponseWriter, r *http.Request, pw *mcp.Client, a map[string]any) {
	site, _ := a["site"].(string)
	user, _ := a["username"].(string)
	pass, _ := a["password"].(string)
	totp, _ := a["totp_secret"].(string)
	redact := func(t string) string {
		for _, sec := range []string{pass, totp} {
			if len(sec) >= 4 {
				t = strings.ReplaceAll(t, sec, "[REDACTED]")
			}
		}
		return t
	}
	if _, _, err := pw.Call(r.Context(), "browser_navigate", map[string]any{"url": site}); err != nil {
		http.Error(w, "navigation fehlgeschlagen", 502)
		return
	}
	snap, _, err := pw.Call(r.Context(), "browser_snapshot", map[string]any{})
	if err != nil {
		http.Error(w, "snapshot fehlgeschlagen", 502)
		return
	}
	uref, pref := FindLoginFields(snap)
	if pref == "" {
		writeJSON(w, map[string]any{"text": "Login fehlgeschlagen: kein Passwortfeld gefunden (evtl. mehrstufiger Login – Owner-Take-over nötig)."})
		return
	}
	if uref != "" && user != "" {
		_, _, _ = pw.Call(r.Context(), "browser_type", map[string]any{"element": "Benutzername", "ref": uref, "text": user})
	}
	if _, _, err := pw.Call(r.Context(), "browser_type", map[string]any{"element": "Passwort", "ref": pref, "text": pass, "submit": true}); err != nil {
		http.Error(w, "eingabe fehlgeschlagen", 502)
		return
	}
	if totp != "" {
		time.Sleep(2 * time.Second)
		snap2, _, _ := pw.Call(r.Context(), "browser_snapshot", map[string]any{})
		if m := regexp.MustCompile(`(?i)textbox\s+"[^"]*(code|otp|2fa|token)[^"]*"[^\n]*\[ref=([^\]]+)\]`).FindStringSubmatch(snap2); m != nil {
			code, err := TOTP(totp, time.Now())
			if err == nil {
				_, _, _ = pw.Call(r.Context(), "browser_type", map[string]any{"element": "Einmalcode", "ref": m[2], "text": code, "submit": true})
			}
		}
	}
	time.Sleep(2 * time.Second)
	after, _, _ := pw.Call(r.Context(), "browser_snapshot", map[string]any{})
	ok := !strings.Contains(strings.ToLower(after), "passw")
	msg := "Login abgeschlossen."
	if !ok {
		msg = "Login vermutlich fehlgeschlagen (Passwortfeld noch sichtbar)."
	}
	writeJSON(w, map[string]any{"text": redact(msg)})
}

// TOTP berechnet einen RFC-6238-Code (lokal in der Sandbox, Seed verlässt sie nicht).
func TOTP(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	h := m.Sum(nil)
	off := h[len(h)-1] & 0x0f
	v := (binary.BigEndian.Uint32(h[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", v), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
