// Package s3compat is the BlobStore adapter for S3-compatible storage: AWS S3, Cloudflare R2 and MinIO.
package s3compat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Angle-HR/server/pkg/platform/blob"
)

// Config selects a bucket.
type Config struct {
	// Endpoint is host[:port] without scheme.
	Endpoint string
	Bucket   string
	// Region is the signing region; "auto" for R2, empty means "auto".
	Region string
	UseSSL bool
	// PublicEndpoint is the host[:port] clients outside the service's network use to reach the same bucket, when
	// that differs from Endpoint (a MinIO container reached as minio:9000 inside the compose network and as
	// localhost:9000 from a browser, or an internal VPC endpoint against a public CDN host). Presigned URLs are
	// signed for it, because the host is part of the signature; every other call keeps using Endpoint. Empty means
	// Endpoint is reachable by clients.
	PublicEndpoint string
	PublicUseSSL   bool
	// AccessKey and SecretKey are static credentials. When both are empty the AWS environment variables and then
	// the instance or task role are used.
	AccessKey string
	SecretKey string
}

// Store is one bucket. It implements blob.BlobStore.
type Store struct {
	client *minio.Client
	// signer signs the presigned URLs. It is client unless a PublicEndpoint is configured.
	signer *minio.Client
	bucket string
}

var _ blob.BlobStore = (*Store)(nil)

// New returns the store of cfg.Bucket.
func New(cfg Config) (*Store, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("s3compat: endpoint and bucket are required")
	}
	creds := credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	if cfg.AccessKey == "" {
		creds = credentials.NewChainCredentials([]credentials.Provider{&credentials.EnvAWS{}, &credentials.IAM{}})
	}
	region := cfg.Region
	if region == "" {
		region = "auto"
	}
	c, err := minio.New(cfg.Endpoint, &minio.Options{Creds: creds, Secure: cfg.UseSSL, Region: region})
	if err != nil {
		return nil, fmt.Errorf("s3compat: client: %w", err)
	}
	signer := c
	if cfg.PublicEndpoint != "" && (cfg.PublicEndpoint != cfg.Endpoint || cfg.PublicUseSSL != cfg.UseSSL) {
		// The signing region is fixed, so presigning never contacts the server (no bucket location lookup).
		signer, err = minio.New(cfg.PublicEndpoint, &minio.Options{Creds: creds, Secure: cfg.PublicUseSSL, Region: region})
		if err != nil {
			return nil, fmt.Errorf("s3compat: public client: %w", err)
		}
	}
	return &Store{client: c, signer: signer, bucket: cfg.Bucket}, nil
}

// EnsureBucket creates the bucket when it does not exist. For local stacks and tests.
func (s *Store) EnsureBucket(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("s3compat: bucket exists: %w", err)
	}
	if ok {
		return nil
	}
	if err = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("s3compat: make bucket: %w", err)
	}
	return nil
}

func notFound(err error) bool {
	code := minio.ToErrorResponse(err).Code
	return code == "NoSuchKey" || code == "NotFound"
}

// PresignPut signs a PUT with the content type, so the browser must send that exact Content-Type header. R2 and
// S3 cannot enforce a size limit on a presigned PUT; the size is checked after the upload instead.
func (s *Store) PresignPut(ctx context.Context, key string, opts blob.PutOptions) (blob.PutTarget, error) {
	h := http.Header{}
	h.Set("Content-Type", opts.ContentType)
	u, err := s.signer.PresignHeader(ctx, http.MethodPut, s.bucket, key, opts.TTL, nil, h)
	if err != nil {
		return blob.PutTarget{}, fmt.Errorf("s3compat: presign: %w", err)
	}
	return blob.PutTarget{
		URL: u.String(), Method: http.MethodPut, Headers: map[string]string{"Content-Type": opts.ContentType},
	}, nil
}

// PresignGet signs a GET that expires after opts.TTL.
func (s *Store) PresignGet(ctx context.Context, key string, opts blob.GetOptions) (string, error) {
	q := url.Values{}
	if opts.Filename != "" {
		q.Set("response-content-disposition", mime.FormatMediaType("attachment", map[string]string{"filename": opts.Filename}))
	}
	if opts.ContentType != "" {
		q.Set("response-content-type", opts.ContentType)
	}
	u, err := s.signer.PresignedGetObject(ctx, s.bucket, key, opts.TTL, q)
	if err != nil {
		return "", fmt.Errorf("s3compat: presign get: %w", err)
	}
	return u.String(), nil
}

// Stat returns the size of an object.
func (s *Store) Stat(ctx context.Context, key string) (int64, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if notFound(err) {
			return 0, blob.ErrNotFound
		}
		return 0, fmt.Errorf("s3compat: stat: %w", err)
	}
	return info.Size, nil
}

// Exists reports whether an object exists.
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Stat(ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Open reads an object.
func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3compat: get: %w", err)
	}
	// GetObject does not contact the server; Stat does, and reports a missing object now rather than on read.
	if _, err = obj.Stat(); err != nil {
		_ = obj.Close()
		if notFound(err) {
			return nil, blob.ErrNotFound
		}
		return nil, fmt.Errorf("s3compat: get: %w", err)
	}
	return obj, nil
}

// Put writes an object.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("s3compat: put: %w", err)
	}
	return nil
}

// Delete removes an object. S3 deletes of missing objects succeed, and a missing object is not an error here
// either.
func (s *Store) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && !notFound(err) {
		return fmt.Errorf("s3compat: delete: %w", err)
	}
	return nil
}
