// Package config loads validated application configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/logger"
)

// Default JWT lifetimes in seconds: one hour access, seven days refresh.
// maxPort is the highest valid TCP port.
const maxPort = 65535

const (
	defaultAccessTTLSeconds  = 3600
	defaultRefreshTTLSeconds = 604800
)

// Config holds runtime configuration values.
type Config struct {
	ServerPort             string
	DBUrlGlobal            string
	RedisURL               string
	AppEnv                 string
	LogLevel               string
	PublicAPIURL           string
	FluvioUIOrigin         string
	CORSAllowedOrigins     []string
	JWTSecret              string
	JWTAccessTTL           time.Duration
	JWTRefreshTTL          time.Duration
	AuthDefaultRegion      region.Region
	AdminBootstrapEmail    string
	AdminBootstrapPassword string
	AdminBootstrapName     string
	TOTPEncryptionKey      string
	AddressVerifyMode      string
	AddressSearchMode      string
	CompaniesHouseAPIKey   string
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load env file: %w", err)
	}

	cfg := Config{
		ServerPort:             os.Getenv("SERVER_PORT"),
		DBUrlGlobal:            os.Getenv("DB_URL_GLOBAL"),
		RedisURL:               os.Getenv("REDIS_URL"),
		CompaniesHouseAPIKey:   strings.TrimSpace(os.Getenv("COMPANIES_HOUSE_API_KEY")),
		AppEnv:                 os.Getenv("APP_ENV"),
		LogLevel:               strings.TrimSpace(os.Getenv("LOG_LEVEL")),
		PublicAPIURL:           os.Getenv("PUBLIC_API_URL"),
		FluvioUIOrigin:         os.Getenv("FLUVIO_UI_ORIGIN"),
		JWTSecret:              os.Getenv("JWT_SECRET"),
		AdminBootstrapEmail:    strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_EMAIL")),
		AdminBootstrapPassword: os.Getenv("ADMIN_BOOTSTRAP_PASSWORD"),
		AdminBootstrapName:     strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_NAME")),
		TOTPEncryptionKey:      strings.TrimSpace(os.Getenv("TOTP_ENCRYPTION_KEY")),
		AddressVerifyMode:      strings.ToLower(strings.TrimSpace(os.Getenv("ADDRESS_VERIFY_MODE"))),
		AddressSearchMode:      strings.ToLower(strings.TrimSpace(os.Getenv("ADDRESS_SEARCH_MODE"))),
	}

	applyDefaults(&cfg)

	if err := loadJWTTTLs(&cfg); err != nil {
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

	return cfg, nil
}

// applyDefaults fills in development-friendly defaults for unset values.
func applyDefaults(cfg *Config) {
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = "dev-insecure-jwt-secret-change-me"
	}
	if cfg.TOTPEncryptionKey == "" {
		cfg.TOTPEncryptionKey = cfg.JWTSecret
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

// loadJWTTTLs reads the access and refresh token lifetimes from the environment.
func loadJWTTTLs(cfg *Config) error {
	accessTTL, err := parsePositiveIntEnv("JWT_ACCESS_TTL", defaultAccessTTLSeconds)
	if err != nil {
		return err
	}
	cfg.JWTAccessTTL = time.Duration(accessTTL) * time.Second

	refreshTTL, err := parsePositiveIntEnv("JWT_REFRESH_TTL", defaultRefreshTTLSeconds)
	if err != nil {
		return err
	}
	cfg.JWTRefreshTTL = time.Duration(refreshTTL) * time.Second
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
