// Package dbrouter routes database and object-storage connections by geographic region.
package dbrouter

import (
	"fmt"
	"os"
	"strings"

	"github.com/Angle-HR/server/internal/region"
)

// RegionConfig holds per-region PostgreSQL and Cloudflare R2 connection settings.
type RegionConfig struct {
	Region       region.Region
	PostgresDSN  string
	R2Endpoint   string
	R2AccessKey  string
	R2SecretKey  string
	R2Bucket     string
	R2UseSSL     bool
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
// R2_ACCESS_KEY and R2_SECRET_KEY are shared across all regions.
// R2_ENDPOINT is the default S3 API host; ANGLEHR_<REGION>_R2_ENDPOINT overrides it per region.
// All four regions must be fully configured; missing variables return an error.
func LoadConfigsFromEnv() ([]RegionConfig, error) {
	accessKey := strings.TrimSpace(os.Getenv("R2_ACCESS_KEY"))
	if accessKey == "" {
		return nil, fmt.Errorf("dbrouter: R2_ACCESS_KEY is required")
	}

	secretKey := strings.TrimSpace(os.Getenv("R2_SECRET_KEY"))
	if secretKey == "" {
		return nil, fmt.Errorf("dbrouter: R2_SECRET_KEY is required")
	}

	defaultEndpoint := strings.TrimSpace(os.Getenv("R2_ENDPOINT"))

	configs := make([]RegionConfig, 0, len(allRegions()))

	for _, reg := range allRegions() {
		suffix := envSuffix(reg)
		cfg := RegionConfig{
			Region:      reg,
			R2AccessKey: accessKey,
			R2SecretKey: secretKey,
		}

		postgresDSN := strings.TrimSpace(os.Getenv(envKey(suffix, "POSTGRES_DSN")))
		if postgresDSN == "" {
			return nil, fmt.Errorf("dbrouter: %s is required", envKey(suffix, "POSTGRES_DSN"))
		}
		cfg.PostgresDSN = postgresDSN

		bucket := strings.TrimSpace(os.Getenv(envKey(suffix, "R2_BUCKET")))
		if bucket == "" {
			return nil, fmt.Errorf("dbrouter: %s is required", envKey(suffix, "R2_BUCKET"))
		}
		cfg.R2Bucket = bucket

		rawEndpoint := strings.TrimSpace(os.Getenv(envKey(suffix, "R2_ENDPOINT")))
		if rawEndpoint == "" {
			rawEndpoint = defaultEndpoint
		}
		if rawEndpoint == "" {
			return nil, fmt.Errorf("dbrouter: %s or R2_ENDPOINT is required", envKey(suffix, "R2_ENDPOINT"))
		}

		cfg.R2Endpoint = stripEndpointScheme(rawEndpoint)

		if sslRaw := strings.TrimSpace(os.Getenv(envKey(suffix, "R2_USE_SSL"))); sslRaw != "" {
			cfg.R2UseSSL = sslRaw == "1" || strings.EqualFold(sslRaw, "true")
		} else {
			cfg.R2UseSSL = strings.HasPrefix(strings.ToLower(rawEndpoint), "https://") ||
				strings.Contains(strings.ToLower(cfg.R2Endpoint), "r2.cloudflarestorage.com")
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
