package admin

import (
	"testing"

	"github.com/Angle-HR/server/pkg/apperror"
)

func TestRoleSuperadminConstant(t *testing.T) {
	if RoleSuperadmin != "superadmin" {
		t.Fatalf("RoleSuperadmin = %q", RoleSuperadmin)
	}
}

func TestInviteErrors(t *testing.T) {
	expired := apperror.New(apperror.CodeGone, "invite expired")
	accepted := apperror.New(apperror.CodeGone, "invite already accepted")
	if apperror.HTTPStatus(expired) != 410 || apperror.HTTPStatus(accepted) != 410 {
		t.Fatal("invite terminal states should map to 410 Gone")
	}
	conflict := apperror.New(apperror.CodeConflict, "cannot remove or deactivate the last active superadmin")
	if apperror.HTTPStatus(conflict) != 409 {
		t.Fatal("last-superadmin guard should map to 409")
	}
}
