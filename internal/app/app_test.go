package app

import "testing"

func TestRun(t *testing.T) {
	t.Parallel()
	if err := Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
