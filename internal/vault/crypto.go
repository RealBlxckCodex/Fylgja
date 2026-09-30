// Package vault implementiert Envelope Encryption für Secrets (Spec 14.8) und den Redactor.
//
// Pro Secret wird ein zufälliger DEK erzeugt (XChaCha20-Poly1305). Der DEK wird mit
// einem Workspace-KEK gewrappt, der per HKDF aus dem Master-Key (FYLGJA_MASTER_KEY)
// und der Workspace-ID abgeleitet wird. key_version erlaubt Rotation.
package vault

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

var ErrDecrypt = errors.New("vault: entschlüsselung fehlgeschlagen")

// Keyring hält die Master-Keys je Version. Die höchste Version verschlüsselt.
type Keyring struct {
	keys    map[int][]byte
	current int
}

// ParseMasterKey akzeptiert base64 (32 Byte) oder eine Passphrase (≥ 32 Zeichen), die per SHA-256 verdichtet wird.
func ParseMasterKey(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if len(s) < 32 {
		return nil, errors.New("vault: master key muss base64(32 Byte) oder eine Passphrase ≥ 32 Zeichen sein")
	}
	h := sha256.Sum256([]byte(s))
	return h[:], nil
}

// NewKeyring erzeugt einen Keyring mit einem Master-Key als Version 1.
func NewKeyring(master []byte) *Keyring {
	return &Keyring{keys: map[int][]byte{1: master}, current: 1}
}

// AddVersion fügt einen neuen Master-Key hinzu und macht ihn zum aktuellen (Rotation).
func (k *Keyring) AddVersion(v int, master []byte) {
	k.keys[v] = master
	if v > k.current {
		k.current = v
	}
}

func (k *Keyring) Current() int { return k.current }

func (k *Keyring) kek(version int, workspace uuid.UUID) ([]byte, error) {
	m, ok := k.keys[version]
	if !ok {
		return nil, fmt.Errorf("vault: key_version %d unbekannt", version)
	}
	r := hkdf.New(sha256.New, m, workspace[:], []byte("fylgja/kek/v1"))
	out := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Sealed ist das Ergebnis einer Verschlüsselung, so wie es in vault_secrets steht.
type Sealed struct {
	Ciphertext []byte
	WrappedDEK []byte
	KeyVersion int
}

func seal(key, plaintext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

func open(key, box, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(box) < aead.NonceSize() {
		return nil, ErrDecrypt
	}
	pt, err := aead.Open(nil, box[:aead.NonceSize()], box[aead.NonceSize():], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// Seal verschlüsselt plaintext für einen Workspace. aad bindet den Ciphertext an
// Kontext (z. B. Secret-ID), damit Ciphertexts nicht vertauscht werden können.
func (k *Keyring) Seal(workspace uuid.UUID, plaintext, aad []byte) (Sealed, error) {
	dek := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(dek); err != nil {
		return Sealed{}, err
	}
	ct, err := seal(dek, plaintext, aad)
	if err != nil {
		return Sealed{}, err
	}
	kek, err := k.kek(k.current, workspace)
	if err != nil {
		return Sealed{}, err
	}
	w, err := seal(kek, dek, workspace[:])
	if err != nil {
		return Sealed{}, err
	}
	return Sealed{Ciphertext: ct, WrappedDEK: w, KeyVersion: k.current}, nil
}

// Open entschlüsselt ein Sealed.
func (k *Keyring) Open(workspace uuid.UUID, s Sealed, aad []byte) ([]byte, error) {
	kek, err := k.kek(s.KeyVersion, workspace)
	if err != nil {
		return nil, err
	}
	dek, err := open(kek, s.WrappedDEK, workspace[:])
	if err != nil {
		return nil, err
	}
	return open(dek, s.Ciphertext, aad)
}

// Rewrap wrappt den DEK mit der aktuellen Key-Version neu (Rotation ohne Neu-Verschlüsselung der Daten).
func (k *Keyring) Rewrap(workspace uuid.UUID, s Sealed) (Sealed, error) {
	if s.KeyVersion == k.current {
		return s, nil
	}
	old, err := k.kek(s.KeyVersion, workspace)
	if err != nil {
		return s, err
	}
	dek, err := open(old, s.WrappedDEK, workspace[:])
	if err != nil {
		return s, err
	}
	kek, err := k.kek(k.current, workspace)
	if err != nil {
		return s, err
	}
	w, err := seal(kek, dek, workspace[:])
	if err != nil {
		return s, err
	}
	return Sealed{Ciphertext: s.Ciphertext, WrappedDEK: w, KeyVersion: k.current}, nil
}
