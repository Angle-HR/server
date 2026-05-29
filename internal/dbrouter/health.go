package dbrouter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/region"
)

const healthCheckTimeout = 2 * time.Second

// RegionHealth reports connectivity for one region.
type RegionHealth struct {
	Region region.Region `json:"region"`
	DB     bool          `json:"db"`
	MinIO  bool          `json:"minio"`
	Error  string        `json:"error,omitempty"`
}

// HealthCheck pings each regional pool and checks MinIO bucket access.
func (r *DBRouter) HealthCheck(ctx context.Context) []RegionHealth {
	if r == nil {
		return nil
	}

	results := make([]RegionHealth, 0, len(allRegions()))
	for _, reg := range allRegions() {
		h := RegionHealth{Region: reg}
		var errs []string

		if pool, ok := r.pools[reg]; ok && pool != nil {
			pingCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
			err := pool.Ping(pingCtx)
			cancel()
			if err != nil {
				errs = append(errs, fmt.Sprintf("db: %v", err))
			} else {
				h.DB = true
			}
		} else {
			errs = append(errs, "db: pool not configured")
		}

		if client, ok := r.minio[reg]; ok && client != nil {
			bucket := r.buckets[reg]
			minioCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
			_, err := client.BucketExists(minioCtx, bucket)
			cancel()
			if err != nil {
				errs = append(errs, fmt.Sprintf("minio: %v", err))
			} else {
				h.MinIO = true
			}
		} else {
			errs = append(errs, "minio: client not configured")
		}

		if len(errs) > 0 {
			h.Error = strings.Join(errs, "; ")
		}

		results = append(results, h)
	}

	return results
}
