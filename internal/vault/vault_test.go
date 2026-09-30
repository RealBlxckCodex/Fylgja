package vault

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func ring(t *testing.T) *Keyring {
	k, err := ParseMasterKey("this-is-a-very-long-test-passphrase-123")
	if err != nil {
		t.Fatal(err)
	}
	return NewKeyring(k)
}

func TestSealOpenRoundtrip(t *testing.T) {
	k := ring(t)
	ws := uuid.New()
	s, err := k.Seal(ws, []byte("hunter2-canary"), []byte("secret-1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s.Ciphertext, []byte("hunter2")) || bytes.Contains(s.WrappedDEK, []byte("hunter2")) {
		t.Fatal("klartext im ciphertext")
	}
	pt, err := k.Open(ws, s, []byte("secret-1"))
	if err != nil || string(pt) != "hunter2-canary" {
		t.Fatalf("open: %v %q", err, pt)
	}
	if _, err := k.Open(ws, s, []byte("secret-2")); err == nil {
		t.Fatal("falsches AAD akzeptiert")
	}
	if _, err := k.Open(uuid.New(), s, []byte("secret-1")); err == nil {
		t.Fatal("fremder workspace akzeptiert")
	}
}

func TestRotation(t *testing.T) {
	k := ring(t)
	ws := uuid.New()
	s, _ := k.Seal(ws, []byte("v"), nil)
	k.AddVersion(2, bytes.Repeat([]byte{7}, 32))
	s2, err := k.Rewrap(ws, s)
	if err != nil || s2.KeyVersion != 2 {
		t.Fatalf("rewrap %v %d", err, s2.KeyVersion)
	}
	pt, err := k.Open(ws, s2, nil)
	if err != nil || string(pt) != "v" {
		t.Fatal("open nach rotation")
	}
}

func TestShortMasterKeyRejected(t *testing.T) {
	if _, err := ParseMasterKey("short"); err == nil {
		t.Fatal("kurzer key akzeptiert")
	}
}

func TestRedactor(t *testing.T) {
	r := NewRedactor()
	r.Register("CANARY-s3cr3t-value")
	cases := map[string]string{
		"login with CANARY-s3cr3t-value ok":             "CANARY",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789": "ghp_",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz123": "abcdefghijklmnop",
		"password=supergeheim99":                         "supergeheim",
		"key sk-ant-api03-abcdefghijklmnopqrstuvwxyz":    "sk-ant",
		"123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsawx":  "AAHdq",
	}
	for in, leak := range cases {
		out := r.Redact(in)
		if strings.Contains(out, leak) {
			t.Errorf("leak %q in %q", leak, out)
		}
		if !r.ContainsSecret(in) {
			t.Errorf("ContainsSecret false for %q", in)
		}
	}
	if r.ContainsSecret("Hallo, wie geht es dir heute?") {
		t.Error("false positive")
	}
}
