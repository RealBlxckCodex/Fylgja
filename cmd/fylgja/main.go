// Command fylgja ist die Control Plane (Spec 4.2): `fylgja serve|migrate|admin|version`.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // Zeitzonen auch in minimalen Images

	"github.com/realblxckcodex/fylgja/internal/app"
	"github.com/realblxckcodex/fylgja/internal/auth"
	"github.com/realblxckcodex/fylgja/internal/platform/config"
	"github.com/realblxckcodex/fylgja/internal/platform/logging"
	"github.com/realblxckcodex/fylgja/internal/store"
	"github.com/realblxckcodex/fylgja/web"
)

var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `Fylgja %s – persistente, proaktive KI-Agents

Nutzung:
  fylgja serve   [-config fylgja.yaml] [-role all|api|worker|channels|scheduler|router]
  fylgja migrate [-config fylgja.yaml]
  fylgja keygen                                   neuen FYLGJA_MASTER_KEY erzeugen
  fylgja admin create-user -email E -password P [-role owner] [-workspace ID]
  fylgja version
`, version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", os.Getenv("FYLGJA_CONFIG"), "pfad zur fylgja.yaml")
	role := fs.String("role", "", "prozessrolle")
	switch cmd {
	case "version":
		fmt.Println(version)
		return
	case "keygen":
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		fmt.Println(base64.StdEncoding.EncodeToString(b))
		return
	case "serve", "migrate":
		_ = fs.Parse(args)
	case "admin":
		admin(args)
		return
	default:
		usage()
		os.Exit(2)
	}
	cfg, err := config.Load(*cfgPath)
	if *role != "" {
		cfg.Role = *role
		if err == nil {
			err = cfg.Validate()
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "konfiguration ungültig:\n"+err.Error())
		os.Exit(1)
	}
	log := logging.New(cfg.LogLevel, nil)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cmd == "migrate" {
		if err := store.Migrate(ctx, cfg.DatabaseURL); err != nil {
			log.Error("migration fehlgeschlagen", "err", err)
			os.Exit(1)
		}
		v, _ := store.Status(ctx, cfg.DatabaseURL)
		fmt.Println("schema-version", v)
		return
	}
	a, err := app.New(ctx, cfg, log, web.Handler(), version)
	if err != nil {
		log.Error("start fehlgeschlagen", "err", err)
		os.Exit(1)
	}
	if err := a.Run(ctx); err != nil {
		log.Error("beendet mit fehler", "err", err)
		os.Exit(1)
	}
}

func admin(args []string) {
	if len(args) < 1 || args[0] != "create-user" {
		usage()
		os.Exit(2)
	}
	fs := flag.NewFlagSet("create-user", flag.ExitOnError)
	cfgPath := fs.String("config", os.Getenv("FYLGJA_CONFIG"), "pfad zur fylgja.yaml")
	email := fs.String("email", "", "e-mail")
	pw := fs.String("password", "", "passwort (≥ 12 zeichen)")
	name := fs.String("name", "", "anzeigename")
	role := fs.String("role", "member", "owner|admin|member|viewer|auditor")
	wsFlag := fs.String("workspace", "", "workspace-id (leer = erster workspace)")
	_ = fs.Parse(args[1:])
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx := context.Background()
	if err := store.Migrate(ctx, cfg.DatabaseURL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pool.Close()
	ws := *wsFlag
	if ws == "" {
		if err := pool.QueryRow(ctx, `SELECT id::text FROM workspaces WHERE name <> '_system' ORDER BY created_at LIMIT 1`).Scan(&ws); err != nil {
			fmt.Fprintln(os.Stderr, "kein workspace – zuerst das setup in der web-ui abschließen")
			os.Exit(1)
		}
	}
	svc := &auth.Service{Pool: pool}
	id, err := svc.CreateUser(ctx, mustUUID(ws), *email, *name, *pw, *role)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("nutzer angelegt:", id)
}
