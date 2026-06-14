package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/region"
)

func TestHashAndCheckPassword(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("secure-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !CheckPassword(hash, "secure-password") {
		t.Fatal("expected password to match hash")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Fatal("expected wrong password to fail")
	}
}

func TestTokenServiceIssueAndParse(t *testing.T) {
	t.Parallel()

	svc, err := NewTokenService("test-secret-key", time.Hour, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("NewTokenService: %v", err)
	}

	userID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	pair, err := svc.IssuePair(userID, region.RegionUK, true)
	if err != nil {
		t.Fatalf("IssuePair: %v", err)
	}

	claims, err := svc.ParseAccess(pair.AccessToken)
	if err != nil {
		t.Fatalf("ParseAccess: %v", err)
	}
	if claims.Subject != userID.String() {
		t.Fatalf("subject: got %q", claims.Subject)
	}
	if claims.Region != string(region.RegionUK) {
		t.Fatalf("region: got %q", claims.Region)
	}
}
