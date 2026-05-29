package config

import (
	"testing"
)

func TestValidatePublicAPIURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"valid http", "http://localhost:8080", false},
		{"valid https", "https://api.example.com", false},
		{"trailing slash ok", "http://localhost:8080/", false},
		{"missing scheme", "localhost:8080", true},
		{"path not allowed", "http://localhost:8080/api/v1", true},
		{"empty host", "http://", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validatePublicAPIURL(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validatePublicAPIURL(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

func TestNormalizePublicAPIURL(t *testing.T) {
	t.Parallel()

	if got := NormalizePublicAPIURL("http://localhost:8080/"); got != "http://localhost:8080" {
		t.Fatalf("got %q, want http://localhost:8080", got)
	}
}
