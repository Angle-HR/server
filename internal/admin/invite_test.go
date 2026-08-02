package admin

import (
	"testing"
	"time"

	"github.com/Angle-HR/server/pkg/apperror"
)

func TestInviteTokenHashStable(t *testing.T) {
	raw, err := NewInviteToken()
	if err != nil {
		t.Fatalf("NewInviteToken: %v", err)
	}
	if raw == "" {
		t.Fatal("expected non-empty token")
	}
	h1 := HashInviteToken(raw)
	h2 := HashInviteToken(raw)
	if h1 != h2 {
		t.Fatalf("hash not stable: %s vs %s", h1, h2)
	}
	if h1 == raw {
		t.Fatal("hash should not equal raw token")
	}
	if HashInviteToken("other") == h1 {
		t.Fatal("different tokens must not collide")
	}
}

func TestInviteTTL(t *testing.T) {
	if InviteTTL != 72*time.Hour {
		t.Fatalf("InviteTTL = %v, want 72h", InviteTTL)
	}
}

func TestGoneCode(t *testing.T) {
	err := apperror.New(apperror.CodeGone, "invite expired")
	if apperror.HTTPStatus(err) != 410 {
		t.Fatalf("status = %d, want 410", apperror.HTTPStatus(err))
	}
}
