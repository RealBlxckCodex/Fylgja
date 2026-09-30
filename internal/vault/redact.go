package vault

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Mask ersetzt redigierte Werte.
const Mask = "[REDACTED]"

// Muster für bekannte Secret-Formate (Muster-Scanner, Spec 13.8).
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{50,}\b`),
	regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_\-]{20,}\b`),
	regexp.MustCompile(`\bxox[abpors]-[A-Za-z0-9\-]{10,}\b`),
	regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_\-]{35}\b`), // Telegram-Bot-Token
	regexp.MustCompile(`\brpa_[A-Za-z0-9]{20,}\b`),        // RunPod
	regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9\-._~+/]{20,}=*`),
	regexp.MustCompile(`(?i)\b(password|passwort|passwd|secret|api[_-]?key|token)\s*[:=]\s*["']?[^\s"']{6,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`), // JWT
}

// Redactor entfernt bekannte Secrets (Exact-Match) und Muster aus Text.
// Alle Tool-Ergebnisse, Logs und Journale laufen hier durch.
type Redactor struct {
	mu    sync.RWMutex
	exact map[string]struct{}
}

func NewRedactor() *Redactor { return &Redactor{exact: map[string]struct{}{}} }

// Register merkt sich einen Secret-Wert für Exact-Match-Redaction (min. 4 Zeichen).
func (r *Redactor) Register(secret string) {
	if len(secret) < 4 {
		return
	}
	r.mu.Lock()
	r.exact[secret] = struct{}{}
	r.mu.Unlock()
}

// Redact liefert s ohne Secrets.
func (r *Redactor) Redact(s string) string {
	if r != nil {
		r.mu.RLock()
		keys := make([]string, 0, len(r.exact))
		for k := range r.exact {
			keys = append(keys, k)
		}
		r.mu.RUnlock()
		// Längste zuerst, damit Teilstrings nicht halbe Secrets übrig lassen.
		sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
		for _, k := range keys {
			s = strings.ReplaceAll(s, k, Mask)
		}
	}
	for _, p := range secretPatterns {
		s = p.ReplaceAllStringFunc(s, func(m string) string {
			// Bei "key: value"-Mustern den Schlüssel stehen lassen.
			if i := strings.IndexAny(m, ":="); i > 0 && i < 20 && !strings.HasPrefix(m, "-----") && !strings.HasPrefix(m, "ey") {
				if sub := p.FindStringSubmatch(m); len(sub) > 1 && sub[1] != "" && !strings.EqualFold(sub[1], "bearer") {
					return m[:i+1] + " " + Mask
				}
			}
			if len(m) > 7 && strings.EqualFold(m[:7], "bearer ") {
				return m[:7] + Mask
			}
			return Mask
		})
	}
	return s
}

// ContainsSecret meldet, ob s nach Muster ein Secret enthält (Secret-Scanner für Memory).
func (r *Redactor) ContainsSecret(s string) bool {
	return r.Redact(s) != s
}
