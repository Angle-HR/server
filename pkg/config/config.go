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
	ServerPort string
	DBUrl      string
	RedisURL   string
	AppEnv     string
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load env file: %w", err)
	}

	cfg := Config{
		ServerPort: os.Getenv("SERVER_PORT"),
		DBUrl:      os.Getenv("DB_URL"),
		RedisURL:   os.Getenv("REDIS_URL"),
		AppEnv:     os.Getenv("APP_ENV"),
	}

	if cfg.ServerPort == "" {
		cfg.ServerPort = "8080"
	}

	if cfg.AppEnv == "" {
		cfg.AppEnv = "development"
	}

	if cfg.DBUrl == "" {
		return Config{}, errors.New("DB_URL is required")
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
