// Command fylgja-link verbindet ein Nutzergerät (Laptop) mit einer Fylgja (Spec 13.9).
// Nur ausgehend (WSS). Scopes aus ~/.config/fylgja-link.json. Kill-Switch: Strg+C, SIGUSR1
// oder Datei ~/.config/fylgja-link.KILL – sowie "Trennen" in der Web-UI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/realblxckcodex/fylgja/internal/fleet/tunnel"
	"github.com/realblxckcodex/fylgja/internal/link"
	"github.com/realblxckcodex/fylgja/internal/platform/logging"
)

func main() {
	home, _ := os.UserHomeDir()
	cfgPath := flag.String("scopes", filepath.Join(home, ".config", "fylgja-link.json"), "scopes-datei")
	server := flag.String("server", os.Getenv("FYLGJA_LINK_URL"), "wss://fylgja.example.org/api/v1/link/tunnel")
	id := flag.String("id", os.Getenv("FYLGJA_LINK_ID"), "link-id")
	token := flag.String("token", os.Getenv("FYLGJA_LINK_TOKEN"), "link-token")
	flag.Parse()
	log := logging.New("info", nil)
	var scopes link.Scopes
	if b, err := os.ReadFile(*cfgPath); err == nil {
		if err := json.Unmarshal(b, &scopes); err != nil {
			fmt.Fprintln(os.Stderr, "scopes ungültig:", err)
			os.Exit(1)
		}
	} else {
		fmt.Fprintf(os.Stderr, "keine scopes (%s) – der Link erlaubt nichts. Beispiel:\n{\"read\":[\"%s/Documents\"],\"write\":[],\"exec_allow\":[\"git status\"]}\n", *cfgPath, home)
	}
	if *server == "" || *id == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "server, id und token nötig (siehe Web-UI → Fylgja → Laptop-Link)")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGUSR1)
	defer stop()
	killFile := filepath.Join(home, ".config", "fylgja-link.KILL")
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(killFile); err == nil {
				log.Warn("kill-switch-datei gefunden – trenne")
				stop()
				return
			}
			time.Sleep(time.Second)
		}
	}()
	local := &link.Local{Scopes: scopes, Log: func(a, d string) { log.Info("aktion", "art", a, "detail", d) }}
	addr, err := local.Serve(ctx)
	if err != nil {
		log.Error("lokaler server", "err", err)
		os.Exit(1)
	}
	agent := &tunnel.Agent{URL: *server, NodeID: *id, Token: *token, Upstream: addr, Log: log, Interval: 15 * time.Second,
		Register: func() any { return map[string]any{"scopes": scopes, "hostname": hostname()} }}
	log.Info("fylgja-link aktiv", "lesen", scopes.Read, "schreiben", scopes.Write, "kommandos", scopes.ExecAllow)
	if err := agent.Run(ctx); err != nil && ctx.Err() == nil {
		log.Error("link", "err", err)
		os.Exit(1)
	}
}

func hostname() string { h, _ := os.Hostname(); return h }
