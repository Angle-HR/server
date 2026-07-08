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
)

// Config holds runtime configuration values.
type Config struct {
	ServerPort        string
	DBUrlGlobal       string
	RedisURL          string
	AppEnv            string
	PublicAPIURL      string
	FluvioUIOrigin    string
	JWTSecret         string
	JWTAccessTTL      time.Duration
	JWTRefreshTTL     time.Duration
	AuthDefaultRegion region.Region
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load env file: %w", err)
	}

	cfg := Config{
		ServerPort:     os.Getenv("SERVER_PORT"),
		DBUrlGlobal:    os.Getenv("DB_URL_GLOBAL"),
		RedisURL:       os.Getenv("REDIS_URL"),
		AppEnv:         os.Getenv("APP_ENV"),
		PublicAPIURL:   os.Getenv("PUBLIC_API_URL"),
		FluvioUIOrigin: os.Getenv("FLUVIO_UI_ORIGIN"),
		JWTSecret:      os.Getenv("JWT_SECRET"),
	}

	if cfg.JWTSecret == "" {
		cfg.JWTSecret = "dev-insecure-jwt-secret-change-me"
	}

	accessTTL, err := parsePositiveIntEnv("JWT_ACCESS_TTL", 3600)
	if err != nil {
		return Config{}, err
	}
	cfg.JWTAccessTTL = time.Duration(accessTTL) * time.Second

	refreshTTL, err := parsePositiveIntEnv("JWT_REFRESH_TTL", 604800)
	if err != nil {
		return Config{}, err
	}
	cfg.JWTRefreshTTL = time.Duration(refreshTTL) * time.Second

	defaultRegion := os.Getenv("AUTH_DEFAULT_REGION")
	if defaultRegion == "" {
		defaultRegion = string(region.RegionUK)
	}
	cfg.AuthDefaultRegion = region.Region(defaultRegion)
	if !region.Valid(cfg.AuthDefaultRegion) {
		return Config{}, fmt.Errorf("invalid AUTH_DEFAULT_REGION %q", defaultRegion)
	}

	if cfg.ServerPort == "" {
		cfg.ServerPort = "8080"
	}

	if cfg.AppEnv == "" {
		cfg.AppEnv = "development"
	}

	if cfg.DBUrlGlobal == "" {
		return Config{}, errors.New("DB_URL_GLOBAL is required")
	}

	if cfg.RedisURL == "" {
		return Config{}, errors.New("REDIS_URL is required")
	}

	port, err := strconv.Atoi(cfg.ServerPort)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("invalid SERVER_PORT %q", cfg.ServerPort)
	}

	if cfg.PublicAPIURL == "" {
		cfg.PublicAPIURL = fmt.Sprintf("http://localhost:%s", cfg.ServerPort)
	}

	if err := validatePublicAPIURL(cfg.PublicAPIURL); err != nil {
		return Config{}, err
	}

	return cfg, nil
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
