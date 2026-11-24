package s3compat_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Angle-HR/server/pkg/platform/blob"
	"github.com/Angle-HR/server/pkg/platform/blob/s3compat"
)

// Presigning is offline (the signing region is fixed), so no server is needed.
func TestPresignedURLsUseThePublicEndpoint(t *testing.T) {
	s, err := s3compat.New(s3compat.Config{
		Endpoint: "minio:9000", PublicEndpoint: "localhost:39000", Bucket: "bkt", Region: "auto",
		AccessKey: "k", SecretKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	put, err := s.PresignPut(context.Background(), "u/x", blob.PutOptions{ContentType: "application/pdf", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	get, err := s.PresignGet(context.Background(), "f/x", blob.GetOptions{TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{put.URL, get} {
		u, err := url.Parse(raw)
		if err != nil || u.Host != "localhost:39000" || u.Scheme != "http" || !strings.Contains(raw, "X-Amz-Signature=") {
			t.Errorf("presigned url = %s (%v)", raw, err)
		}
	}
}

func TestPresignedURLsKeepTheEndpointWithoutAPublicOne(t *testing.T) {
	s, err := s3compat.New(s3compat.Config{Endpoint: "minio:9000", Bucket: "bkt", Region: "auto", AccessKey: "k", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	get, err := s.PresignGet(context.Background(), "f/x", blob.GetOptions{TTL: time.Minute})
	if err != nil || !strings.HasPrefix(get, "http://minio:9000/") {
		t.Fatalf("%s %v", get, err)
	}
}
