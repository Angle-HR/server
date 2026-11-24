// Package dbrouter routes database and object-storage connections by geographic region.
package dbrouter

import (
	"fmt"
	"os"
	"strings"

	"github.com/Angle-HR/server/internal/region"
)

// RegionConfig holds per-region PostgreSQL and S3-compatible object storage (Cloudflare R2 or AWS S3) settings.
// Empty R2AccessKey and R2SecretKey mean the client uses the AWS credential chain (env vars, then the ECS task
// role).
type RegionConfig struct {
	Region      region.Region
	PostgresDSN string
	R2Endpoint  string
	R2Region    string
	R2AccessKey string
	R2SecretKey string
	R2Bucket    string
	R2UseSSL    bool
	// R2PublicEndpoint is the host[:port] clients outside the service network use for presigned URLs when it
	// differs from R2Endpoint (see s3compat.Config.PublicEndpoint). Empty: R2Endpoint.
	R2PublicEndpoint string
	R2PublicUseSSL   bool

	// BlobProvider is "s3" (default: S3, R2, MinIO) or "azure". For azure, R2Bucket is the container name.
	BlobProvider  string
	AzureAccount  string
	AzureKey      string // empty means managed identity
	AzureEndpoint string // empty means https://<account>.blob.core.windows.net
}

// Blob providers.
const (
	BlobProviderS3    = "s3"
	BlobProviderAzure = "azure"
)

func allRegions() []region.Region {
	return Regions()
}

// Regions returns the configured geographic regions in stable order.
func Regions() []region.Region {
	return []region.Region{
		region.RegionUK,
		region.RegionUS,
		region.RegionAfrica,
		region.RegionEU,
		region.RegionAsia,
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
	case region.RegionAsia:
		return "ASIA"
	case region.RegionUnknown, region.RegionGlobal:
		return strings.ToUpper(string(reg))
	default:
		return strings.ToUpper(string(reg))
	}
}

func envKey(suffix, name string) string {
	return "ANGLEHR_" + suffix + "_" + name
}

// LoadConfigsFromEnv reads per-region settings from the environment.
//
// R2_ACCESS_KEY and R2_SECRET_KEY are shared across all regions. Set both for Cloudflare R2 or static S3 keys, or
// neither to use the AWS credential chain (the ECS task role in AWS).
//
// ANGLEHR_<REGION>_R2_ENDPOINT overrides R2_ENDPOINT for one region; every region needs one of them.
// ANGLEHR_<REGION>_R2_REGION (then R2_REGION) is the signing region. When unset it comes from an AWS endpoint
// such as s3.eu-west-2.amazonaws.com, and is "auto" otherwise, which is what R2 expects.
//
// R2_PUBLIC_ENDPOINT (or ANGLEHR_<REGION>_R2_PUBLIC_ENDPOINT) is the address clients outside the service's network
// use to upload to and download from the bucket, when it differs from R2_ENDPOINT; presigned URLs are signed for
// it. "https://" selects TLS. Local stacks: R2_ENDPOINT=http://minio:9000, R2_PUBLIC_ENDPOINT=http://localhost:9000.
//
// All regions must be fully configured; missing variables return an error.
func LoadConfigsFromEnv() ([]RegionConfig, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("BLOB_PROVIDER")), BlobProviderAzure) {
		return loadAzureConfigs()
	}
	accessKey := strings.TrimSpace(os.Getenv("R2_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("R2_SECRET_KEY"))
	if (accessKey == "") != (secretKey == "") {
		return nil, fmt.Errorf("dbrouter: set both R2_ACCESS_KEY and R2_SECRET_KEY, or neither to use the AWS credential chain")
	}

	sharedEndpoint := strings.TrimSpace(os.Getenv("R2_ENDPOINT"))
	sharedRegion := strings.TrimSpace(os.Getenv("R2_REGION"))

	configs := make([]RegionConfig, 0, len(allRegions()))

	for _, reg := range allRegions() {
		suffix := envSuffix(reg)

		rawEndpoint := strings.TrimSpace(os.Getenv(envKey(suffix, "R2_ENDPOINT")))
		if rawEndpoint == "" {
			rawEndpoint = sharedEndpoint
		}
		if rawEndpoint == "" {
			return nil, fmt.Errorf("dbrouter: R2_ENDPOINT or %s is required", envKey(suffix, "R2_ENDPOINT"))
		}
		endpoint := stripEndpointScheme(rawEndpoint)

		signingRegion := strings.TrimSpace(os.Getenv(envKey(suffix, "R2_REGION")))
		if signingRegion == "" {
			signingRegion = sharedRegion
		}
		if signingRegion == "" {
			signingRegion = awsRegionFromEndpoint(endpoint)
		}
		if signingRegion == "" {
			signingRegion = "auto"
		}

		cfg := RegionConfig{
			Region:      reg,
			R2AccessKey: accessKey,
			R2SecretKey: secretKey,
			R2Endpoint:  endpoint,
			R2Region:    signingRegion,
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

		if sslRaw := strings.TrimSpace(os.Getenv(envKey(suffix, "R2_USE_SSL"))); sslRaw != "" {
			cfg.R2UseSSL = sslRaw == "1" || strings.EqualFold(sslRaw, "true")
		} else {
			cfg.R2UseSSL = strings.HasPrefix(strings.ToLower(rawEndpoint), "https://") ||
				strings.Contains(strings.ToLower(endpoint), "r2.cloudflarestorage.com") ||
				strings.HasSuffix(strings.ToLower(endpoint), ".amazonaws.com")
		}

		publicRaw := strings.TrimSpace(os.Getenv(envKey(suffix, "R2_PUBLIC_ENDPOINT")))
		if publicRaw == "" {
			publicRaw = strings.TrimSpace(os.Getenv("R2_PUBLIC_ENDPOINT"))
		}
		if publicRaw != "" {
			cfg.R2PublicEndpoint = stripEndpointScheme(publicRaw)
			cfg.R2PublicUseSSL = strings.HasPrefix(strings.ToLower(publicRaw), "https://")
		}

		configs = append(configs, cfg)
	}

	return configs, nil
}

// awsRegionFromEndpoint returns the region in an AWS S3 endpoint (s3.<region>.amazonaws.com or
// s3.dualstack.<region>.amazonaws.com), or "" for anything else.
func awsRegionFromEndpoint(endpoint string) string {
	host := strings.ToLower(endpoint)
	if i := strings.IndexAny(host, ":/"); i >= 0 {
		host = host[:i]
	}
	if !strings.HasSuffix(host, ".amazonaws.com") {
		return ""
	}
	parts := strings.Split(strings.TrimSuffix(host, ".amazonaws.com"), ".")
	if len(parts) < 2 || parts[0] != "s3" {
		return ""
	}
	return parts[len(parts)-1]
}

func stripEndpointScheme(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	return endpoint
}

// loadAzureConfigs reads the Azure Blob variables: AZURE_STORAGE_ACCOUNT, AZURE_STORAGE_KEY (empty for managed
// identity) and AZURE_BLOB_ENDPOINT (optional, for Azurite), with ANGLEHR_<REGION>_R2_BUCKET naming the container.
func loadAzureConfigs() ([]RegionConfig, error) {
	configs := make([]RegionConfig, 0, len(allRegions()))
	for _, reg := range allRegions() {
		suffix := envSuffix(reg)
		cfg := RegionConfig{
			Region:        reg,
			BlobProvider:  BlobProviderAzure,
			AzureAccount:  strings.TrimSpace(os.Getenv("AZURE_STORAGE_ACCOUNT")),
			AzureKey:      strings.TrimSpace(os.Getenv("AZURE_STORAGE_KEY")),
			AzureEndpoint: strings.TrimSpace(os.Getenv("AZURE_BLOB_ENDPOINT")),
			R2Bucket:      strings.TrimSpace(os.Getenv(envKey(suffix, "R2_BUCKET"))),
			PostgresDSN:   strings.TrimSpace(os.Getenv(envKey(suffix, "POSTGRES_DSN"))),
		}
		if cfg.PostgresDSN == "" {
			return nil, fmt.Errorf("dbrouter: %s is required", envKey(suffix, "POSTGRES_DSN"))
		}
		if cfg.AzureAccount == "" || cfg.R2Bucket == "" {
			return nil, fmt.Errorf("dbrouter: AZURE_STORAGE_ACCOUNT and %s are required", envKey(suffix, "R2_BUCKET"))
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}
