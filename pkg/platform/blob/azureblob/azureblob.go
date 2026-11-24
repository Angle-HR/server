// Package azureblob is the BlobStore adapter for Azure Blob Storage. Presigned URLs are SAS tokens: signed with the
// account key when one is configured, otherwise with a user delegation key obtained through the managed identity
// (DefaultAzureCredential).
package azureblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	azb "github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"

	"github.com/Angle-HR/server/pkg/platform/blob"
)

// delegationSkew backdates SAS start times so clock drift does not reject a fresh token.
const delegationSkew = 5 * time.Minute

// Config selects a container.
type Config struct {
	// AccountName is the storage account.
	AccountName string
	// AccountKey is the shared key. Empty means managed identity.
	AccountKey string
	// Container is the blob container, the counterpart of an S3 bucket.
	Container string
	// Endpoint is the service URL. Empty means https://<account>.blob.core.windows.net. Azurite uses
	// http://127.0.0.1:10000/devstoreaccount1.
	Endpoint string
}

// Store is one container. It implements blob.BlobStore.
type Store struct {
	svc       *service.Client
	cred      *azblob.SharedKeyCredential // nil with managed identity
	account   string
	container string
}

var _ blob.BlobStore = (*Store)(nil)

// New returns the store of cfg.Container.
func New(cfg Config) (*Store, error) {
	if cfg.AccountName == "" || cfg.Container == "" {
		return nil, errors.New("azureblob: account name and container are required")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://" + cfg.AccountName + ".blob.core.windows.net"
	}
	s := &Store{account: cfg.AccountName, container: cfg.Container}
	var err error
	if cfg.AccountKey != "" {
		s.cred, err = azblob.NewSharedKeyCredential(cfg.AccountName, cfg.AccountKey)
		if err != nil {
			return nil, fmt.Errorf("azureblob: credential: %w", err)
		}
		s.svc, err = service.NewClientWithSharedKeyCredential(endpoint, s.cred, nil)
	} else {
		var tc *azidentity.DefaultAzureCredential
		tc, err = azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, fmt.Errorf("azureblob: identity: %w", err)
		}
		s.svc, err = service.NewClient(endpoint, tc, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("azureblob: client: %w", err)
	}
	return s, nil
}

// EnsureContainer creates the container when it does not exist. For local stacks and tests.
func (s *Store) EnsureContainer(ctx context.Context) error {
	_, err := s.svc.NewContainerClient(s.container).Create(ctx, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
		return fmt.Errorf("azureblob: create container: %w", err)
	}
	return nil
}

func (s *Store) blobClient(key string) *azb.Client {
	return s.svc.NewContainerClient(s.container).NewBlobClient(key)
}

func notFound(err error) bool {
	return bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ResourceNotFound, bloberror.ContainerNotFound)
}

// Put writes an object.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, _ int64, contentType string) error {
	bb := s.svc.NewContainerClient(s.container).NewBlockBlobClient(key)
	_, err := bb.UploadStream(ctx, r, &azblob.UploadStreamOptions{
		HTTPHeaders: &azb.HTTPHeaders{BlobContentType: &contentType},
	})
	if err != nil {
		return fmt.Errorf("azureblob: put: %w", err)
	}
	return nil
}

// Open reads an object.
func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := s.blobClient(key).DownloadStream(ctx, nil)
	if err != nil {
		if notFound(err) {
			return nil, blob.ErrNotFound
		}
		return nil, fmt.Errorf("azureblob: get: %w", err)
	}
	return resp.Body, nil
}

// Stat returns the size of an object.
func (s *Store) Stat(ctx context.Context, key string) (int64, error) {
	p, err := s.blobClient(key).GetProperties(ctx, nil)
	if err != nil {
		if notFound(err) {
			return 0, blob.ErrNotFound
		}
		return 0, fmt.Errorf("azureblob: stat: %w", err)
	}
	if p.ContentLength == nil {
		return 0, nil
	}
	return *p.ContentLength, nil
}

// Exists reports whether an object exists.
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Stat(ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Delete removes an object; a missing object is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.blobClient(key).Delete(ctx, nil)
	if err != nil && !notFound(err) {
		return fmt.Errorf("azureblob: delete: %w", err)
	}
	return nil
}

// sign returns the query string of a SAS for one blob.
func (s *Store) sign(ctx context.Context, v sas.BlobSignatureValues) (string, error) {
	now := time.Now().UTC()
	v.Protocol = sas.ProtocolHTTPSandHTTP
	v.StartTime = now.Add(-delegationSkew)
	v.ContainerName = s.container
	if s.cred != nil {
		q, err := v.SignWithSharedKey(s.cred)
		if err != nil {
			return "", err
		}
		return q.Encode(), nil
	}
	start, expiry := v.StartTime, v.ExpiryTime
	udk, err := s.svc.GetUserDelegationCredential(ctx, service.KeyInfo{
		Start:  ptr(start.Format(sas.TimeFormat)),
		Expiry: ptr(expiry.Format(sas.TimeFormat)),
	}, nil)
	if err != nil {
		return "", err
	}
	q, err := v.SignWithUserDelegation(udk)
	if err != nil {
		return "", err
	}
	return q.Encode(), nil
}

func ptr[T any](v T) *T { return &v }

// blobURL is the unsigned URL of key.
func (s *Store) blobURL(key string) string { return s.blobClient(key).URL() }

// PresignPut returns a SAS URL with create and write permission. The uploader must send the returned headers:
// Azure requires x-ms-blob-type, and the Content-Type header becomes the blob's content type. A SAS cannot cap the
// size, so SizeEnforced is false.
func (s *Store) PresignPut(ctx context.Context, key string, opts blob.PutOptions) (blob.PutTarget, error) {
	q, err := s.sign(ctx, sas.BlobSignatureValues{
		BlobName:    key,
		Permissions: (&sas.BlobPermissions{Create: true, Write: true}).String(),
		ExpiryTime:  time.Now().UTC().Add(opts.TTL),
	})
	if err != nil {
		return blob.PutTarget{}, fmt.Errorf("azureblob: presign: %w", err)
	}
	return blob.PutTarget{
		URL:    s.blobURL(key) + "?" + q,
		Method: http.MethodPut,
		Headers: map[string]string{
			"Content-Type":   opts.ContentType,
			"x-ms-blob-type": "BlockBlob",
		},
	}, nil
}

// PresignGet returns a read-only SAS URL that expires after opts.TTL.
func (s *Store) PresignGet(ctx context.Context, key string, opts blob.GetOptions) (string, error) {
	v := sas.BlobSignatureValues{
		BlobName:    key,
		Permissions: (&sas.BlobPermissions{Read: true}).String(),
		ExpiryTime:  time.Now().UTC().Add(opts.TTL),
		ContentType: opts.ContentType,
	}
	if opts.Filename != "" {
		v.ContentDisposition = `attachment; filename="` + strings.NewReplacer(`"`, "", "\\", "").Replace(opts.Filename) + `"`
	}
	q, err := s.sign(ctx, v)
	if err != nil {
		return "", fmt.Errorf("azureblob: presign get: %w", err)
	}
	return s.blobURL(key) + "?" + q, nil
}
