package identitysvc

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/identityevents"
	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
)

// Staff events: with IDENTITY_EVENTS=on the service also consumes its own identity.staff.deactivated.v1 and ends
// every session of the staff member. The PATCH /admin/staff/{id} handler revokes inline too; this consumer repeats
// it until it succeeds, so a Redis outage at the moment of the PATCH cannot leave a deactivated member signed in.
// Consumer and inbox name: identity-staff.
const (
	StaffConsumer     = "identity-staff"
	StaffSubjectMatch = "identity.staff.deactivated.>"
)

// SessionRevoker ends all sessions of a user (session.Manager).
type SessionRevoker interface {
	RevokeAll(ctx context.Context, userID uuid.UUID) error
}

// StaffDeactivation revokes the admin sessions of a deactivated staff member.
type StaffDeactivation struct {
	DB       inbox.DB
	Sessions SessionRevoker
}

// Handle is the events.Handler.
func (s *StaffDeactivation) Handle(ctx context.Context, e events.Event) error {
	if e.Type != identityevents.TypeStaffDeactivated {
		return events.Terminal(fmt.Errorf("identity-staff: unexpected event type %q", e.Type))
	}
	var p identityevents.StaffDeactivated
	if err := e.DecodeJSON(&p); err != nil {
		return events.Terminal(fmt.Errorf("identity-staff: decode %s: %w", e.ID, err))
	}
	id, err := uuid.Parse(p.StaffID)
	if err != nil {
		return events.Terminal(fmt.Errorf("identity-staff: %s: staff id %q is not a uuid", e.ID, p.StaffID))
	}
	return inbox.Handle(s.DB, StaffConsumer, func(ctx context.Context, _ pgx.Tx, _ events.Event) error {
		if err := s.Sessions.RevokeAll(ctx, id); err != nil {
			return fmt.Errorf("identity-staff: revoke sessions of %s: %w", id, err)
		}
		return nil
	})(ctx, e)
}

// SubscribeStaffEvents starts the consumer. Stop the returned subscription on shutdown.
func SubscribeStaffEvents(ctx context.Context, bus events.Subscriber, h *StaffDeactivation, opts ...events.SubscribeOption) (events.Subscription, error) {
	return bus.Subscribe(ctx, StaffConsumer, []string{StaffSubjectMatch}, h.Handle, opts...)
}
