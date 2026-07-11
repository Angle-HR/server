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

func TestParseCORSAllowedOrigins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{
			name: "empty disables cors",
			raw:  "",
			want: nil,
		},
		{
			name: "single origin",
			raw:  "http://localhost:3000",
			want: []string{"http://localhost:3000"},
		},
		{
			name: "comma separated trims spaces",
			raw:  "http://localhost:3000, https://app.example.com ",
			want: []string{"http://localhost:3000", "https://app.example.com"},
		},
		{
			name:    "invalid origin",
			raw:     "not-a-url",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseCORSAllowedOrigins(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseCORSAllowedOrigins(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}

			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}

			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
