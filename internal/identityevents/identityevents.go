// Package identityevents is the contract of the events the identity service publishes:
//
//	identity.user.registered.v1      a product account was created (signup), still in the global holding area
//	identity.user.email_verified.v1  the account verified its email (the user now has a home region row)
//	identity.user.deactivated.v1     an operator deactivated the account (reactivation is not an event yet)
//	identity.staff.invited.v1        an operator invited a platform staff member
//	identity.staff.deactivated.v1    an operator deactivated a platform staff member (reactivation is not an event)
//
// Events go to the transactional outbox of the database that holds the change, in the same transaction, with the
// user (or staff) id as partition key. Payloads carry ids, region and timestamps only: never an email, a name or a
// credential. Emission is off unless IDENTITY_EVENTS=on (see Enabled).
package identityevents

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// Source is the producing service written to every event.
const Source = "/services/identity"

// Event types.
const (
	TypeUserRegistered     = "identity.user.registered.v1"
	TypeUserEmailVerified  = "identity.user.email_verified.v1"
	TypeUserDeactivated    = "identity.user.deactivated.v1"
	TypeStaffInvited       = "identity.staff.invited.v1"
	TypeStaffDeactivated   = "identity.staff.deactivated.v1"
	regionGlobalHolding    = "global"
	envVar                 = "IDENTITY_EVENTS"
	idNamespaceDescription = "openhr/identity/events"
)

// EnvVar switches emission.
const EnvVar = envVar

// Enabled is the single gate for emitting identity events. Anything but "on" (case-insensitive) writes nothing.
func Enabled() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(envVar)), "on") }

// UserRegistered is the data of identity.user.registered.v1.
type UserRegistered struct {
	UserID       string    `json:"user_id"`
	Region       string    `json:"region"`
	RegisteredAt time.Time `json:"registered_at"`
}

// UserEmailVerified is the data of identity.user.email_verified.v1.
type UserEmailVerified struct {
	UserID     string    `json:"user_id"`
	Region     string    `json:"region"`
	VerifiedAt time.Time `json:"verified_at"`
}

// UserDeactivated is the data of identity.user.deactivated.v1.
type UserDeactivated struct {
	UserID        string    `json:"user_id"`
	Region        string    `json:"region"`
	ByAdminID     string    `json:"by_admin_id,omitempty"`
	DeactivatedAt time.Time `json:"deactivated_at"`
}

// StaffInvited is the data of identity.staff.invited.v1.
type StaffInvited struct {
	StaffID   string    `json:"staff_id"`
	InvitedBy string    `json:"invited_by,omitempty"`
	Region    string    `json:"region"` // always "global": staff live in the global database
	InvitedAt time.Time `json:"invited_at"`
}

// StaffDeactivated is the data of identity.staff.deactivated.v1.
type StaffDeactivated struct {
	StaffID       string    `json:"staff_id"`
	Region        string    `json:"region"` // always "global": staff live in the global database
	DeactivatedAt time.Time `json:"deactivated_at"`
}

var idSpace = uuid.NewSHA1(uuid.NameSpaceURL, []byte(idNamespaceDescription))

func build(typ, subject, region, key string, at time.Time, payload any) (events.Event, error) {
	ev, err := events.NewJSON(Source, typ, payload)
	if err != nil {
		return ev, err
	}
	ev.ID = uuid.NewSHA1(idSpace, []byte(typ+"/"+key)).String()
	ev.Time = at.UTC()
	return ev.WithSubject(subject).WithRegion(region).WithPartitionKey(subject), nil
}

// RegisteredEvent builds identity.user.registered.v1. The id derives from the user, so enqueueing twice is a no-op.
func RegisteredEvent(p UserRegistered) (events.Event, error) {
	return build(TypeUserRegistered, p.UserID, p.Region, p.UserID, p.RegisteredAt, p)
}

// EmailVerifiedEvent builds identity.user.email_verified.v1.
func EmailVerifiedEvent(p UserEmailVerified) (events.Event, error) {
	return build(TypeUserEmailVerified, p.UserID, p.Region, p.UserID, p.VerifiedAt, p)
}

// DeactivatedEvent builds identity.user.deactivated.v1. The id includes the time so a later deactivation after a
// reactivation is a new event.
func DeactivatedEvent(p UserDeactivated) (events.Event, error) {
	return build(TypeUserDeactivated, p.UserID, p.Region, p.UserID+"/"+p.DeactivatedAt.UTC().Format(time.RFC3339Nano),
		p.DeactivatedAt, p)
}

// StaffInvitedEvent builds identity.staff.invited.v1. Resending an invite does not emit again.
func StaffInvitedEvent(p StaffInvited) (events.Event, error) {
	if p.Region == "" {
		p.Region = regionGlobalHolding
	}
	return build(TypeStaffInvited, p.StaffID, p.Region, p.StaffID, p.InvitedAt, p)
}

// StaffDeactivatedEvent builds identity.staff.deactivated.v1. The id includes the time so a deactivation after a
// reactivation is a new event.
func StaffDeactivatedEvent(p StaffDeactivated) (events.Event, error) {
	if p.Region == "" {
		p.Region = regionGlobalHolding
	}
	return build(TypeStaffDeactivated, p.StaffID, p.Region, p.StaffID+"/"+p.DeactivatedAt.UTC().Format(time.RFC3339Nano),
		p.DeactivatedAt, p)
}

func enqueue(ctx context.Context, tx outbox.Execer, ev events.Event, err error) error {
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, ev)
}

// EnqueueRegistered writes the event into tx's outbox. It does nothing unless Enabled. An error must abort tx.
func EnqueueRegistered(ctx context.Context, tx outbox.Execer, p UserRegistered) error {
	if !Enabled() {
		return nil
	}
	ev, err := RegisteredEvent(p)
	return enqueue(ctx, tx, ev, err)
}

// EnqueueEmailVerified writes the event into tx's outbox. It does nothing unless Enabled.
func EnqueueEmailVerified(ctx context.Context, tx outbox.Execer, p UserEmailVerified) error {
	if !Enabled() {
		return nil
	}
	ev, err := EmailVerifiedEvent(p)
	return enqueue(ctx, tx, ev, err)
}

// EnqueueDeactivated writes the event into tx's outbox. It does nothing unless Enabled.
func EnqueueDeactivated(ctx context.Context, tx outbox.Execer, p UserDeactivated) error {
	if !Enabled() {
		return nil
	}
	ev, err := DeactivatedEvent(p)
	return enqueue(ctx, tx, ev, err)
}

// EnqueueStaffInvited writes the event into tx's outbox. It does nothing unless Enabled.
func EnqueueStaffInvited(ctx context.Context, tx outbox.Execer, p StaffInvited) error {
	if !Enabled() {
		return nil
	}
	ev, err := StaffInvitedEvent(p)
	return enqueue(ctx, tx, ev, err)
}

// EnqueueStaffDeactivated writes the event into tx's outbox. It does nothing unless Enabled.
func EnqueueStaffDeactivated(ctx context.Context, tx outbox.Execer, p StaffDeactivated) error {
	if !Enabled() {
		return nil
	}
	ev, err := StaffDeactivatedEvent(p)
	return enqueue(ctx, tx, ev, err)
}
