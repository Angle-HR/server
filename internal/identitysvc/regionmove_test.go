package identitysvc

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/Angle-HR/server/gen/openhr/identity/v1"
	"github.com/Angle-HR/server/internal/identityevents"
	"github.com/Angle-HR/server/internal/regionsaga"
	"github.com/Angle-HR/server/pkg/events"
)

func TestRegionMoveArgsValidation(t *testing.T) {
	r := &RPC{}
	cases := []struct{ user, target string }{{"nope", "eu"}, {uuid.NewString(), "global"}, {uuid.NewString(), "mars"}, {uuid.NewString(), ""}}
	for _, c := range cases {
		_, err := r.PrepareRegionMove(context.Background(), &identityv1.PrepareRegionMoveRequest{UserId: c.user, TargetRegion: c.target})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%+v: %v", c, err)
		}
		_, err = r.CommitRegionMove(context.Background(), &identityv1.CommitRegionMoveRequest{UserId: c.user, TargetRegion: c.target})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%+v: %v", c, err)
		}
		_, err = r.AbortRegionMove(context.Background(), &identityv1.AbortRegionMoveRequest{UserId: c.user, TargetRegion: c.target})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%+v: %v", c, err)
		}
	}
}

func TestRegionMoveErrorMapping(t *testing.T) {
	if got := status.Code(regionMoveError("x", regionsaga.Permanent{Err: regionsaga.ErrNotPending})); got != codes.FailedPrecondition {
		t.Fatal(got)
	}
	if got := status.Code(regionMoveError("x", errors.New("db down"))); got != codes.Unavailable {
		t.Fatal(got)
	}
}

func TestStaffDeactivationRejectsBadEventsTerminally(t *testing.T) {
	h := &StaffDeactivation{}
	wrong := events.Event{Type: "identity.user.deactivated.v1"}
	if err := h.Handle(context.Background(), wrong); !events.IsTerminal(err) {
		t.Fatalf("wrong type must dead-letter: %v", err)
	}
	ev, _ := identityevents.StaffDeactivatedEvent(identityevents.StaffDeactivated{StaffID: "not-a-uuid"})
	if err := h.Handle(context.Background(), ev); !events.IsTerminal(err) {
		t.Fatalf("bad id must dead-letter: %v", err)
	}
}
