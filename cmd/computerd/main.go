// Command computerd läuft IN der Sandbox einer Fylgja und stellt Shell, Dateien und
// Browser (Playwright-MCP) als authentifizierte API bereit (Spec 13.4).
package main

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/realblxckcodex/fylgja/internal/computer"
	"github.com/realblxckcodex/fylgja/internal/platform/logging"
)

func main() {
	log := logging.New(os.Getenv("COMPUTERD_LOG_LEVEL"), nil)
	token := os.Getenv("COMPUTERD_TOKEN")
	if len(token) < 32 {
		log.Error("COMPUTERD_TOKEN fehlt oder ist zu kurz")
		os.Exit(1)
	}
	root := os.Getenv("COMPUTERD_ROOT")
	if root == "" {
		root = "/home/dot"
	}
	_ = os.MkdirAll(root+"/workspace", 0o755)
	var browser []string
	if b := os.Getenv("COMPUTERD_BROWSER_CMD"); b != "" {
		browser = strings.Fields(b)
	}
	s := &computer.Server{Token: token, Root: root, Browser: browser, Log: log, Display: os.Getenv("DISPLAY")}
	// Token nicht an Kindprozesse vererben.
	_ = os.Unsetenv("COMPUTERD_TOKEN")
	addr := os.Getenv("COMPUTERD_LISTEN")
	if addr == "" {
		addr = ":7070"
	}
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("computerd lauscht", "addr", addr, "root", root)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("computerd", "err", err)
		os.Exit(1)
	}
}
