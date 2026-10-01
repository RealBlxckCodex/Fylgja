// Package pki stellt kurzlebige Client-Zertifikate für Nodes aus (Spec 16.9, mTLS).
//
// Die CA wird deterministisch aus dem Master-Key abgeleitet (HKDF → Ed25519-Seed, feste
// Gültigkeit und Seriennummer). Dadurch brauchen mehrere Control-Plane-Replikas keine
// geteilte Ablage, und ein Neustart erzeugt dieselbe CA. Wer den Master-Key hat, kann die
// CA ohnehin ableiten – er ist die Vertrauenswurzel des Systems (siehe vault).
package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"

	"golang.org/x/crypto/hkdf"
)

// DefaultTTL ist die Gültigkeit eines Node-Zertifikats.
const DefaultTTL = 24 * time.Hour

// CA signiert Node-Zertifikate.
type CA struct {
	key  ed25519.PrivateKey
	cert *x509.Certificate
	der  []byte
	pool *x509.CertPool
}

// NewCA leitet die CA aus dem Master-Key ab.
func NewCA(master []byte) (*CA, error) {
	if len(master) < 16 {
		return nil, errors.New("pki: master-key zu kurz")
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte("fylgja/node-ca/v1")), seed); err != nil {
		return nil, err
	}
	key := ed25519.NewKeyFromSeed(seed)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Fylgja Node CA", Organization: []string{"Fylgja"}},
		NotBefore:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2056, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{key: key, cert: cert, der: der, pool: pool}, nil
}

// Pool liefert den Trust-Pool (für ClientCAs).
func (c *CA) Pool() *x509.CertPool { return c.pool }

// CertPEM liefert das CA-Zertifikat.
func (c *CA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der})
}

// SignCSR stellt ein Client-Zertifikat für nodeID aus. Subject und SANs der CSR werden
// ignoriert: Der Common Name ist immer die Node-ID, nur Client-Authentifizierung ist erlaubt.
func (c *CA) SignCSR(csrPEM []byte, nodeID string, ttl time.Duration, now time.Time) (certPEM []byte, notAfter time.Time, err error) {
	if nodeID == "" {
		return nil, time.Time{}, errors.New("pki: node-id fehlt")
	}
	if ttl <= 0 || ttl > 7*24*time.Hour {
		ttl = DefaultTTL
	}
	blk, _ := pem.Decode(csrPEM)
	if blk == nil || blk.Type != "CERTIFICATE REQUEST" {
		return nil, time.Time{}, errors.New("pki: ungültige CSR")
	}
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("pki: csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, time.Time{}, fmt.Errorf("pki: csr-signatur: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, time.Time{}, err
	}
	notAfter = now.Add(ttl)
	tmpl := &x509.Certificate{
		SerialNumber: serial.Add(serial, big.NewInt(2)),
		Subject:      pkix.Name{CommonName: nodeID, Organization: []string{"Fylgja Node"}},
		NotBefore:    now.Add(-2 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, time.Time{}, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), notAfter, nil
}

// VerifyNode prüft, dass cert von dieser CA stammt, gerade gültig ist und zur Node-ID gehört.
func (c *CA) VerifyNode(cert *x509.Certificate, nodeID string, now time.Time) error {
	if cert == nil {
		return errors.New("pki: kein client-zertifikat")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: c.pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("pki: %w", err)
	}
	if cert.Subject.CommonName != nodeID {
		return errors.New("pki: zertifikat gehört zu einem anderen node")
	}
	return nil
}

// NewCSR erzeugt Schlüssel und CSR (Node-Seite).
func NewCSR(nodeID string) (key ed25519.PrivateKey, csrPEM []byte, err error) {
	_, key, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: nodeID}}, key)
	if err != nil {
		return nil, nil, err
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}
