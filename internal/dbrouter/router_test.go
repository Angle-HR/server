package dbrouter

import (
	"errors"
	"testing"

	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/platform/blob"
)

func TestLoadConfigsFromEnv_missing(t *testing.T) {
	t.Setenv("ANGLEHR_UK_POSTGRES_DSN", "")

	_, err := LoadConfigsFromEnv()
	if err == nil {
		t.Fatal("expected error for missing ANGLEHR_UK_POSTGRES_DSN")
	}
}

func TestLoadConfigsFromEnv_missingSharedCredentials(t *testing.T) {
	setFullEnv(t)
	t.Setenv("R2_ACCESS_KEY", "")

	_, err := LoadConfigsFromEnv()
	if err == nil {
		t.Fatal("expected error for missing R2_ACCESS_KEY")
	}
}

func TestLoadConfigsFromEnv_success(t *testing.T) {
	setFullEnv(t)

	configs, err := LoadConfigsFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigsFromEnv: %v", err)
	}
	if len(configs) != 5 {
		t.Fatalf("got %d configs, want 5", len(configs))
	}

	byRegion := make(map[region.Region]RegionConfig, len(configs))
	for _, cfg := range configs {
		byRegion[cfg.Region] = cfg
	}

	uk := byRegion[region.RegionUK]
	if uk.PostgresDSN != "postgres://UK" {
		t.Fatalf("uk dsn: got %q", uk.PostgresDSN)
	}
	if uk.R2Endpoint != "abc123.r2.cloudflarestorage.com" {
		t.Fatalf("uk endpoint: got %q", uk.R2Endpoint)
	}
	if uk.R2AccessKey != "access" || uk.R2SecretKey != "secret" {
		t.Fatalf("uk credentials: got %q / %q", uk.R2AccessKey, uk.R2SecretKey)
	}
	if !uk.R2UseSSL {
		t.Fatal("uk R2UseSSL should be true for R2 endpoint")
	}

	asia := byRegion[region.RegionAsia]
	if asia.PostgresDSN != "postgres://ASIA" {
		t.Fatalf("asia dsn: got %q", asia.PostgresDSN)
	}
	if asia.R2Bucket != "anglehr-asia" {
		t.Fatalf("asia bucket: got %q", asia.R2Bucket)
	}
}

func TestLoadConfigsFromEnv_missingR2Endpoint(t *testing.T) {
	setFullEnv(t)
	t.Setenv("R2_ENDPOINT", "")

	_, err := LoadConfigsFromEnv()
	if err == nil {
		t.Fatal("expected error for missing R2_ENDPOINT")
	}
}

func TestLoadConfigsFromEnv_sharedEndpoint(t *testing.T) {
	setFullEnv(t)
	t.Setenv("R2_ENDPOINT", "https://default.r2.cloudflarestorage.com")

	configs, err := LoadConfigsFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigsFromEnv: %v", err)
	}

	for _, cfg := range configs {
		if cfg.R2Endpoint != "default.r2.cloudflarestorage.com" {
			t.Fatalf("region %s endpoint: got %q", cfg.Region, cfg.R2Endpoint)
		}
	}
}

func TestLoadConfigsFromEnv_httpsSSL(t *testing.T) {
	setFullEnv(t)
	t.Setenv("R2_ENDPOINT", "https://abc123.r2.cloudflarestorage.com")

	configs, err := LoadConfigsFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigsFromEnv: %v", err)
	}

	for _, cfg := range configs {
		if cfg.Region == region.RegionUK {
			if !cfg.R2UseSSL {
				t.Fatal("expected R2UseSSL true for https endpoint")
			}
			if cfg.R2Endpoint != "abc123.r2.cloudflarestorage.com" {
				t.Fatalf("endpoint: got %q", cfg.R2Endpoint)
			}
			return
		}
	}
	t.Fatal("uk config not found")
}

func newDBRouterForTest(
	pools map[region.Region]PgxPool,
	storageClients map[region.Region]blob.BlobStore,
	buckets map[region.Region]string,
) *DBRouter {
	return &DBRouter{
		pools:   pools,
		storage: storageClients,
		buckets: buckets,
	}
}

func TestDB_unknownRegion(t *testing.T) {
	router := newDBRouterForTest(map[region.Region]PgxPool{}, nil, nil)

	_, err := router.DB(region.Region("invalid"))
	if !errors.Is(err, ErrUnknownRegion) {
		t.Fatalf("got %v, want ErrUnknownRegion", err)
	}
}

func TestDB_nilPool(t *testing.T) {
	router := newDBRouterForTest(map[region.Region]PgxPool{
		region.RegionUK: nil,
	}, nil, nil)

	_, err := router.DB(region.RegionUK)
	if !errors.Is(err, ErrPoolNil) {
		t.Fatalf("got %v, want ErrPoolNil", err)
	}
}

func TestMustDB_panics(t *testing.T) {
	router := newDBRouterForTest(map[region.Region]PgxPool{}, nil, nil)

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	router.MustDB(region.RegionUK)
}

func TestBlob_unknownRegion(t *testing.T) {
	router := newDBRouterForTest(nil, map[region.Region]blob.BlobStore{}, nil)

	_, err := router.Blob(region.RegionUK)
	if !errors.Is(err, ErrUnknownRegion) {
		t.Fatalf("got %v, want ErrUnknownRegion", err)
	}
}

func TestMustBlob_panics(t *testing.T) {
	router := newDBRouterForTest(nil, map[region.Region]blob.BlobStore{}, nil)

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	router.MustBlob(region.RegionUK)
}

func TestBucket(t *testing.T) {
	router := newDBRouterForTest(nil, nil, map[region.Region]string{
		region.RegionUK: "anglehr-uk",
	})

	if got := router.Bucket(region.RegionUK); got != "anglehr-uk" {
		t.Fatalf("Bucket: got %q", got)
	}
}

func TestClose_empty(t *testing.T) {
	router := newDBRouterForTest(map[region.Region]PgxPool{}, nil, nil)
	router.Close()
}

func setFullEnv(t *testing.T) {
	t.Helper()

	t.Setenv("R2_ACCESS_KEY", "access")
	t.Setenv("R2_SECRET_KEY", "secret")
	t.Setenv("R2_ENDPOINT", "https://abc123.r2.cloudflarestorage.com")

	regions := []struct {
		suffix string
		bucket string
	}{
		{"UK", "anglehr-uk"},
		{"US", "anglehr-us"},
		{"AFRICA", "anglehr-africa"},
		{"EU", "anglehr-eu"},
		{"ASIA", "anglehr-asia"},
	}

	for _, r := range regions {
		prefix := "ANGLEHR_" + r.suffix
		t.Setenv(prefix+"_POSTGRES_DSN", "postgres://"+r.suffix)
		t.Setenv(prefix+"_R2_BUCKET", r.bucket)
	}
}

func TestLoadConfigsFromEnv_noKeysUsesCredentialChain(t *testing.T) {
	setFullEnv(t)
	t.Setenv("R2_ACCESS_KEY", "")
	t.Setenv("R2_SECRET_KEY", "")
	t.Setenv("R2_ENDPOINT", "https://s3.eu-west-2.amazonaws.com")

	configs, err := LoadConfigsFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigsFromEnv: %v", err)
	}
	for _, cfg := range configs {
		if cfg.R2AccessKey != "" || cfg.R2SecretKey != "" {
			t.Fatalf("region %s: expected empty keys", cfg.Region)
		}
		if cfg.R2Region != "eu-west-2" {
			t.Fatalf("region %s signing region: got %q", cfg.Region, cfg.R2Region)
		}
		if !cfg.R2UseSSL {
			t.Fatalf("region %s: expected SSL for S3", cfg.Region)
		}
		if err := validateRegionConfig(cfg); err != nil {
			t.Fatalf("validateRegionConfig: %v", err)
		}
		if _, err := newBlobStore(cfg); err != nil {
			t.Fatalf("newBlobStore: %v", err)
		}
	}
}

func TestLoadConfigsFromEnv_onlyOneKey(t *testing.T) {
	setFullEnv(t)
	t.Setenv("R2_SECRET_KEY", "")

	if _, err := LoadConfigsFromEnv(); err == nil {
		t.Fatal("expected error when only R2_ACCESS_KEY is set")
	}
}

func TestLoadConfigsFromEnv_perRegionEndpoint(t *testing.T) {
	setFullEnv(t)
	t.Setenv("R2_ENDPOINT", "https://s3.eu-west-2.amazonaws.com")
	t.Setenv("ANGLEHR_ASIA_R2_ENDPOINT", "https://s3.ap-southeast-1.amazonaws.com")
	t.Setenv("ANGLEHR_US_R2_REGION", "us-east-1")

	configs, err := LoadConfigsFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigsFromEnv: %v", err)
	}
	want := map[region.Region][2]string{
		region.RegionUK:   {"s3.eu-west-2.amazonaws.com", "eu-west-2"},
		region.RegionAsia: {"s3.ap-southeast-1.amazonaws.com", "ap-southeast-1"},
		region.RegionUS:   {"s3.eu-west-2.amazonaws.com", "us-east-1"},
	}
	for _, cfg := range configs {
		w, ok := want[cfg.Region]
		if !ok {
			continue
		}
		if cfg.R2Endpoint != w[0] || cfg.R2Region != w[1] {
			t.Fatalf("region %s: got %q / %q, want %q / %q", cfg.Region, cfg.R2Endpoint, cfg.R2Region, w[0], w[1])
		}
	}
}

func TestLoadConfigsFromEnv_r2SigningRegionAuto(t *testing.T) {
	setFullEnv(t)

	configs, err := LoadConfigsFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigsFromEnv: %v", err)
	}
	for _, cfg := range configs {
		if cfg.R2Region != "auto" {
			t.Fatalf("region %s: got %q, want auto", cfg.Region, cfg.R2Region)
		}
	}
}

func TestAWSRegionFromEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"s3.eu-west-2.amazonaws.com":            "eu-west-2",
		"s3.dualstack.af-south-1.amazonaws.com": "af-south-1",
		"s3.us-east-1.amazonaws.com:443":        "us-east-1",
		"abc123.r2.cloudflarestorage.com":       "",
		"localhost:9000":                        "",
	} {
		if got := awsRegionFromEndpoint(in); got != want {
			t.Errorf("awsRegionFromEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
