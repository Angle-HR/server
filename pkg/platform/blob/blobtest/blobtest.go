// Package blobtest is the conformance suite every blob.BlobStore adapter must pass.
package blobtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Angle-HR/server/pkg/platform/blob"
)

// Factory returns an empty, ready store. Each call may share the backing bucket; keys are unique per test.
type Factory func(t *testing.T) blob.BlobStore

const ttl = time.Minute

// Run executes the suite. Presigned URLs with the memory: scheme are only checked for shape; real URLs are used
// over HTTP.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, s blob.BlobStore, key string)
	}{
		{"PutOpenStat", putOpenStat},
		{"PutOverwrites", putOverwrites},
		{"MissingObject", missingObject},
		{"DeleteIsIdempotent", deleteIdempotent},
		{"ExistsTracksLifecycle", existsLifecycle},
		{"EmptyObject", emptyObject},
		{"PresignPutThenGet", presignRoundTrip},
		{"PresignGetOverrides", presignGetOverrides},
		{"PresignExpires", presignExpires},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := factory(t)
			c.fn(t, s, fmt.Sprintf("conformance/%s/%d/file name.bin", c.name, time.Now().UnixNano()))
		})
	}
}

func read(t *testing.T, s blob.BlobStore, key string) []byte {
	t.Helper()
	rc, err := s.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return b
}

func putOpenStat(t *testing.T, s blob.BlobStore, key string) {
	ctx := context.Background()
	data := []byte("%PDF-1.4 hello")
	if err := s.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "application/pdf"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := read(t, s, key); !bytes.Equal(got, data) {
		t.Fatalf("open returned %q", got)
	}
	size, err := s.Stat(ctx, key)
	if err != nil || size != int64(len(data)) {
		t.Fatalf("stat = %d, %v", size, err)
	}
}

func putOverwrites(t *testing.T, s blob.BlobStore, key string) {
	ctx := context.Background()
	for _, v := range []string{"first version", "v2"} {
		if err := s.Put(ctx, key, strings.NewReader(v), int64(len(v)), "text/plain"); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if got := read(t, s, key); string(got) != "v2" {
		t.Fatalf("got %q", got)
	}
}

func missingObject(t *testing.T, s blob.BlobStore, key string) {
	ctx := context.Background()
	if _, err := s.Stat(ctx, key); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("stat: %v", err)
	}
	if rc, err := s.Open(ctx, key); !errors.Is(err, blob.ErrNotFound) {
		if rc != nil {
			_ = rc.Close()
		}
		t.Fatalf("open: %v", err)
	}
}

func deleteIdempotent(t *testing.T, s blob.BlobStore, key string) {
	ctx := context.Background()
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if err := s.Put(ctx, key, strings.NewReader("x"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Stat(ctx, key); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("stat after delete: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func existsLifecycle(t *testing.T, s blob.BlobStore, key string) {
	ctx := context.Background()
	check := func(want bool) {
		t.Helper()
		got, err := s.Exists(ctx, key)
		if err != nil || got != want {
			t.Fatalf("exists = %v, %v; want %v", got, err, want)
		}
	}
	check(false)
	if err := s.Put(ctx, key, strings.NewReader("x"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	check(true)
	_ = s.Delete(ctx, key)
	check(false)
}

func emptyObject(t *testing.T, s blob.BlobStore, key string) {
	ctx := context.Background()
	if err := s.Put(ctx, key, bytes.NewReader(nil), 0, "application/octet-stream"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if size, err := s.Stat(ctx, key); err != nil || size != 0 {
		t.Fatalf("stat = %d, %v", size, err)
	}
}

func fetchable(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func do(t *testing.T, method, url string, headers map[string]string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return resp
}

func presignRoundTrip(t *testing.T, s blob.BlobStore, key string) {
	ctx := context.Background()
	target, err := s.PresignPut(ctx, key, blob.PutOptions{ContentType: "application/pdf", MaxBytes: 1 << 20, TTL: ttl})
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	if target.URL == "" || target.Method == "" {
		t.Fatalf("incomplete target %+v", target)
	}
	if !fetchable(target.URL) {
		return
	}
	data := []byte("uploaded through a presigned url")
	resp := do(t, target.Method, target.URL, target.Headers, data)
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("upload status %d", resp.StatusCode)
	}
	if size, err := s.Stat(ctx, key); err != nil || size != int64(len(data)) {
		t.Fatalf("stat after upload = %d, %v", size, err)
	}
	u, err := s.PresignGet(ctx, key, blob.GetOptions{TTL: ttl})
	if err != nil {
		t.Fatalf("presign get: %v", err)
	}
	resp = do(t, http.MethodGet, u, nil, nil)
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, data) {
		t.Fatalf("download status %d body %q", resp.StatusCode, got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("stored content type %q", ct)
	}
}

func presignGetOverrides(t *testing.T, s blob.BlobStore, key string) {
	ctx := context.Background()
	if err := s.Put(ctx, key, strings.NewReader("abc"), 3, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	u, err := s.PresignGet(ctx, key, blob.GetOptions{TTL: ttl, Filename: "cv.pdf", ContentType: "application/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if !fetchable(u) {
		return
	}
	resp := do(t, http.MethodGet, u, nil, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "cv.pdf") {
		t.Fatalf("content-disposition %q", cd)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content-type %q", ct)
	}
}

func presignExpires(t *testing.T, s blob.BlobStore, key string) {
	ctx := context.Background()
	if err := s.Put(ctx, key, strings.NewReader("abc"), 3, "text/plain"); err != nil {
		t.Fatal(err)
	}
	u, err := s.PresignGet(ctx, key, blob.GetOptions{TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !fetchable(u) {
		return
	}
	time.Sleep(3 * time.Second)
	resp := do(t, http.MethodGet, u, nil, nil)
	_ = resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		t.Fatalf("expired url still works: %d", resp.StatusCode)
	}
}
