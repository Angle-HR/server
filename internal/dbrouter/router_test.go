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
	if uk.MinIOEndpoint != "localhost:9000" {
		t.Fatalf("uk endpoint: got %q", uk.MinIOEndpoint)
	}
	if uk.MinIOUseSSL {
		t.Fatal("uk MinIOUseSSL should be false without https endpoint")
	}
}

func TestLoadConfigsFromEnv_httpsSSL(t *testing.T) {
	setFullEnv(t)
	t.Setenv("ANGLEHR_UK_MINIO_ENDPOINT", "https://minio.example.com:9000")

	configs, err := LoadConfigsFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigsFromEnv: %v", err)
	}

	for _, cfg := range configs {
		if cfg.Region == region.RegionUK {
			if !cfg.MinIOUseSSL {
				t.Fatal("expected MinIOUseSSL true for https endpoint")
			}
			if cfg.MinIOEndpoint != "minio.example.com:9000" {
				t.Fatalf("endpoint: got %q", cfg.MinIOEndpoint)
			}
			return
		}
	}
	t.Fatal("uk config not found")
}

func newDBRouterForTest(
	pools map[region.Region]PgxPool,
	minioClients map[region.Region]*minio.Client,
	buckets map[region.Region]string,
) *DBRouter {
	return &DBRouter{
		pools:   pools,
		minio:   minioClients,
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

func TestMinIO_unknownRegion(t *testing.T) {
	router := newDBRouterForTest(nil, map[region.Region]*minio.Client{}, nil)

	_, err := router.MinIO(region.RegionUK)
	if !errors.Is(err, ErrUnknownRegion) {
		t.Fatalf("got %v, want ErrUnknownRegion", err)
	}
}

func TestMustMinIO_panics(t *testing.T) {
	router := newDBRouterForTest(nil, map[region.Region]*minio.Client{}, nil)

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	router.MustMinIO(region.RegionUK)
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

	regions := []struct {
		suffix string
		port   string
	}{
		{"UK", "9000"},
		{"US", "9002"},
		{"AFRICA", "9004"},
		{"EU", "9006"},
	}

	for _, r := range regions {
		prefix := "ANGLEHR_" + r.suffix
		t.Setenv(prefix+"_POSTGRES_DSN", "postgres://"+r.suffix)
		t.Setenv(prefix+"_MINIO_ENDPOINT", "localhost:"+r.port)
		t.Setenv(prefix+"_MINIO_ACCESS_KEY", "access")
		t.Setenv(prefix+"_MINIO_SECRET_KEY", "secret")
		t.Setenv(prefix+"_MINIO_BUCKET", "bucket-"+r.suffix)
	}
}
