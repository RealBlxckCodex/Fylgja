package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/realblxckcodex/fylgja/internal/fleet/pki"
)

// EnrollResponse ist die Antwort des Enrollment-Endpunkts.
type EnrollResponse struct {
	Cert      string    `json:"cert"`
	CA        string    `json:"ca"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Enroller holt und erneuert das kurzlebige Client-Zertifikat eines Nodes.
// Der private Schlüssel entsteht lokal und verlässt den Node nie.
type Enroller struct {
	URL    string // https://control/api/v1/node/enroll
	NodeID string
	Token  string
	Base   *tls.Config // Server-Vertrauen (z. B. private CA), optional
	Log    *slog.Logger

	mu       sync.RWMutex
	cert     *tls.Certificate
	notAfter time.Time
}

func (e *Enroller) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e *Enroller) plainClient() *http.Client {
	tr := &http.Transport{}
	if e.Base != nil {
		tr.TLSClientConfig = e.Base.Clone()
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}

// Enroll fordert ein neues Zertifikat an.
func (e *Enroller) Enroll(ctx context.Context) error {
	key, csr, err := pki.NewCSR(e.NodeID)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, bytes.NewReader(csr))
	if err != nil {
		return err
	}
	req.Header.Set("X-Fylgja-Node", e.NodeID)
	req.Header.Set("Authorization", "Bearer "+e.Token)
	req.Header.Set("Content-Type", "application/x-pem-file")
	resp, err := e.plainClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("enroll: status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	var er EnrollResponse
	if err := json.Unmarshal(body, &er); err != nil {
		return err
	}
	blk, _ := pem.Decode([]byte(er.Cert))
	if blk == nil {
		return errors.New("enroll: zertifikat fehlt")
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.cert = &tls.Certificate{Certificate: [][]byte{blk.Bytes}, PrivateKey: key, Leaf: leaf}
	e.notAfter = leaf.NotAfter
	e.mu.Unlock()
	e.log().Info("node-zertifikat erneuert", "gültig_bis", leaf.NotAfter.Format(time.RFC3339))
	return nil
}

// Run erneuert das Zertifikat nach zwei Dritteln der Laufzeit; bei Fehlern alle 30 s neu.
func (e *Enroller) Run(ctx context.Context) {
	skip := e.Current() != nil // bereits per Enroll() geholt
	for ctx.Err() == nil {
		wait := 30 * time.Second
		var err error
		if !skip {
			err = e.Enroll(ctx)
		}
		skip = false
		if err != nil {
			e.log().Warn("enrollment fehlgeschlagen", "err", err)
		} else {
			e.mu.RLock()
			na := e.notAfter
			e.mu.RUnlock()
			wait = max(time.Until(na)*2/3, 10*time.Second)
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// Current liefert das aktuelle Zertifikat (nil, solange noch keins vorliegt).
func (e *Enroller) Current() *tls.Certificate {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cert
}

// HTTPClient liefert einen Client, der bei jedem Handshake das aktuelle Zertifikat vorzeigt.
func (e *Enroller) HTTPClient() *http.Client {
	var cfg *tls.Config
	if e.Base != nil {
		cfg = e.Base.Clone()
	} else {
		cfg = &tls.Config{MinVersion: tls.VersionTLS13}
	}
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		if c := e.Current(); c != nil {
			return c, nil
		}
		return &tls.Certificate{}, nil
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: false}}
}
