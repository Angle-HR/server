// Package blob is the object-storage port. Services depend on BlobStore only; the cloud SDKs live in the adapter
// packages (s3compat for S3, Cloudflare R2 and MinIO; azureblob for Azure Blob Storage; memory for tests).
package blob

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned by Stat and Open for a missing object. Delete of a missing object is not an error.
var ErrNotFound = errors.New("blob: object not found")

// PutOptions constrain a presigned upload.
type PutOptions struct {
	// ContentType is the Content-Type the uploader must send.
	ContentType string
	// MaxBytes is the largest accepted object, 0 for no limit. Adapters that cannot enforce it in the signature
	// (S3 and R2 presigned PUT, Azure SAS) report SizeEnforced false in the PutTarget; the caller must then
	// check the size with Stat after the upload.
	MaxBytes int64
	// TTL is how long the URL works.
	TTL time.Duration
}

// PutTarget is where and how a client uploads: one request with Method to URL carrying Headers.
type PutTarget struct {
	URL     string
	Method  string
	Headers map[string]string
	// SizeEnforced reports whether the storage rejects objects larger than PutOptions.MaxBytes by itself.
	SizeEnforced bool
}

// GetOptions shape a presigned download.
type GetOptions struct {
	// TTL is how long the URL works.
	TTL time.Duration
	// Filename, when set, makes the browser save the object under this name (Content-Disposition: attachment).
	Filename string
	// ContentType, when set, overrides the Content-Type of the response.
	ContentType string
}

// BlobStore is one bucket (S3) or container (Azure).
//
//nolint:revive // BlobStore is the name the platform ports contract fixes.
type BlobStore interface {
	// Put writes an object from r. size is the length of r, or -1 when unknown.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Open reads an object. The caller closes it. A missing object is ErrNotFound.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Stat returns the size of an object, or ErrNotFound.
	Stat(ctx context.Context, key string) (int64, error)
	// Exists reports whether an object exists.
	Exists(ctx context.Context, key string) (bool, error)
	// Delete removes an object; removing a missing object is not an error.
	Delete(ctx context.Context, key string) error
	// PresignPut returns a target that accepts one upload of the object until opts.TTL has passed.
	PresignPut(ctx context.Context, key string, opts PutOptions) (PutTarget, error)
	// PresignGet returns a download URL that expires after opts.TTL.
	PresignGet(ctx context.Context, key string, opts GetOptions) (string, error)
}
