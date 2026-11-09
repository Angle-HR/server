package auth

import (
	"context"
	"testing"
	"time"

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
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Logf("close redis client: %v", closeErr)
		}
	})

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

	if markResentByEmailErr := store.MarkResentByEmail(ctx, email); markResentByEmailErr != nil {
		t.Fatalf("MarkResentByEmail: %v", markResentByEmailErr)
	}

	ok, err = store.CanResendByEmail(ctx, email)
	if err != nil {
		t.Fatalf("CanResendByEmail after mark: %v", err)
	}
	if ok {
		t.Fatal("expected resend blocked while cooldown marker exists")
	}
}

func newTestStore(t *testing.T) (*VerificationStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewVerificationStore(client), mr
}

func TestVerificationStore_RetryAfter(t *testing.T) {
	t.Parallel()
	store, mr := newTestStore(t)
	ctx := context.Background()

	if n, err := store.RetryAfterByEmail(ctx, "a@example.com"); err != nil || n != 0 {
		t.Fatalf("no cooldown: got %d, %v", n, err)
	}
	if err := store.MarkResentByEmail(ctx, "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.RetryAfterByEmail(ctx, "a@example.com"); n != resendCooldownSeconds {
		t.Fatalf("got %d want %d", n, resendCooldownSeconds)
	}
	mr.FastForward(10*time.Second + 500*time.Millisecond)
	if n, _ := store.RetryAfterByEmail(ctx, "a@example.com"); n < 19 || n > 20 {
		t.Fatalf("after 10.5s got %d want 20", n)
	}
	mr.FastForward(time.Minute)
	if n, _ := store.RetryAfterByEmail(ctx, "a@example.com"); n != 0 {
		t.Fatalf("after expiry got %d want 0", n)
	}
}

func TestVerificationStore_ReplaceLatestSessionInvalidatesPrevious(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	ctx := context.Background()

	for _, s := range []VerificationSession{{SessionID: "s1", Code: "111111"}, {SessionID: "s2", Code: "222222"}} {
		if err := store.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceLatestSession(ctx, PurposeEmailVerify, "a@example.com", s.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ValidateCode(ctx, "s1", "111111"); err == nil {
		t.Fatal("previous session code must no longer validate")
	}
	if id, _ := store.LatestSessionID(ctx, PurposeEmailVerify, "a@example.com"); id != "s2" {
		t.Fatalf("latest = %q want s2", id)
	}
	if _, err := store.ValidateCode(ctx, "s2", "222222"); err != nil {
		t.Fatalf("latest code should validate: %v", err)
	}
}
