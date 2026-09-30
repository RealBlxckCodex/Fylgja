package netguard

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestAllowed(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "169.254.169.254", "::1", "fe80::1", "::ffff:127.0.0.1", "172.20.0.5", "100.100.1.1"} {
		if Allowed(netip.MustParseAddr(s)) {
			t.Errorf("%s erlaubt", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2a01:4f8::1"} {
		if !Allowed(netip.MustParseAddr(s)) {
			t.Errorf("%s gesperrt", s)
		}
	}
}

func TestClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	c := (&Guard{}).Client(2 * time.Second)
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("loopback erreichbar")
	}
}
