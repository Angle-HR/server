package auth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/region"
)

func TestRevocationStore_JTIAndUser(t *testing.T) {
	t.Parallel()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewRevocationStore(client)
	ctx := context.Background()
	jti := "jti-1"
	expires := time.Now().Add(time.Hour)

	if err := store.RevokeJTI(ctx, jti, expires); err != nil {
		t.Fatalf("RevokeJTI: %v", err)
	}
	revoked, err := store.IsJTIRevoked(ctx, jti)
	if err != nil || !revoked {
		t.Fatalf("IsJTIRevoked = %v, %v", revoked, err)
	}

	userID := uuid.New()
	tokens, err := NewTokenService("secret", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tokens.IssuePair(userID, region.RegionUK, true)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tokens.ParseRefresh(pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := store.IsRefreshValid(ctx, claims)
	if err != nil || !ok {
		t.Fatalf("IsRefreshValid before revoke = %v, %v", ok, err)
	}
	if err := store.RevokeUserSessions(ctx, userID, time.Hour); err != nil {
		t.Fatalf("RevokeUserSessions: %v", err)
	}
	ok, err = store.IsRefreshValid(ctx, claims)
	if err != nil || ok {
		t.Fatalf("IsRefreshValid after user revoke = %v, %v want false", ok, err)
	}
}

func TestTOTPCryptoRoundTrip(t *testing.T) {
	t.Parallel()

	crypto, err := NewTOTPCrypto("test-key")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.Encrypt("SECRET123")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := crypto.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "SECRET123" {
		t.Fatalf("got %q", plain)
	}
}

func TestIssuePairIncludesJTI(t *testing.T) {
	t.Parallel()

	tokens, err := NewTokenService("secret", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tokens.IssuePair(uuid.New(), region.RegionUK, true)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tokens.ParseRefresh(pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ID == "" {
		t.Fatal("expected refresh jti")
	}
}
