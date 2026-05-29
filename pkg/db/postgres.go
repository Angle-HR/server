// Package db provides database connection helpers.
package db

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMaxConns        = int32(25)
	defaultMinConns        = int32(2)
	defaultMaxConnLifetime = time.Hour
	defaultMaxConnIdleTime = 30 * time.Minute
	defaultConnectTimeout  = 5 * time.Second
)

// NewGlobalPool creates a pool for the global registry database (public schema only).
func NewGlobalPool(ctx context.Context, dbURL string) (*pgxpool.Pool, error) {
	return newPool(ctx, dbURL, "public")
}

func newPool(ctx context.Context, dbURL, searchPath string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}

	cfg.MaxConns = envInt32("DB_POOL_MAX_CONNS", defaultMaxConns)
	cfg.MinConns = envInt32("DB_POOL_MIN_CONNS", defaultMinConns)
	cfg.MaxConnLifetime = envDuration("DB_POOL_MAX_CONN_LIFETIME", defaultMaxConnLifetime)
	cfg.MaxConnIdleTime = envDuration("DB_POOL_MAX_CONN_IDLE_TIME", defaultMaxConnIdleTime)
	cfg.ConnConfig.ConnectTimeout = envDuration("DB_CONNECT_TIMEOUT", defaultConnectTimeout)
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = searchPath

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}

func envInt32(key string, fallback int32) int32 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}

	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < 0 {
		return fallback
	}

	return int32(value)
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}

	return value
}
