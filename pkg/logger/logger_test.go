package logger

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		level   string
		appEnv  string
		want    slog.Level
		wantErr bool
	}{
		{name: "empty development defaults debug", level: "", appEnv: "development", want: slog.LevelDebug},
		{name: "empty production defaults info", level: "", appEnv: "production", want: slog.LevelInfo},
		{name: "debug", level: "debug", appEnv: "production", want: slog.LevelDebug},
		{name: "info case insensitive", level: "INFO", appEnv: "development", want: slog.LevelInfo},
		{name: "warn", level: "warn", appEnv: "development", want: slog.LevelWarn},
		{name: "warning alias", level: "warning", appEnv: "development", want: slog.LevelWarn},
		{name: "error", level: "error", appEnv: "development", want: slog.LevelError},
		{name: "invalid", level: "trace", appEnv: "development", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseLevel(tc.level, tc.appEnv)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseLevel(%q, %q) err = %v, wantErr %v", tc.level, tc.appEnv, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
