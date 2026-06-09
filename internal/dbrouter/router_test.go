package dbrouter

import (
	"errors"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/Angle-HR/server/internal/region"
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
	if len(configs) != 4 {
		t.Fatalf("got %d configs, want 4", len(configs))
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
	storageClients map[region.Region]*minio.Client,
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

func TestR2_unknownRegion(t *testing.T) {
	router := newDBRouterForTest(nil, map[region.Region]*minio.Client{}, nil)

	_, err := router.R2(region.RegionUK)
	if !errors.Is(err, ErrUnknownRegion) {
		t.Fatalf("got %v, want ErrUnknownRegion", err)
	}
}

func TestMustR2_panics(t *testing.T) {
	router := newDBRouterForTest(nil, map[region.Region]*minio.Client{}, nil)

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	router.MustR2(region.RegionUK)
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
	}

	for _, r := range regions {
		prefix := "ANGLEHR_" + r.suffix
		t.Setenv(prefix+"_POSTGRES_DSN", "postgres://"+r.suffix)
		t.Setenv(prefix+"_R2_BUCKET", r.bucket)
	}
}
