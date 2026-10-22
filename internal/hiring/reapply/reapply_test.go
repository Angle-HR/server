package reapply_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/reapply"
)

func TestHash(t *testing.T) {
	h, err := reapply.New(strings.Repeat("k", 32))
	if err != nil || !h.Enabled() {
		t.Fatalf("new: %v", err)
	}
	a := h.Hash("org-1", "ada@example.com")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a) {
		t.Fatalf("hash = %q", a)
	}
	if a != h.Hash("org-1", "  Ada@Example.COM ") {
		t.Error("case and spaces must not change the hash")
	}
	if a == h.Hash("org-2", "ada@example.com") {
		t.Error("the same address must hash differently for another company")
	}
	if strings.Contains(a, "ada") {
		t.Error("hash leaks the address")
	}
	other, _ := reapply.New(strings.Repeat("z", 32))
	if a == other.Hash("org-1", "ada@example.com") {
		t.Error("another key must give another hash")
	}
}

func TestKey(t *testing.T) {
	if _, err := reapply.New("short"); err == nil {
		t.Error("a short key must be refused")
	}
	h, err := reapply.New("  ")
	if err != nil || h.Enabled() || h.Hash("o", "a@b.test") != "" {
		t.Error("no key means off")
	}
	var nilHasher *reapply.Hasher
	if nilHasher.Enabled() || nilHasher.Hash("o", "a@b.test") != "" {
		t.Error("a nil hasher is off")
	}
}
