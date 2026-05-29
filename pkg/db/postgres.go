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

// Store exposes the shared connection pool.
type Store struct {
	Pool *pgxpool.Pool
}

// NewStore creates and verifies a PostgreSQL pool.
func NewStore(ctx context.Context, dbURL string) (*Store, error) {
	pool, err := NewPool(ctx, dbURL)
	if err != nil {
		return nil, err
	}

	return &Store{Pool: pool}, nil
}

// NewPool creates and verifies a PostgreSQL connection pool.
func NewPool(ctx context.Context, dbURL string) (*pgxpool.Pool, error) {
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
	cfg.ConnConfig.RuntimeParams["search_path"] = "waitlist,public"

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

// New is a compatibility alias for NewPool.
func New(ctx context.Context, dbURL string) (*pgxpool.Pool, error) {
	return NewPool(ctx, dbURL)
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
