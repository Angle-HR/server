// Package memory is an in-process BlobStore for tests. Presigned URLs use the memory: scheme and are not fetchable.
package memory

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/Angle-HR/server/pkg/platform/blob"
)

// Store keeps objects in a map.
type Store struct {
	mu      sync.Mutex
	objects map[string][]byte
}

var _ blob.BlobStore = (*Store)(nil)

// New returns an empty store.
func New() *Store { return &Store{objects: map[string][]byte{}} }

// Put writes an object.
func (s *Store) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	return nil
}

// Open reads an object.
func (s *Store) Open(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Stat returns the size of an object.
func (s *Store) Stat(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return 0, blob.ErrNotFound
	}
	return int64(len(data)), nil
}

// Exists reports whether an object exists.
func (s *Store) Exists(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok, nil
}

// Delete removes an object.
func (s *Store) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

// PresignPut returns a memory: URL.
func (s *Store) PresignPut(_ context.Context, key string, opts blob.PutOptions) (blob.PutTarget, error) {
	return blob.PutTarget{
		URL: "memory:put/" + url.PathEscape(key), Method: http.MethodPut,
		Headers: map[string]string{"Content-Type": opts.ContentType},
	}, nil
}

// PresignGet returns a memory: URL.
func (s *Store) PresignGet(_ context.Context, key string, _ blob.GetOptions) (string, error) {
	return "memory:get/" + url.PathEscape(key), nil
}
