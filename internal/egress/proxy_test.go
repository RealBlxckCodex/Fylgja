package egress

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/realblxckcodex/fylgja/internal/platform/netguard"
)

func TestDecide(t *testing.T) {
	al := Rule{Mode: Allowlist, Allow: []string{"github.com", "api.openai.com"}}
	for host, want := range map[string]bool{"github.com": true, "api.github.com": true, "evilgithub.com": false, "github.com.evil.io": false} {
		if ok, _ := Decide(al, host); ok != want {
			t.Errorf("%s: %v", host, ok)
		}
	}
	if ok, _ := Decide(Rule{Mode: Offline}, "x.de"); ok {
		t.Error("offline")
	}
	if ok, _ := Decide(Rule{Mode: Open, Deny: []string{"pastebin.com"}}, "pastebin.com"); ok {
		t.Error("deny")
	}
}

func TestProxyBlocksMetadataAndPrivate(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("intern")) }))
	defer internal.Close()
	var logs []Entry
	p := &Proxy{Policy: &Policy{Default: Rule{Mode: Open}}, Guard: &netguard.Guard{}, OnLog: func(e Entry) { logs = append(logs, e) }}
	srv := httptest.NewServer(p)
	defer srv.Close()
	pu, _ := url.Parse(srv.URL)
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	for _, target := range []string{internal.URL, "http://169.254.169.254/latest/meta-data/"} {
		resp, err := c.Get(target)
		if err == nil {
			if resp.StatusCode == 200 {
				t.Fatalf("%s erreichbar über proxy", target)
			}
			resp.Body.Close()
		}
	}
	if len(logs) < 2 || logs[0].Allowed {
		t.Fatalf("logs %+v", logs)
	}
}
