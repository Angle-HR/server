package config

import (
	"os"
	"testing"
)

func TestLoadReadsSecretsFromFiles(t *testing.T) {
	p := t.TempDir() + "/jwt"
	if err := os.WriteFile(p, []byte("file-jwt-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOTP_ENCRYPTION_KEY", "")
	if err := os.Unsetenv("TOTP_ENCRYPTION_KEY"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOTP_ENCRYPTION_KEY_FILE", p)
	t.Setenv("DB_URL_GLOBAL", "postgres://x")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TOTPEncryptionKey != "file-jwt-secret" {
		t.Fatalf("TOTPEncryptionKey = %q", cfg.TOTPEncryptionKey)
	}
}

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

func TestValidateGoogleAddressModeNeedsKey(t *testing.T) {
	t.Parallel()

	cfg := Config{DBUrlGlobal: "postgres://x", RedisURL: "redis://x", ServerPort: "8080", AddressVerifyMode: "google"}
	if err := validate(&cfg); err == nil {
		t.Fatal("expected an error when GOOGLE_MAPS_API_KEY is empty")
	}
	cfg.GoogleMapsAPIKey = "key"
	if err := validate(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// baseEnv sets the variables Load needs and clears every TOTP/JWT source so each case starts clean.
func baseEnv(t *testing.T, appEnv string) {
	t.Helper()
	t.Setenv("DB_URL_GLOBAL", "postgres://x")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("APP_ENV", appEnv)
	for _, k := range []string{
		"TOTP_ENCRYPTION_KEY", "TOTP_ENCRYPTION_KEY_FILE", "TOTP_ENCRYPTION_KEY_PREVIOUS",
		"TOTP_ENCRYPTION_KEY_PREVIOUS_FILE", "JWT_SECRET", "JWT_SECRET_FILE", "LOG_LEVEL",
	} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTOTPKeyDevelopmentDefault(t *testing.T) {
	baseEnv(t, "development")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TOTPEncryptionKey != InsecureDevTOTPKey {
		t.Fatalf("dev key = %q", cfg.TOTPEncryptionKey)
	}
}

func TestTOTPKeyProductionGuard(t *testing.T) {
	good := "a-real-random-key-0123456789"
	cases := []struct {
		name, env, key, jwt string
		wantErr             bool
	}{
		{"prod no key", "production", "", "", true},
		{"prod insecure default", "production", InsecureDevTOTPKey, "", true},
		{"prod insecure via jwt", "production", "", InsecureDevTOTPKey, true},
		{"prod short key", "production", "short", "", true},
		{"prod real key", "production", good, "", false},
		{"prod jwt fallback", "production", "", good, false},
		{"staging no key", "staging", "", "", true},
		{"staging insecure default", "staging", InsecureDevTOTPKey, "", true},
		{"staging real key", "staging", good, "", false},
		{"prod alias no key", "prod", "", "", true},
		{"dev insecure ok", "development", InsecureDevTOTPKey, "", false},
		{"dev none ok", "development", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			baseEnv(t, c.env)
			if c.key != "" {
				t.Setenv("TOTP_ENCRYPTION_KEY", c.key)
			}
			if c.jwt != "" {
				t.Setenv("JWT_SECRET", c.jwt)
			}
			_, err := Load()
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestTOTPPreviousKeys(t *testing.T) {
	baseEnv(t, "production")
	t.Setenv("TOTP_ENCRYPTION_KEY", "a-real-random-key-0123456789")
	t.Setenv("TOTP_ENCRYPTION_KEY_PREVIOUS", " old-one , ,old-two ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.TOTPEncryptionKeyPrevious; len(got) != 2 || got[0] != "old-one" || got[1] != "old-two" {
		t.Fatalf("previous = %q", got)
	}
}

func TestHardenedEnv(t *testing.T) {
	for env, want := range map[string]bool{
		"production": true, "prod": true, "staging": true, "Staging": true,
		"development": false, "": false, "test": false,
	} {
		if got := HardenedEnv(env); got != want {
			t.Errorf("HardenedEnv(%q) = %v", env, got)
		}
	}
}

func TestAdminTOTPRequired(t *testing.T) {
	cases := []struct {
		env, val string
		want     bool
		wantErr  bool
	}{
		{"development", "", false, false},
		{"development", "true", true, false},
		{"development", "false", false, false},
		{"production", "", true, false},
		{"staging", "", true, false},
		{"production", "true", true, false},
		{"production", "false", false, true},
		{"staging", "false", false, true},
		{"development", "maybe", false, true},
	}
	for _, c := range cases {
		t.Run(c.env+"/"+c.val, func(t *testing.T) {
			baseEnv(t, c.env)
			t.Setenv("TOTP_ENCRYPTION_KEY", "a-real-random-key-0123456789")
			t.Setenv("ADMIN_TOTP_REQUIRED", c.val)
			cfg, err := Load()
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err == nil && cfg.AdminTOTPRequired != c.want {
				t.Fatalf("AdminTOTPRequired = %v want %v", cfg.AdminTOTPRequired, c.want)
			}
		})
	}
}
