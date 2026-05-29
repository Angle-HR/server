// Package dbrouter routes database and object storage clients by geographic region.
package dbrouter

import (
	"fmt"
	"os"
	"strings"

	"github.com/Angle-HR/server/internal/region"
)

// RegionConfig holds per-region PostgreSQL and MinIO connection settings.
type RegionConfig struct {
	Region          region.Region
	PostgresDSN     string
	MinIOEndpoint   string
	MinIOAccessKey  string
	MinIOSecretKey  string
	MinIOBucket     string
	MinIOUseSSL     bool
}

func allRegions() []region.Region {
	return []region.Region{
		region.RegionUK,
		region.RegionUS,
		region.RegionAfrica,
		region.RegionEU,
	}
}

func envSuffix(reg region.Region) string {
	switch reg {
	case region.RegionUK:
		return "UK"
	case region.RegionUS:
		return "US"
	case region.RegionAfrica:
		return "AFRICA"
	case region.RegionEU:
		return "EU"
	default:
		return strings.ToUpper(string(reg))
	}
}

func envKey(suffix, name string) string {
	return "ANGLEHR_" + suffix + "_" + name
}

// LoadConfigsFromEnv reads per-region settings from the environment.
// All four regions must be fully configured; missing variables return an error.
func LoadConfigsFromEnv() ([]RegionConfig, error) {
	configs := make([]RegionConfig, 0, len(allRegions()))

	for _, reg := range allRegions() {
		suffix := envSuffix(reg)
		cfg := RegionConfig{Region: reg}

		required := map[string]*string{
			"POSTGRES_DSN":    &cfg.PostgresDSN,
			"MINIO_ENDPOINT":  &cfg.MinIOEndpoint,
			"MINIO_ACCESS_KEY": &cfg.MinIOAccessKey,
			"MINIO_SECRET_KEY": &cfg.MinIOSecretKey,
			"MINIO_BUCKET":    &cfg.MinIOBucket,
		}

		for name, dest := range required {
			key := envKey(suffix, name)
			value := strings.TrimSpace(os.Getenv(key))
			if value == "" {
				return nil, fmt.Errorf("dbrouter: %s is required", key)
			}
			*dest = value
		}

		rawEndpoint := cfg.MinIOEndpoint
		cfg.MinIOEndpoint = stripEndpointScheme(rawEndpoint)

		if sslRaw := strings.TrimSpace(os.Getenv(envKey(suffix, "MINIO_USE_SSL"))); sslRaw != "" {
			cfg.MinIOUseSSL = sslRaw == "1" || strings.EqualFold(sslRaw, "true")
		} else {
			cfg.MinIOUseSSL = strings.HasPrefix(strings.ToLower(rawEndpoint), "https://")
		}

		configs = append(configs, cfg)
	}

	return configs, nil
}

func stripEndpointScheme(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	return endpoint
}
