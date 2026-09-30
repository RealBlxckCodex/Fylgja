package computer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJailAndExec(t *testing.T) {
	root := t.TempDir()
	s := &Server{Token: "tok", Root: root}
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd", root + "/../x"} {
		if _, err := s.Resolve(bad); err == nil {
			t.Errorf("%s akzeptiert", bad)
		}
	}
	os.MkdirAll(filepath.Join(root, "workspace"), 0o755)
	os.Symlink("/etc", filepath.Join(root, "workspace", "evil"))
	if _, err := s.Resolve("evil/passwd"); err == nil {
		t.Error("symlink-ausbruch")
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	post := func(path, tok string, body any) *http.Response {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if post("/v1/exec", "falsch", map[string]string{"cmd": "echo hi"}).StatusCode != 401 {
		t.Fatal("falsches token akzeptiert")
	}
	resp := post("/v1/exec", "tok", map[string]any{"cmd": "echo hallo && pwd && exit 3"})
	var out struct {
		Stdout string
		Exit   int
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if !strings.Contains(out.Stdout, "hallo") || !strings.Contains(out.Stdout, "workspace") || out.Exit != 3 {
		t.Fatalf("%+v", out)
	}
	post("/v1/fs/write", "tok", map[string]any{"path": "notes/a.txt", "data": []byte("inhalt")})
	resp = post("/v1/fs/read", "tok", map[string]any{"path": "notes/a.txt"})
	var rd struct{ Data []byte }
	json.NewDecoder(resp.Body).Decode(&rd)
	if string(rd.Data) != "inhalt" {
		t.Fatal("read/write")
	}
	resp = post("/v1/exec", "tok", map[string]any{"cmd": "sleep 5", "timeout_s": 1})
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Exit != 124 {
		t.Fatalf("timeout: %+v", out)
	}
}

func TestTOTPAndLoginFields(t *testing.T) {
	// RFC 6238 Testvektor (SHA1, T=59) → 94287082 (8 Stellen) → 6 Stellen: 287082
	code, err := TOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0))
	if err != nil || code != "287082" {
		t.Fatalf("%s %v", code, err)
	}
	snap := `- textbox "E-Mail-Adresse" [ref=e12]
- textbox "Passwort" [ref=e15]
- button "Anmelden" [ref=e20]`
	u, p := FindLoginFields(snap)
	if u != "e12" || p != "e15" {
		t.Fatalf("%s %s", u, p)
	}
}
