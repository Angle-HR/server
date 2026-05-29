// Package logger constructs structured application loggers.
package logger

import (
	"log/slog"
	"os"
)

// New returns a logger configured for appEnv.
func New(appEnv string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}

	if appEnv == "development" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
