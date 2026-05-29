// Package config loads validated application configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds runtime configuration values.
type Config struct {
	ServerPort   string
	DBUrlGlobal  string
	AppEnv       string
	PublicAPIURL string
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load env file: %w", err)
	}

	cfg := Config{
		ServerPort:   os.Getenv("SERVER_PORT"),
		DBUrlGlobal:  os.Getenv("DB_URL_GLOBAL"),
		AppEnv:       os.Getenv("APP_ENV"),
		PublicAPIURL: os.Getenv("PUBLIC_API_URL"),
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
