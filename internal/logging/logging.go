// Package logging wires a process-wide slog.Logger based on env config.
//
//   - SHINYHUB_LOG_FORMAT = "text" (default when stdout is a TTY) or "json".
//   - SHINYHUB_LOG_LEVEL = "debug" | "info" | "warn" | "error" (default info).
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New builds a logger for the current process. Call once from main.
func New() *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(os.Getenv("SHINYHUB_LOG_LEVEL")) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	format := strings.ToLower(os.Getenv("SHINYHUB_LOG_FORMAT"))
	if format == "" {
		if isTTY(os.Stdout) {
			format = "text"
		} else {
			format = "json"
		}
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// isTTY returns true when w is a character device (best-effort).
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
