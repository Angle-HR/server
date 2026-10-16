// Package r2 is the object storage of the upload step: one region's R2 (S3 compatible) bucket through minio-go.
package r2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/Angle-HR/server/internal/hiring/uploads"
)

// Storage is one region's bucket. It implements uploads.Storage.
type Storage struct {
	Client *minio.Client
	Bucket string
}

var _ uploads.Storage = (*Storage)(nil)

// PresignPut signs a PUT with the content type, so the browser must send that exact Content-Type header. R2
// cannot enforce a size limit on a presigned PUT; the size is checked after the upload instead.
func (s *Storage) PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (uploads.PutTarget, error) {
	h := http.Header{}
	h.Set("Content-Type", contentType)
	u, err := s.Client.PresignHeader(ctx, http.MethodPut, s.Bucket, key, ttl, nil, h)
	if err != nil {
		return uploads.PutTarget{}, fmt.Errorf("r2: presign: %w", err)
	}
	return uploads.PutTarget{URL: u.String(), Headers: map[string]string{"Content-Type": contentType}}, nil
}

// PresignGet signs a GET that expires after ttl. The response is forced to download under filename.
func (s *Storage) PresignGet(ctx context.Context, key string, ttl time.Duration, filename, contentType string) (string, error) {
	q := url.Values{}
	q.Set("response-content-disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	if contentType != "" {
		q.Set("response-content-type", contentType)
	}
	u, err := s.Client.PresignedGetObject(ctx, s.Bucket, key, ttl, q)
	if err != nil {
		return "", fmt.Errorf("r2: presign get: %w", err)
	}
	return u.String(), nil
}

func notFound(err error) bool {
	code := minio.ToErrorResponse(err).Code
	return code == "NoSuchKey" || code == "NotFound"
}

// Stat returns the size of an object.
func (s *Storage) Stat(ctx context.Context, key string) (int64, error) {
	info, err := s.Client.StatObject(ctx, s.Bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if notFound(err) {
			return 0, uploads.ErrObjectNotFound
		}
		return 0, fmt.Errorf("r2: stat: %w", err)
	}
	return info.Size, nil
}

// Open reads an object.
func (s *Storage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.Client.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("r2: get: %w", err)
	}
	// GetObject does not contact the server; Stat does, and reports a missing object now rather than on read.
	if _, err = obj.Stat(); err != nil {
		_ = obj.Close()
		if notFound(err) {
			return nil, uploads.ErrObjectNotFound
		}
		return nil, fmt.Errorf("r2: get: %w", err)
	}
	return obj, nil
}

// Put writes an object.
func (s *Storage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if _, err := s.Client.PutObject(ctx, s.Bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("r2: put: %w", err)
	}
	return nil
}

// Remove deletes an object. S3 deletes of missing objects succeed, and a missing object is not an error here
// either.
func (s *Storage) Remove(ctx context.Context, key string) error {
	err := s.Client.RemoveObject(ctx, s.Bucket, key, minio.RemoveObjectOptions{})
	if err != nil && !notFound(err) {
		return fmt.Errorf("r2: remove: %w", err)
	}
	return nil
}

// errNoClient is returned by New for a region without a bucket.
var errNoClient = errors.New("r2: region has no object storage")

// New returns the storage of a bucket; both must be set.
func New(client *minio.Client, bucket string) (*Storage, error) {
	if client == nil || bucket == "" {
		return nil, errNoClient
	}
	return &Storage{Client: client, Bucket: bucket}, nil
}
