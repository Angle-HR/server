package auth

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestVerificationStore_CanResendByEmail(t *testing.T) {
	t.Parallel()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewVerificationStore(client)
	ctx := context.Background()
	email := "user@example.com"

	ok, err := store.CanResendByEmail(ctx, email)
	if err != nil {
		t.Fatalf("CanResendByEmail: %v", err)
	}
	if !ok {
		t.Fatal("expected resend allowed before marker is set")
	}

	if err := store.MarkResentByEmail(ctx, email); err != nil {
		t.Fatalf("MarkResentByEmail: %v", err)
	}

	ok, err = store.CanResendByEmail(ctx, email)
	if err != nil {
		t.Fatalf("CanResendByEmail after mark: %v", err)
	}
	if ok {
		t.Fatal("expected resend blocked while cooldown marker exists")
	}
}
