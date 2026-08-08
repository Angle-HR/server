// Package logger constructs structured application loggers.
package logger

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// New returns a logger configured for appEnv and level.
// level is one of debug, info, warn, error (case-insensitive).
// When level is empty, development defaults to debug and all other envs to info.
// The returned logger is also installed as slog.Default.
func New(appEnv, level string) *slog.Logger {
	lvl, err := ParseLevel(level, appEnv)
	if err != nil {
		lvl = defaultLevel(appEnv)
	}

	opts := &slog.HandlerOptions{
		Level:     lvl,
		AddSource: lvl <= slog.LevelDebug,
	}

	var handler slog.Handler
	if appEnv == "development" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	log := slog.New(handler)
	slog.SetDefault(log)
	return log
}

// ParseLevel maps a level string to slog.Level.
// Empty level uses the appEnv default (debug in development, otherwise info).
func ParseLevel(level, appEnv string) (slog.Level, error) {
	raw := strings.TrimSpace(strings.ToLower(level))
	if raw == "" {
		return defaultLevel(appEnv), nil
	}

	switch raw {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid LOG_LEVEL %q (want debug, info, warn, or error)", level)
	}
}

func defaultLevel(appEnv string) slog.Level {
	if appEnv == "development" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
