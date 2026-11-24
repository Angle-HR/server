// Package orgevents is the contract of the events the org side publishes:
//
//	org.company.verified.v1          KYB reached verified (automatic check or reviewer approval)
//	org.company.kyb_rejected.v1      KYB failed or a reviewer rejected
//	org.agreement.accepted.v1        a company accepted an agreement version
//	org.company.profile_updated.v1   company data other services denormalize changed (the privacy contact)
//	org.company.deleted.v1           a company was deleted
//
// Events are written to the transactional outbox of the company's regional database in the same transaction as the
// change, with the company id as partition key, and relayed to the bus by an outbox relay. Payloads carry ids,
// region and status only: never names, email addresses, the registration number or the failure details. A consumer
// that needs more asks OrgService (GetCompany, GetCompanyContact, GetPublishReadiness).
//
// Emission is off unless ORG_EVENTS=on (see Enabled). The package depends on shared libraries only.
package orgevents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// Source is the producing service written to every event.
const Source = "/services/org"

// Event types. The "org" domain gets its own stream (ORG) on the bus.
const (
	TypeCompanyVerified       = "org.company.verified.v1"
	TypeCompanyKybRejected    = "org.company.kyb_rejected.v1"
	TypeAgreementAccepted     = "org.agreement.accepted.v1"
	TypeCompanyProfileUpdated = "org.company.profile_updated.v1"
	TypeCompanyDeleted        = "org.company.deleted.v1"
)

// EnvVar switches emission. Anything but "on" (case-insensitive) leaves behaviour unchanged and writes nothing.
const EnvVar = "ORG_EVENTS"

// Enabled is the single gate for emitting org events.
func Enabled() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvVar)), "on") }

// CompanyVerified is the data of org.company.verified.v1.
type CompanyVerified struct {
	// CompanyKind is "business" or "individual" (a personal organization).
	CompanyKind string    `json:"company_kind"`
	CompanyID   string    `json:"company_id"`
	Region      string    `json:"region"`
	Status      string    `json:"status"` // verified
	VerifiedAt  time.Time `json:"verified_at"`
}

// CompanyKybRejected is the data of org.company.kyb_rejected.v1. Reason is the failure reason code (for example
// name_mismatch), not free text.
type CompanyKybRejected struct {
	// CompanyKind is "business" or "individual" (a personal organization).
	CompanyKind string    `json:"company_kind"`
	CompanyID   string    `json:"company_id"`
	Region      string    `json:"region"`
	Status      string    `json:"status"` // failed
	Reason      string    `json:"reason"`
	RejectedAt  time.Time `json:"rejected_at"`
}

// AgreementAccepted is the data of org.agreement.accepted.v1.
type AgreementAccepted struct {
	// CompanyKind is "business" or "individual" (a personal organization).
	CompanyKind      string    `json:"company_kind"`
	CompanyID        string    `json:"company_id"`
	Region           string    `json:"region"`
	Kind             string    `json:"kind"`
	Version          string    `json:"version"`
	AcceptedByUserID string    `json:"accepted_by_user_id"`
	AcceptedAt       time.Time `json:"accepted_at"`
}

// CompanyProfileUpdated is the data of org.company.profile_updated.v1. Changed names the fields that changed
// ("privacy_contact"); it carries no values.
type CompanyProfileUpdated struct {
	// CompanyKind is "business" or "individual" (a personal organization).
	CompanyKind string    `json:"company_kind"`
	CompanyID   string    `json:"company_id"`
	Region      string    `json:"region"`
	Changed     []string  `json:"changed"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CompanyDeleted is the data of org.company.deleted.v1.
type CompanyDeleted struct {
	// CompanyKind is "business" or "individual" (a personal organization).
	CompanyKind string    `json:"company_kind"`
	CompanyID   string    `json:"company_id"`
	Region      string    `json:"region"`
	DeletedAt   time.Time `json:"deleted_at"`
	// Reason is free text from the operator, for the audit trail of the consumers.
	Reason string `json:"reason,omitempty"`
}

// Kinds of company.
const (
	KindBusiness   = "business"
	KindIndividual = "individual"
)

// Querier is the part of a transaction KindOf needs.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// KindOf reads the company's kind inside the transaction that writes the event. It returns "" when events are off
// (nothing is written, so nothing is read).
func KindOf(ctx context.Context, q Querier, companyID string) (string, error) {
	if !Enabled() {
		return "", nil
	}
	var kind string
	if err := q.QueryRow(ctx, `SELECT kind FROM accounts.organizations WHERE id = $1::uuid`, companyID).Scan(&kind); err != nil {
		return "", fmt.Errorf("orgevents: company kind: %w", err)
	}
	return kind, nil
}

// ErrNoRegion is returned when events are on but the caller has no region to stamp them with.
var ErrNoRegion = errors.New("orgevents: events are on but the store has no region")

var idSpace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("openhr/org/events"))

func build(typ, key, companyID, region string, at time.Time, payload any) (events.Event, error) {
	ev, err := events.NewJSON(Source, typ, payload)
	if err != nil {
		return ev, err
	}
	ev.ID = uuid.NewSHA1(idSpace, []byte(typ+"/"+key)).String()
	ev.Time = at.UTC()
	return ev.WithSubject(companyID).WithCompany(companyID).WithRegion(region).WithPartitionKey(companyID), nil
}

// VerifiedEvent builds the event. Its id derives from the company and the verification time, so enqueueing it twice
// is a no-op.
func VerifiedEvent(p CompanyVerified) (events.Event, error) {
	return build(TypeCompanyVerified, p.CompanyID+"/"+p.VerifiedAt.UTC().Format(time.RFC3339Nano), p.CompanyID, p.Region, p.VerifiedAt, p)
}

// KybRejectedEvent builds the event. Its id derives from the company, the reason and the time.
func KybRejectedEvent(p CompanyKybRejected) (events.Event, error) {
	key := strings.Join([]string{p.CompanyID, p.Reason, p.RejectedAt.UTC().Format(time.RFC3339Nano)}, "/")
	return build(TypeCompanyKybRejected, key, p.CompanyID, p.Region, p.RejectedAt, p)
}

// AgreementAcceptedEvent builds the event. Its id derives from the company, kind and version: an agreement version
// is accepted once.
func AgreementAcceptedEvent(p AgreementAccepted) (events.Event, error) {
	return build(TypeAgreementAccepted, strings.Join([]string{p.CompanyID, p.Kind, p.Version}, "/"), p.CompanyID, p.Region, p.AcceptedAt, p)
}

// ProfileUpdatedEvent builds the event. Its id derives from the company and the change time.
func ProfileUpdatedEvent(p CompanyProfileUpdated) (events.Event, error) {
	return build(TypeCompanyProfileUpdated, p.CompanyID+"/"+p.UpdatedAt.UTC().Format(time.RFC3339Nano), p.CompanyID, p.Region, p.UpdatedAt, p)
}

// DeletedEvent builds the event. Its id derives from the company: a company is deleted once.
func DeletedEvent(p CompanyDeleted) (events.Event, error) {
	return build(TypeCompanyDeleted, p.CompanyID, p.CompanyID, p.Region, p.DeletedAt, p)
}

func enqueue(ctx context.Context, tx outbox.Execer, region string, build func() (events.Event, error)) error {
	if !Enabled() {
		return nil
	}
	if region == "" {
		return ErrNoRegion
	}
	ev, err := build()
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, ev)
}

// EnqueueVerified writes org.company.verified.v1 into the outbox using the caller's transaction. It does nothing
// unless Enabled. An error must abort the transaction (fail closed).
func EnqueueVerified(ctx context.Context, tx outbox.Execer, p CompanyVerified) error {
	return enqueue(ctx, tx, p.Region, func() (events.Event, error) { return VerifiedEvent(p) })
}

// EnqueueKybRejected writes org.company.kyb_rejected.v1; see EnqueueVerified.
func EnqueueKybRejected(ctx context.Context, tx outbox.Execer, p CompanyKybRejected) error {
	return enqueue(ctx, tx, p.Region, func() (events.Event, error) { return KybRejectedEvent(p) })
}

// EnqueueAgreementAccepted writes org.agreement.accepted.v1; see EnqueueVerified.
func EnqueueAgreementAccepted(ctx context.Context, tx outbox.Execer, p AgreementAccepted) error {
	return enqueue(ctx, tx, p.Region, func() (events.Event, error) { return AgreementAcceptedEvent(p) })
}

// EnqueueProfileUpdated writes org.company.profile_updated.v1; see EnqueueVerified.
func EnqueueProfileUpdated(ctx context.Context, tx outbox.Execer, p CompanyProfileUpdated) error {
	return enqueue(ctx, tx, p.Region, func() (events.Event, error) { return ProfileUpdatedEvent(p) })
}

// EnqueueDeleted writes org.company.deleted.v1; see EnqueueVerified. Nothing in the code base deletes a company
// yet (kyb-sweep only flags); the first deletion path must call this inside its transaction.
func EnqueueDeleted(ctx context.Context, tx outbox.Execer, p CompanyDeleted) error {
	return enqueue(ctx, tx, p.Region, func() (events.Event, error) { return DeletedEvent(p) })
}
