package dbrouter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/platform/blob"
	"github.com/Angle-HR/server/pkg/platform/blob/azureblob"
	"github.com/Angle-HR/server/pkg/platform/blob/s3compat"
)

// poolHealthCheckPeriod is how often idle pool connections are health-checked.
const poolHealthCheckPeriod = 30 * time.Second

var (
	// ErrUnknownRegion indicates the region is not configured on this router.
	ErrUnknownRegion = errors.New("dbrouter: unknown region")
	// ErrPoolNil indicates a pool entry exists but is nil.
	ErrPoolNil = errors.New("dbrouter: pool is nil for region")
)

const regionalSearchPath = "accounts,waitlist,hiring,public"

// DBRouter holds one PostgreSQL pool and one R2 (S3-compatible) client per region.
type DBRouter struct {
	pools   map[region.Region]PgxPool
	storage map[region.Region]blob.BlobStore
	buckets map[region.Region]string
}

// NewWithPools returns a DBRouter backed by the given pools. It is intended for tests.
func NewWithPools(pools map[region.Region]PgxPool) *DBRouter {
	if pools == nil {
		pools = map[region.Region]PgxPool{}
	}

	return &DBRouter{
		pools:   pools,
		storage: map[region.Region]blob.BlobStore{},
		buckets: map[region.Region]string{},
	}
}

// New initializes all regional pools and R2 clients. Startup fails if any region
// is missing, duplicated, or unreachable.
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
		storage: make(map[region.Region]blob.BlobStore, len(allRegions())),
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

		slog.Info("dbrouter connecting", "region", reg, "component", "r2")
		client, err := newBlobStore(cfg)
		if err != nil {
			router.Close()
			return nil, fmt.Errorf("dbrouter: r2 %s: %w", reg, err)
		}

		router.storage[reg] = client
		router.buckets[reg] = cfg.R2Bucket
	}

	return router, nil
}

func validateRegionConfig(cfg RegionConfig) error {
	if cfg.PostgresDSN == "" {
		return fmt.Errorf("dbrouter: postgres DSN is required for region %q", cfg.Region)
	}
	if cfg.BlobProvider == BlobProviderAzure {
		if cfg.AzureAccount == "" || cfg.R2Bucket == "" {
			return fmt.Errorf("dbrouter: azure account and container are required for region %q", cfg.Region)
		}
		return nil
	}
	if cfg.R2Endpoint == "" {
		return fmt.Errorf("dbrouter: r2 endpoint is required for region %q", cfg.Region)
	}
	if (cfg.R2AccessKey == "") != (cfg.R2SecretKey == "") {
		return fmt.Errorf("dbrouter: r2 access key and secret key must both be set or both empty for region %q", cfg.Region)
	}
	if cfg.R2Bucket == "" {
		return fmt.Errorf("dbrouter: r2 bucket is required for region %q", cfg.Region)
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
	cfg.HealthCheckPeriod = poolHealthCheckPeriod
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

func newBlobStore(cfg RegionConfig) (blob.BlobStore, error) {
	if cfg.BlobProvider == BlobProviderAzure {
		st, err := azureblob.New(azureblob.Config{
			AccountName: cfg.AzureAccount, AccountKey: cfg.AzureKey, Container: cfg.R2Bucket, Endpoint: cfg.AzureEndpoint,
		})
		if err != nil {
			return nil, fmt.Errorf("create azure blob store: %w", err)
		}
		return st, nil
	}
	signingRegion := cfg.R2Region
	if signingRegion == "" {
		signingRegion = "auto"
	}
	// With no static keys the adapter uses the AWS environment, then the ECS task role / instance profile.
	st, err := s3compat.New(s3compat.Config{
		Endpoint: cfg.R2Endpoint, Bucket: cfg.R2Bucket, Region: signingRegion, UseSSL: cfg.R2UseSSL,
		AccessKey: cfg.R2AccessKey, SecretKey: cfg.R2SecretKey,
		PublicEndpoint: cfg.R2PublicEndpoint, PublicUseSSL: cfg.R2PublicUseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create s3 blob store: %w", err)
	}
	return st, nil
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

// Blob returns the object store of reg's bucket (S3, R2, MinIO or Azure container).
func (r *DBRouter) Blob(reg region.Region) (blob.BlobStore, error) {
	if r == nil {
		return nil, ErrUnknownRegion
	}

	st, ok := r.storage[reg]
	if !ok {
		return nil, ErrUnknownRegion
	}
	if st == nil {
		return nil, fmt.Errorf("dbrouter: blob store is nil for region %q", reg)
	}

	return st, nil
}

// MustBlob returns the store for reg or panics if the region is unknown.
func (r *DBRouter) MustBlob(reg region.Region) blob.BlobStore {
	st, err := r.Blob(reg)
	if err != nil {
		panic(fmt.Sprintf("dbrouter: MustBlob(%q): %v", reg, err))
	}
	return st
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
