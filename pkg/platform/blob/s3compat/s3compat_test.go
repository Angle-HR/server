package s3compat_test

import (
	"context"
	"os"
	"testing"

	"github.com/Angle-HR/server/pkg/platform/blob"
	"github.com/Angle-HR/server/pkg/platform/blob/blobtest"
	"github.com/Angle-HR/server/pkg/platform/blob/s3compat"
)

// TestConformance runs against a MinIO (or any S3) endpoint given by BLOB_S3_ENDPOINT, for example
// BLOB_S3_ENDPOINT=127.0.0.1:20900 with the default minioadmin credentials. It is skipped when unset.
func TestConformance(t *testing.T) {
	endpoint := os.Getenv("BLOB_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("BLOB_S3_ENDPOINT not set")
	}
	cfg := s3compat.Config{
		Endpoint: endpoint, Bucket: "blobtest", Region: "us-east-1",
		AccessKey: envOr("BLOB_S3_ACCESS_KEY", "minioadmin"), SecretKey: envOr("BLOB_S3_SECRET_KEY", "minioadmin"),
	}
	blobtest.Run(t, func(t *testing.T) blob.BlobStore {
		s, err := s3compat.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.EnsureBucket(context.Background()); err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
