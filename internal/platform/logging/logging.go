// Package logging konfiguriert slog (JSON) für alle Prozesse.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New erzeugt einen JSON-Logger. Werte laufen vor dem Schreiben durch redact (falls gesetzt).
func New(level string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l}))
}
