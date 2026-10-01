package pki

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func parse(t *testing.T, p []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(p)
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCADeterministic(t *testing.T) {
	a, _ := NewCA([]byte("0123456789abcdef0123456789abcdef"))
	b, _ := NewCA([]byte("0123456789abcdef0123456789abcdef"))
	c, _ := NewCA([]byte("another-master-key-another-master"))
	if string(a.CertPEM()) != string(b.CertPEM()) {
		t.Fatal("CA nicht reproduzierbar")
	}
	if string(a.CertPEM()) == string(c.CertPEM()) {
		t.Fatal("CA hängt nicht vom Master-Key ab")
	}
	if _, err := NewCA([]byte("kurz")); err == nil {
		t.Fatal("kurzer key akzeptiert")
	}
}

func TestSignAndVerify(t *testing.T) {
	ca, _ := NewCA([]byte("0123456789abcdef0123456789abcdef"))
	_, csr, err := NewCSR("evil-name-in-csr")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	pemCert, na, err := ca.SignCSR(csr, "node-1", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	cert := parse(t, pemCert)
	if cert.Subject.CommonName != "node-1" {
		t.Fatalf("CN aus CSR übernommen: %s", cert.Subject.CommonName)
	}
	if !na.Equal(now.Add(time.Hour)) {
		t.Fatal("laufzeit")
	}
	if err := ca.VerifyNode(cert, "node-1", now); err != nil {
		t.Fatal(err)
	}
	if err := ca.VerifyNode(cert, "node-2", now); err == nil {
		t.Fatal("fremde node-id akzeptiert")
	}
	if err := ca.VerifyNode(cert, "node-1", now.Add(2*time.Hour)); err == nil {
		t.Fatal("abgelaufenes zertifikat akzeptiert")
	}
	other, _ := NewCA([]byte("another-master-key-another-master"))
	if err := other.VerifyNode(cert, "node-1", now); err == nil {
		t.Fatal("zertifikat fremder CA akzeptiert")
	}
	// Zertifikat darf nicht zum Signieren taugen / keine CA sein.
	if cert.IsCA {
		t.Fatal("leaf ist CA")
	}
}

func TestBadCSR(t *testing.T) {
	ca, _ := NewCA([]byte("0123456789abcdef0123456789abcdef"))
	if _, _, err := ca.SignCSR([]byte("müll"), "n", time.Hour, time.Now()); err == nil {
		t.Fatal("müll akzeptiert")
	}
	if _, _, err := ca.SignCSR(nil, "", time.Hour, time.Now()); err == nil {
		t.Fatal("leere node-id akzeptiert")
	}
}
