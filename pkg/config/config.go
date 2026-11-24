// Package config loads validated application configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/logger"
	"github.com/Angle-HR/server/pkg/platform/secrets"
)

// maxPort is the highest valid TCP port.
const maxPort = 65535

// Default session lifetimes in seconds: two hours idle (sliding), twelve hours absolute.
const (
	defaultSessionIdleTTLSeconds     = 7200
	defaultSessionAbsoluteTTLSeconds = 43200
)

// Config holds runtime configuration values.
type Config struct {
	ServerPort  string
	DBUrlGlobal string
	RedisURL    string
	// NotificationsTransport selects how email requests leave the monolith: "fluvio" (default, the in-process
	// queue drained by email-worker) or "events" (outbox + NATS, consumed by the notifications service).
	NotificationsTransport string
	NATSURL                string
	AppEnv                 string
	LogLevel               string
	PublicAPIURL           string
	FluvioUIOrigin         string
	FluvioBasicAuthUser    string
	FluvioBasicAuthPass    string
	CORSAllowedOrigins     []string
	// SessionIdleTTL is the sliding idle timeout of a server-side session (SESSION_IDLE_TTL, seconds).
	SessionIdleTTL time.Duration
	// SessionAbsoluteTTL is the hard lifetime of a session regardless of activity (SESSION_ABSOLUTE_TTL, seconds).
	SessionAbsoluteTTL time.Duration
	// CookieSecure marks session cookies Secure and uses the __Host- prefix (COOKIE_SECURE, default true).
	// Set COOKIE_SECURE=false only for plain-http local development.
	CookieSecure bool
	// AllowedOrigins is the Origin/Referer allow-list for unsafe methods (ALLOWED_ORIGINS, falls back to
	// CORS_ALLOWED_ORIGINS).
	AllowedOrigins         []string
	AuthDefaultRegion      region.Region
	AdminBootstrapEmail    string
	AdminBootstrapPassword string
	AdminBootstrapName     string
	// TOTPEncryptionKey encrypts TOTP secrets at rest (TOTP_ENCRYPTION_KEY). It is the only key new secrets are
	// written with. TOTPEncryptionKeyPrevious (TOTP_ENCRYPTION_KEY_PREVIOUS, comma separated) lists retired keys
	// that can still decrypt existing secrets while the key is being rotated.
	TOTPEncryptionKey string
	// AdminTOTPRequired (ADMIN_TOTP_REQUIRED) forces every admin to enrol an authenticator: a password-only admin
	// session can then do nothing but enrol. Unset it defaults to true in production-like environments (production,
	// prod, staging) and false elsewhere; setting it false in a production-like environment is refused.
	AdminTOTPRequired         bool
	TOTPEncryptionKeyPrevious []string
	AddressVerifyMode         string
	AddressSearchMode         string
	GoogleMapsAPIKey          string
	CompaniesHouseAPIKey      string
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load env file: %w", err)
	}

	// Secrets come from NAME or the file named by NAME_FILE (pkg/platform/secrets).
	var sec secrets.Reader

	cfg := Config{
		ServerPort:                os.Getenv("SERVER_PORT"),
		DBUrlGlobal:               sec.Get("DB_URL_GLOBAL"),
		RedisURL:                  sec.Get("REDIS_URL"),
		CompaniesHouseAPIKey:      strings.TrimSpace(sec.Get("COMPANIES_HOUSE_API_KEY")),
		AppEnv:                    os.Getenv("APP_ENV"),
		LogLevel:                  strings.TrimSpace(os.Getenv("LOG_LEVEL")),
		PublicAPIURL:              os.Getenv("PUBLIC_API_URL"),
		FluvioUIOrigin:            os.Getenv("FLUVIO_UI_ORIGIN"),
		FluvioBasicAuthUser:       os.Getenv("FLUVIO_BASIC_AUTH_USER"),
		NotificationsTransport:    strings.ToLower(strings.TrimSpace(os.Getenv("NOTIFICATIONS_TRANSPORT"))),
		NATSURL:                   os.Getenv("NATS_URL"),
		FluvioBasicAuthPass:       sec.Get("FLUVIO_BASIC_AUTH_PASSWORD"),
		AdminBootstrapEmail:       strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_EMAIL")),
		AdminBootstrapPassword:    sec.Get("ADMIN_BOOTSTRAP_PASSWORD"),
		AdminBootstrapName:        strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_NAME")),
		TOTPEncryptionKey:         strings.TrimSpace(sec.Get("TOTP_ENCRYPTION_KEY")),
		TOTPEncryptionKeyPrevious: splitList(sec.Get("TOTP_ENCRYPTION_KEY_PREVIOUS")),
		AddressVerifyMode:         strings.ToLower(strings.TrimSpace(os.Getenv("ADDRESS_VERIFY_MODE"))),
		AddressSearchMode:         strings.ToLower(strings.TrimSpace(os.Getenv("ADDRESS_SEARCH_MODE"))),
		GoogleMapsAPIKey:          strings.TrimSpace(sec.Get("GOOGLE_MAPS_API_KEY")),
	}

	if err := sec.Err(); err != nil {
		return Config{}, err
	}

	applyDefaults(&cfg)

	if err := resolveTOTPKey(&cfg, sec.Get("JWT_SECRET")); err != nil {
		return Config{}, err
	}

	if err := loadSessionSettings(&cfg); err != nil {
		return Config{}, err
	}

	if err := loadAdminTOTPRequired(&cfg); err != nil {
		return Config{}, err
	}

	defaultRegion := os.Getenv("AUTH_DEFAULT_REGION")
	if defaultRegion == "" {
		defaultRegion = string(region.RegionUK)
	}
	cfg.AuthDefaultRegion = region.Region(defaultRegion)
	if !region.Valid(cfg.AuthDefaultRegion) {
		return Config{}, fmt.Errorf("invalid AUTH_DEFAULT_REGION %q", defaultRegion)
	}

	if err := validate(&cfg); err != nil {
		return Config{}, err
	}

	origins, err := parseCORSAllowedOrigins(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if err != nil {
		return Config{}, err
	}
	cfg.CORSAllowedOrigins = origins

	cfg.AllowedOrigins = origins
	if raw := strings.TrimSpace(os.Getenv("ALLOWED_ORIGINS")); raw != "" {
		allowed, allowedErr := parseCORSAllowedOrigins(raw)
		if allowedErr != nil {
			return Config{}, allowedErr
		}
		cfg.AllowedOrigins = allowed
	}

	return cfg, nil
}

// applyDefaults fills in development-friendly defaults for unset values.
func applyDefaults(cfg *Config) {
	if cfg.NotificationsTransport == "" {
		cfg.NotificationsTransport = "fluvio"
	}
	if cfg.ServerPort == "" {
		cfg.ServerPort = "8080"
	}
	if cfg.AppEnv == "" {
		cfg.AppEnv = "development"
	}

	isProd := cfg.AppEnv == "production" || cfg.AppEnv == "prod"
	if cfg.AddressVerifyMode == "" && !isProd {
		cfg.AddressVerifyMode = "passthrough"
	}
	if cfg.AddressSearchMode == "" && !isProd {
		cfg.AddressSearchMode = "passthrough"
	}
}

// InsecureDevTOTPKey is the development-only fallback key. Production-like environments refuse to start with it.
const InsecureDevTOTPKey = "dev-insecure-jwt-secret-change-me"

// minProdTOTPKeyLen is the shortest TOTP key accepted in production-like environments.
const minProdTOTPKeyLen = 16

// HardenedEnv reports whether appEnv is production-like (production, prod or staging): environments that must not run
// with development defaults.
func HardenedEnv(appEnv string) bool {
	switch strings.ToLower(strings.TrimSpace(appEnv)) {
	case "production", "prod", "staging":
		return true
	default:
		return false
	}
}

// splitList splits a comma separated list, dropping blanks.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// resolveTOTPKey picks the TOTP encryption key. TOTP_ENCRYPTION_KEY wins. JWT_SECRET (the key TOTP secrets used to
// be encrypted with) is still accepted as a fallback so existing enrolments decrypt. Development falls back to a
// fixed insecure key; production-like environments refuse to start without a real key, and the fallbacks are
// rejected there when they are the insecure default.
func resolveTOTPKey(cfg *Config, jwtSecret string) error {
	hardened := HardenedEnv(cfg.AppEnv)
	jwtSecret = strings.TrimSpace(jwtSecret)

	if cfg.TOTPEncryptionKey == "" && jwtSecret != "" {
		cfg.TOTPEncryptionKey = jwtSecret
		if hardened {
			slog.Warn("TOTP_ENCRYPTION_KEY is unset; falling back to JWT_SECRET. " +
				"Set TOTP_ENCRYPTION_KEY and move the old value to TOTP_ENCRYPTION_KEY_PREVIOUS (see docs/microservices/hardening.md)")
		}
	}
	if cfg.TOTPEncryptionKey == "" && !hardened {
		cfg.TOTPEncryptionKey = InsecureDevTOTPKey
	}
	if !hardened {
		return nil
	}
	switch {
	case cfg.TOTPEncryptionKey == "":
		return fmt.Errorf("TOTP_ENCRYPTION_KEY is required when APP_ENV=%s", cfg.AppEnv)
	case cfg.TOTPEncryptionKey == InsecureDevTOTPKey:
		return fmt.Errorf("TOTP_ENCRYPTION_KEY is the insecure development default; set a real key when APP_ENV=%s", cfg.AppEnv)
	case len(cfg.TOTPEncryptionKey) < minProdTOTPKeyLen:
		return fmt.Errorf("TOTP_ENCRYPTION_KEY must be at least %d characters when APP_ENV=%s", minProdTOTPKeyLen, cfg.AppEnv)
	}
	return nil
}

// loadSessionSettings reads the session lifetimes and cookie settings from the environment.
func loadSessionSettings(cfg *Config) error {
	idle, err := parsePositiveIntEnv("SESSION_IDLE_TTL", defaultSessionIdleTTLSeconds)
	if err != nil {
		return err
	}
	cfg.SessionIdleTTL = time.Duration(idle) * time.Second

	abs, err := parsePositiveIntEnv("SESSION_ABSOLUTE_TTL", defaultSessionAbsoluteTTLSeconds)
	if err != nil {
		return err
	}
	cfg.SessionAbsoluteTTL = time.Duration(abs) * time.Second
	if cfg.SessionAbsoluteTTL < cfg.SessionIdleTTL {
		return errors.New("SESSION_ABSOLUTE_TTL must be >= SESSION_IDLE_TTL")
	}

	cfg.CookieSecure = true
	if raw := strings.TrimSpace(os.Getenv("COOKIE_SECURE")); raw != "" {
		secure, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return fmt.Errorf("invalid COOKIE_SECURE %q", raw)
		}
		cfg.CookieSecure = secure
	}
	return nil
}

// loadAdminTOTPRequired reads ADMIN_TOTP_REQUIRED. Unset: true in production-like environments, false elsewhere.
// An explicit false in a production-like environment is refused.
func loadAdminTOTPRequired(cfg *Config) error {
	hardened := HardenedEnv(cfg.AppEnv)
	cfg.AdminTOTPRequired = hardened
	raw := strings.TrimSpace(os.Getenv("ADMIN_TOTP_REQUIRED"))
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("invalid ADMIN_TOTP_REQUIRED %q (want true or false)", raw)
	}
	if hardened && !v {
		return fmt.Errorf("ADMIN_TOTP_REQUIRED must be true when APP_ENV=%s", cfg.AppEnv)
	}
	cfg.AdminTOTPRequired = v
	return nil
}

// validate checks required values and fills in the public API URL.
func validate(cfg *Config) error {
	if _, err := logger.ParseLevel(cfg.LogLevel, cfg.AppEnv); err != nil {
		return err
	}
	if cfg.DBUrlGlobal == "" {
		return errors.New("DB_URL_GLOBAL is required")
	}
	if cfg.RedisURL == "" {
		return errors.New("REDIS_URL is required")
	}

	port, err := strconv.Atoi(cfg.ServerPort)
	if err != nil || port < 1 || port > maxPort {
		return fmt.Errorf("invalid SERVER_PORT %q", cfg.ServerPort)
	}

	if (cfg.AddressVerifyMode == "google" || cfg.AddressSearchMode == "google") && cfg.GoogleMapsAPIKey == "" {
		return errors.New("GOOGLE_MAPS_API_KEY is required when an address mode is google")
	}

	if cfg.PublicAPIURL == "" {
		cfg.PublicAPIURL = fmt.Sprintf("http://localhost:%s", cfg.ServerPort)
	}
	return validatePublicAPIURL(cfg.PublicAPIURL)
}

func parseCORSAllowedOrigins(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var origins []string
	for _, part := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}

		if err := validatePublicAPIURL(origin); err != nil {
			return nil, fmt.Errorf("invalid CORS_ALLOWED_ORIGINS entry %q: %w", origin, err)
		}

		origins = append(origins, strings.TrimRight(origin, "/"))
	}

	return origins, nil
}

func parsePositiveIntEnv(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("invalid %s %q", key, raw)
	}

	return value, nil
}

func validatePublicAPIURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid PUBLIC_API_URL %q: %w", raw, err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid PUBLIC_API_URL %q: scheme must be http or https", raw)
	}

	if u.Host == "" {
		return fmt.Errorf("invalid PUBLIC_API_URL %q: host is required", raw)
	}

	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("invalid PUBLIC_API_URL %q: must be origin only (no path)", raw)
	}

	return nil
}

// NormalizePublicAPIURL trims a trailing slash from the configured origin.
func NormalizePublicAPIURL(raw string) string {
	return strings.TrimRight(raw, "/")
}
