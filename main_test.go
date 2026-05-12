package main

import (
	"errors"
	"testing"
)

func TestRunWith(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fn   func() error
		want int
	}{
		{"success", func() error { return nil }, 0},
		{"error", func() error { return errors.New("fail") }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := runWith(tt.fn); got != tt.want {
				t.Fatalf("runWith: got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunIntegration(t *testing.T) {
	t.Parallel()
	if got := run(); got != 0 {
		t.Fatalf("run: got exit code %d, want 0", got)
	}
}
