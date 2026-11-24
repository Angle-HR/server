package bff

import (
	"net/netip"
	"strings"
	"testing"
)

func TestConfigWarnings(t *testing.T) {
	t.Parallel()

	if w := (Config{AppEnv: "development"}).Warnings(); len(w) != 0 {
		t.Fatalf("development must not warn: %v", w)
	}
	w := (Config{AppEnv: "production"}).Warnings()
	if len(w) != 3 || !strings.Contains(w[0], "TRUSTED_PROXY_CIDRS") {
		t.Fatalf("production defaults: %v", w)
	}
	ok := Config{
		AppEnv: "staging", RequireOrigin: true, AllowedOrigins: []string{"https://app.example.com"},
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}
	if w := ok.Warnings(); len(w) != 0 {
		t.Fatalf("fully configured staging warned: %v", w)
	}
}
