package dbrouter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Angle-HR/server/internal/region"
)

var (
	// ErrUnknownRegion indicates the region is not configured on this router.
	ErrUnknownRegion = errors.New("dbrouter: unknown region")
	// ErrPoolNil indicates a pool entry exists but is nil.
	ErrPoolNil = errors.New("dbrouter: pool is nil for region")
)

const regionalSearchPath = "waitlist,public"

// DBRouter holds one PostgreSQL pool and one MinIO client per region.
type DBRouter struct {
	pools   map[region.Region]PgxPool
	minio   map[region.Region]*minio.Client
	buckets map[region.Region]string
}

// NewWithPools returns a DBRouter backed by the given pools. It is intended for tests.
func NewWithPools(pools map[region.Region]PgxPool) *DBRouter {
	if pools == nil {
		pools = map[region.Region]PgxPool{}
	}

	return &DBRouter{
		pools:   pools,
		minio:   map[region.Region]*minio.Client{},
		buckets: map[region.Region]string{},
	}
}

// New initializes all regional pools and MinIO clients. Startup fails if any
// region is missing, duplicated, or unreachable.
func New(ctx context.Context, configs []RegionConfig) (*DBRouter, error) {
	byRegion := make(map[region.Region]RegionConfig, len(configs))
	for _, cfg := range configs {
		if !region.Valid(cfg.Region) {
			return nil, fmt.Errorf("dbrouter: invalid region %q", cfg.Region)
		}
		if err := validateRegionConfig(cfg); err != nil {
			return nil, err
		}
		if _, exists := byRegion[cfg.Region]; exists {
			return nil, fmt.Errorf("dbrouter: duplicate config for region %q", cfg.Region)
		}
		byRegion[cfg.Region] = cfg
	}

	for _, reg := range allRegions() {
		if _, ok := byRegion[reg]; !ok {
			return nil, fmt.Errorf("dbrouter: missing config for region %q", reg)
		}
	}

	router := &DBRouter{
		pools:   make(map[region.Region]PgxPool, len(allRegions())),
		minio:   make(map[region.Region]*minio.Client, len(allRegions())),
		buckets: make(map[region.Region]string, len(allRegions())),
	}

	for _, reg := range allRegions() {
		cfg := byRegion[reg]

		slog.Info("dbrouter connecting", "region", reg, "component", "postgres")
		pool, err := newPool(ctx, cfg.PostgresDSN)
		if err != nil {
			router.Close()
			return nil, fmt.Errorf("dbrouter: postgres %s: %w", reg, err)
		}
		router.pools[reg] = pool

		slog.Info("dbrouter connecting", "region", reg, "component", "minio")
		client, err := newMinIOClient(cfg)
		if err != nil {
			router.Close()
			return nil, fmt.Errorf("dbrouter: minio %s: %w", reg, err)
		}

		exists, err := client.BucketExists(ctx, cfg.MinIOBucket)
		if err != nil {
			router.Close()
			return nil, fmt.Errorf("dbrouter: minio bucket check %s: %w", reg, err)
		}
		if !exists {
			router.Close()
			return nil, fmt.Errorf("dbrouter: minio bucket %q does not exist for region %s", cfg.MinIOBucket, reg)
		}

		router.minio[reg] = client
		router.buckets[reg] = cfg.MinIOBucket
	}

	return router, nil
}

func validateRegionConfig(cfg RegionConfig) error {
	if cfg.PostgresDSN == "" {
		return fmt.Errorf("dbrouter: postgres DSN is required for region %q", cfg.Region)
	}
	if cfg.MinIOEndpoint == "" {
		return fmt.Errorf("dbrouter: minio endpoint is required for region %q", cfg.Region)
	}
	if cfg.MinIOAccessKey == "" {
		return fmt.Errorf("dbrouter: minio access key is required for region %q", cfg.Region)
	}
	if cfg.MinIOSecretKey == "" {
		return fmt.Errorf("dbrouter: minio secret key is required for region %q", cfg.Region)
	}
	if cfg.MinIOBucket == "" {
		return fmt.Errorf("dbrouter: minio bucket is required for region %q", cfg.Region)
	}
	return nil
}

func newPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = regionalSearchPath

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

func newMinIOClient(cfg RegionConfig) (*minio.Client, error) {
	client, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure: cfg.MinIOUseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}
	return client, nil
}

// DB returns the PostgreSQL pool for reg.
func (r *DBRouter) DB(reg region.Region) (PgxPool, error) {
	if r == nil {
		return nil, ErrUnknownRegion
	}

	pool, ok := r.pools[reg]
	if !ok {
		return nil, ErrUnknownRegion
	}
	if pool == nil {
		return nil, ErrPoolNil
	}

	return pool, nil
}

// MustDB returns the pool for reg or panics if the region is unknown.
func (r *DBRouter) MustDB(reg region.Region) PgxPool {
	pool, err := r.DB(reg)
	if err != nil {
		panic(fmt.Sprintf("dbrouter: MustDB(%q): %v", reg, err))
	}
	return pool
}

// MinIO returns the MinIO client for reg.
func (r *DBRouter) MinIO(reg region.Region) (*minio.Client, error) {
	if r == nil {
		return nil, ErrUnknownRegion
	}

	client, ok := r.minio[reg]
	if !ok {
		return nil, ErrUnknownRegion
	}
	if client == nil {
		return nil, fmt.Errorf("dbrouter: minio client is nil for region %q", reg)
	}

	return client, nil
}

// MustMinIO returns the MinIO client for reg or panics if the region is unknown.
func (r *DBRouter) MustMinIO(reg region.Region) *minio.Client {
	client, err := r.MinIO(reg)
	if err != nil {
		panic(fmt.Sprintf("dbrouter: MustMinIO(%q): %v", reg, err))
	}
	return client
}

// Bucket returns the configured bucket name for reg.
func (r *DBRouter) Bucket(reg region.Region) string {
	if r == nil {
		return ""
	}
	return r.buckets[reg]
}

// Close closes all PostgreSQL pools.
func (r *DBRouter) Close() {
	if r == nil {
		return
	}
	for reg, pool := range r.pools {
		if pool != nil {
			pool.Close()
		}
		delete(r.pools, reg)
	}
}
