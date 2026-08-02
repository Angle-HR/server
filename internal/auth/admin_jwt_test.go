package auth_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/region"
)

func TestIssueAndParseAdminPair(t *testing.T) {
	t.Parallel()

	tokens, err := auth.NewTokenService("test-secret", time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	id := uuid.New()
	pair, err := tokens.IssueAdminPair(id)
	if err != nil {
		t.Fatal(err)
	}

	claims, err := tokens.ParseAdminAccess(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	gotID, err := auth.AdminUserIDFromAccess(claims)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != id {
		t.Fatalf("subject = %s, want %s", gotID, id)
	}

	// Product access tokens must not parse as admin.
	product, err := tokens.IssuePair(id, region.RegionUK, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.ParseAdminAccess(product.AccessToken); err == nil {
		t.Fatal("expected product token to fail admin parse")
	}

	refresh, err := tokens.ParseAdminRefresh(pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refresh.Subject != id.String() {
		t.Fatalf("refresh subject = %s, want %s", refresh.Subject, id)
	}
}

func TestAdminHasPermission(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	ctx := auth.WithAdmin(t.Context(), id, "a@example.com", []string{"waitlist:read", "jobs:write"})
	if !auth.AdminHasPermission(ctx, "waitlist:read") {
		t.Fatal("expected waitlist:read")
	}
	if auth.AdminHasPermission(ctx, "admins:write") {
		t.Fatal("did not expect admins:write")
	}
}
