// Package logger configures the process-wide structured logger. All log output
// is emitted as JSON on stdout via log/slog; no fmt.Print* is used anywhere in
// the codebase.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a *slog.Logger that writes JSON records to stdout at the given
// level. In non-production environments source location is included to aid
// debugging; the handler is always JSON so that log aggregation works
// identically across environments.
func New(level, env string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}
	if env != "production" {
		opts.AddSource = true
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

// parseLevel maps a config level string to an slog.Level. Unknown values fall
// back to info.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
