// Package netguard verhindert SSRF: Verbindungen zu privaten, Loopback-, Link-Local-
// und Cloud-Metadata-Adressen werden nach der DNS-Auflösung blockiert (Spec 13.7).
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

var ErrBlocked = errors.New("netguard: ziel ist gesperrt (privates netz/metadata)")

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), // link-local inkl. 169.254.169.254 (Metadata)
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

// Allowed meldet, ob eine IP öffentlich erreichbar sein darf.
func Allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Guard ist ein Dialer mit Prüfung nach der Auflösung (schützt auch vor DNS-Rebinding).
type Guard struct {
	// Extra: zusätzlich gesperrte Netze (z. B. Control-Plane-Netz).
	Extra []netip.Prefix
	// AllowPrivate: Ausnahmen (z. B. SearXNG im internen Netz).
	AllowHosts []string
}

func (g *Guard) control(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !Allowed(ip) {
		return fmt.Errorf("%w: %s", ErrBlocked, ip)
	}
	for _, p := range g.Extra {
		if p.Contains(ip.Unmap()) {
			return fmt.Errorf("%w: %s", ErrBlocked, ip)
		}
	}
	return nil
}

// DialContext wählt mit Prüfung.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, _ := net.SplitHostPort(addr)
	for _, h := range g.AllowHosts {
		if strings.EqualFold(h, host) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
		}
	}
	d := &net.Dialer{Timeout: 10 * time.Second, Control: g.control}
	return d.DialContext(ctx, network, addr)
}

// Client liefert einen HTTP-Client mit Guard (Redirects werden ebenfalls geprüft).
func (g *Guard) Client(timeout time.Duration) *http.Client {
	tr := &http.Transport{DialContext: g.DialContext, Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: timeout,
		MaxIdleConns: 50, IdleConnTimeout: 60 * time.Second}
	return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("zu viele weiterleitungen")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("nur http(s)")
		}
		return nil
	}}
}
