// Command egressd ist der Egress-Forward-Proxy pro Sandbox-Host (Spec 13.7).
//
// Konfiguration (JSON, EGRESSD_POLICY oder -policy Datei):
//
//	{"default":{"mode":"open","deny":["pastebin.com"]},
//	 "sources":{"172.30.0.12":{"mode":"allowlist","allow":["github.com"]}}}
//
// Die Datei wird bei SIGHUP neu geladen.
package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/realblxckcodex/fylgja/internal/egress"
	"github.com/realblxckcodex/fylgja/internal/platform/logging"
	"github.com/realblxckcodex/fylgja/internal/platform/netguard"
)

func load(path string, p *egress.Policy, log *slog.Logger) {
	raw := []byte(os.Getenv("EGRESSD_POLICY"))
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			log.Error("policy lesen", "err", err)
			return
		}
		raw = b
	}
	var cfg struct {
		Default egress.Rule            `json:"default"`
		Sources map[string]egress.Rule `json:"sources"`
	}
	cfg.Default.Mode = egress.Open
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			log.Error("policy ungültig", "err", err)
			return
		}
	}
	p.Set(cfg.Sources, cfg.Default)
	log.Info("egress-policy geladen", "quellen", len(cfg.Sources), "default", cfg.Default.Mode)
}

func main() {
	listen := flag.String("listen", ":3128", "adresse")
	policyPath := flag.String("policy", os.Getenv("EGRESSD_POLICY_FILE"), "policy-datei (json)")
	flag.Parse()
	log := logging.New(os.Getenv("EGRESSD_LOG_LEVEL"), nil)
	pol := &egress.Policy{}
	load(*policyPath, pol, log)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			load(*policyPath, pol, log)
		}
	}()
	p := &egress.Proxy{Policy: pol, Guard: &netguard.Guard{}, Log: log}
	srv := &http.Server{Addr: *listen, Handler: p, ReadHeaderTimeout: 15 * time.Second}
	log.Info("egressd lauscht", "addr", *listen)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("egressd", "err", err)
		os.Exit(1)
	}
}
