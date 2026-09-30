// Package skills: Skill-Store mit Permission-Manifest, statischem Scanner und
// Ed25519-Signatur pro Workspace (Spec 12.6). Nicht signierte Skills sind nie aktiv.
package skills

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/hkdf"
)

// Manifest beschreibt, was ein Skill darf.
type Manifest struct {
	Tools   []string `json:"tools"`
	Egress  []string `json:"egress"`
	Secrets []string `json:"secrets"`
}

// Finding ist ein Scanner-Treffer.
type Finding struct {
	Rule    string `json:"rule"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
}

var rules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"pipe-to-shell", regexp.MustCompile(`(?i)(curl|wget)[^\n|]*\|\s*(ba|z)?sh`)},
	{"base64-exec", regexp.MustCompile(`(?i)base64\s+(-d|--decode)[^\n]*\|\s*(ba)?sh`)},
	{"eval-obfuscation", regexp.MustCompile(`(?i)\beval\s*\(\s*(atob|unescape|decodeURIComponent|Buffer\.from)`)},
	{"secret-access", regexp.MustCompile(`(?i)(\.ssh/id_|\.aws/credentials|/etc/shadow|FYLGJA_MASTER_KEY|COMPUTERD_TOKEN)`)},
	{"exfil-webhook", regexp.MustCompile(`(?i)(webhook\.site|requestbin|ngrok\.io|pastebin\.com|transfer\.sh)`)},
	{"reverse-shell", regexp.MustCompile(`(?i)(/dev/tcp/|nc\s+-e|ncat\s+.*-e|socat\s+exec)`)},
	{"prompt-injection", regexp.MustCompile(`(?i)(ignore (all )?(previous|prior) instructions|ignoriere (alle )?(vorherigen|bisherigen) anweisungen|disable (the )?(policy|approval))`)},
	{"persistence", regexp.MustCompile(`(?i)(crontab\s+-|systemctl\s+enable|\.bashrc|\.profile)`)},
}

// Scan prüft Skill-Text und Dateien statisch.
func Scan(body string, files map[string]string) []Finding {
	var out []Finding
	check := func(name, text string) {
		for i, line := range strings.Split(text, "\n") {
			for _, r := range rules {
				if r.re.MatchString(line) {
					ex := strings.TrimSpace(line)
					if len(ex) > 120 {
						ex = ex[:120]
					}
					out = append(out, Finding{Rule: r.name + "@" + name, Line: i + 1, Excerpt: ex})
				}
			}
		}
	}
	check("SKILL.md", body)
	for n, c := range files {
		check(n, c)
	}
	return out
}

// Key leitet den Workspace-Signaturschlüssel aus dem Master-Key ab.
func Key(master []byte, ws uuid.UUID) ed25519.PrivateKey {
	r := hkdf.New(sha256.New, master, ws[:], []byte("fylgja/skills/ed25519"))
	seed := make([]byte, ed25519.SeedSize)
	_, _ = io.ReadFull(r, seed)
	return ed25519.NewKeyFromSeed(seed)
}

// Digest bindet Name, Version, Text, Dateien und Manifest (Hash-Pinning).
func Digest(name string, version int, body string, files map[string]string, m Manifest) []byte {
	b, _ := json.Marshal(struct {
		N string
		V int
		B string
		F map[string]string
		M Manifest
	}{name, version, body, files, m})
	h := sha256.Sum256(b)
	return h[:]
}

var (
	ErrScanner   = errors.New("skills: scanner hat verdächtige muster gefunden")
	ErrSignature = errors.New("skills: signatur ungültig (skill wurde nach der freigabe verändert)")
)

// Store verwaltet Skills.
type Store struct {
	Pool   *pgxpool.Pool
	Master []byte
}

type row struct {
	ws       uuid.UUID
	name     string
	version  int
	body     string
	files    map[string]string
	manifest Manifest
	sig      []byte
	status   string
}

func (s *Store) load(ctx context.Context, id uuid.UUID) (*row, error) {
	var r row
	var files, man []byte
	err := s.Pool.QueryRow(ctx, `SELECT workspace_id, name, version, body_md, files, manifest, signature, status FROM skills WHERE id=$1`, id).
		Scan(&r.ws, &r.name, &r.version, &r.body, &files, &man, &r.sig, &r.status)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(files, &r.files)
	_ = json.Unmarshal(man, &r.manifest)
	return &r, nil
}

// Activate prüft (Scanner) und signiert einen Skill. Aufrufer muss Approval/Step-up erzwungen haben.
func (s *Store) Activate(ctx context.Context, id uuid.UUID) ([]Finding, error) {
	r, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if f := Scan(r.body, r.files); len(f) > 0 {
		return f, ErrScanner
	}
	sig := ed25519.Sign(Key(s.Master, r.ws), Digest(r.name, r.version, r.body, r.files, r.manifest))
	_, err = s.Pool.Exec(ctx, `UPDATE skills SET signature=$2, status='active' WHERE id=$1`, id, sig)
	return nil, err
}

// Verify prüft die Signatur; Änderungen nach Aktivierung deaktivieren den Skill.
func (s *Store) Verify(ctx context.Context, id uuid.UUID) error {
	r, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	key := Key(s.Master, r.ws)
	if len(r.sig) == 0 || !ed25519.Verify(key.Public().(ed25519.PublicKey), Digest(r.name, r.version, r.body, r.files, r.manifest), r.sig) {
		_, _ = s.Pool.Exec(ctx, `UPDATE skills SET status='disabled' WHERE id=$1 AND status='active'`, id)
		return ErrSignature
	}
	return nil
}

// VerifyAll prüft alle aktiven Skills (beim Start und periodisch).
func (s *Store) VerifyAll(ctx context.Context) (disabled int, err error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM skills WHERE status='active'`)
	if err != nil {
		return 0, err
	}
	var list []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		_ = rows.Scan(&id)
		list = append(list, id)
	}
	rows.Close()
	for _, id := range list {
		if err := s.Verify(ctx, id); err != nil {
			disabled++
		}
	}
	return disabled, nil
}

// Parse liest ein SKILL.md mit Frontmatter (name, description, tools, egress).
func Parse(md string) (name, desc string, m Manifest, body string, err error) {
	if !strings.HasPrefix(md, "---\n") {
		return "", "", m, "", errors.New("skills: frontmatter fehlt")
	}
	end := strings.Index(md[4:], "\n---")
	if end < 0 {
		return "", "", m, "", errors.New("skills: frontmatter nicht geschlossen")
	}
	front, body := md[4:4+end], strings.TrimLeft(md[4+end+4:], "\n")
	for _, line := range strings.Split(front, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		list := func() []string {
			v = strings.Trim(v, "[]")
			var out []string
			for _, x := range strings.Split(v, ",") {
				if x = strings.Trim(strings.TrimSpace(x), `"'`); x != "" {
					out = append(out, x)
				}
			}
			return out
		}
		switch strings.TrimSpace(k) {
		case "name":
			name = strings.Trim(v, `"'`)
		case "description":
			desc = strings.Trim(v, `"'`)
		case "tools":
			m.Tools = list()
		case "egress":
			m.Egress = list()
		case "secrets":
			m.Secrets = list()
		}
	}
	if name == "" {
		return "", "", m, "", fmt.Errorf("skills: name fehlt")
	}
	return name, desc, m, body, nil
}
