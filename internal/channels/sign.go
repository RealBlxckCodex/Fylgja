package channels

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// Signer signiert Callback-Daten für Approval-Buttons (kurz, < 64 Byte für Telegram).
type Signer struct{ Key []byte }

func (s Signer) mac(id, action string) string {
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte(id + "|" + action))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:12]
}

// Sign erzeugt "ap|<id32>|<a|d|w>|<mac>".
func (s Signer) Sign(id uuid.UUID, action string) string {
	h := strings.ReplaceAll(id.String(), "-", "")
	return "ap|" + h + "|" + action + "|" + s.mac(h, action)
}

var ErrBadSignature = errors.New("channels: ungültige callback-signatur")

// Verify prüft Callback-Daten und liefert Approval-ID und Aktion.
func (s Signer) Verify(data string) (uuid.UUID, string, error) {
	p := strings.Split(data, "|")
	if len(p) != 4 || p[0] != "ap" {
		return uuid.Nil, "", ErrBadSignature
	}
	if !hmac.Equal([]byte(p[3]), []byte(s.mac(p[1], p[2]))) {
		return uuid.Nil, "", ErrBadSignature
	}
	id, err := uuid.Parse(p[1])
	if err != nil {
		return uuid.Nil, "", ErrBadSignature
	}
	switch p[2] {
	case "a", "d", "w":
	default:
		return uuid.Nil, "", ErrBadSignature
	}
	return id, p[2], nil
}
