// Package config loads validated application configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds runtime configuration values.
type Config struct {
	ServerPort       string
	DBUrl            string
	DBUrlGlobal      string
	RedisURL         string
	AppEnv           string
	JWTSecret        string
	GeoLite2Path     string
	RegionBaseDomain string
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load env file: %w", err)
	}

	cfg := Config{
		ServerPort:       os.Getenv("SERVER_PORT"),
		DBUrl:            os.Getenv("DB_URL"),
		DBUrlGlobal:      os.Getenv("DB_URL_GLOBAL"),
		RedisURL:         os.Getenv("REDIS_URL"),
		AppEnv:           os.Getenv("APP_ENV"),
		JWTSecret:        os.Getenv("JWT_SECRET"),
		GeoLite2Path:     os.Getenv("GEOLITE2_COUNTRY_PATH"),
		RegionBaseDomain: os.Getenv("REGION_BASE_DOMAIN"),
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

	if cfg.JWTSecret == "" {
		return Config{}, errors.New("JWT_SECRET is required")
	}

	if cfg.RegionBaseDomain == "" {
		cfg.RegionBaseDomain = "anglehr.com"
	}

	if cfg.RedisURL == "" {
		return Config{}, errors.New("REDIS_URL is required")
	}

	port, err := strconv.Atoi(cfg.ServerPort)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("invalid SERVER_PORT %q", cfg.ServerPort)
	}

	return cfg, nil
}
