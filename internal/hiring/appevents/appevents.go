// Package appevents is the contract of the events the applications side publishes:
//
//	applications.application.submitted.v1       a new application was stored
//	applications.application.status_changed.v1  an application moved to another status (plain change or review)
//	applications.application.stage_changed.v1   an application moved to another stage of its job's pipeline
//
// Events are written to the transactional outbox of the application's regional database in the same
// transaction as the write, with the application id as partition key, and relayed to the bus by an outbox relay.
// Payloads are small on purpose: ids, region, status and timestamps. They never carry the candidate's name, email
// or answers; a consumer that needs more asks the applications service.
//
// Emission is off unless APPLICATIONS_EVENTS=on (see Enabled). The package depends on shared libraries only.
package appevents

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// Source is the producing service written to every event.
const Source = "/services/applications"

// Event types.
const (
	TypeSubmitted     = "applications.application.submitted.v1"
	TypeStatusChanged = "applications.application.status_changed.v1"
	TypeStageChanged  = "applications.application.stage_changed.v1"
)

// EnvVar switches emission. Anything but "on" (case-insensitive) leaves behaviour unchanged and writes nothing.
const EnvVar = "APPLICATIONS_EVENTS"

// Enabled is the single gate for emitting application events.
func Enabled() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvVar)), "on") }

// Submitted is the data of applications.application.submitted.v1.
type Submitted struct {
	ApplicationID string    `json:"application_id"`
	JobID         string    `json:"job_id"`
	CompanyID     string    `json:"company_id"`
	Region        string    `json:"region"`
	Status        string    `json:"status"` // received, or flagged when a screening rule needs a person
	Source        string    `json:"source"` // share channel, e.g. direct
	SubmittedAt   time.Time `json:"submitted_at"`
}

// StatusChanged is the data of applications.application.status_changed.v1.
type StatusChanged struct {
	ApplicationID   string    `json:"application_id"`
	JobID           string    `json:"job_id"`
	CompanyID       string    `json:"company_id"`
	Region          string    `json:"region"`
	FromStatus      string    `json:"from_status"`
	ToStatus        string    `json:"to_status"`
	ChangedByUserID string    `json:"changed_by_user_id"`
	ChangedAt       time.Time `json:"changed_at"`
}

// StageChanged is the data of applications.application.stage_changed.v1. Stage ids are the job's own; the kind
// (applied, shortlisted, interview, offer, hired, disqualified or custom) is stable across jobs.
type StageChanged struct {
	ApplicationID   string    `json:"application_id"`
	JobID           string    `json:"job_id"`
	CompanyID       string    `json:"company_id"`
	Region          string    `json:"region"`
	FromStageID     string    `json:"from_stage_id,omitempty"`
	ToStageID       string    `json:"to_stage_id"`
	ToStageKind     string    `json:"to_stage_kind"`
	ChangedByUserID string    `json:"changed_by_user_id"`
	ChangedAt       time.Time `json:"changed_at"`
}

// ErrNoRegion is returned when events are on but the store has no region to stamp them with.
var ErrNoRegion = errors.New("appevents: events are on but the store has no region")

var idSpace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("openhr/applications/events"))

// SubmittedEvent builds the event. Its id derives from the application, so enqueueing it twice is a no-op.
func SubmittedEvent(p Submitted) (events.Event, error) {
	ev, err := events.NewJSON(Source, TypeSubmitted, p)
	if err != nil {
		return ev, err
	}
	ev.ID = uuid.NewSHA1(idSpace, []byte(TypeSubmitted+"/"+p.ApplicationID)).String()
	ev.Time = p.SubmittedAt.UTC()
	return ev.WithSubject(p.ApplicationID).WithCompany(p.CompanyID).WithRegion(p.Region).WithPartitionKey(p.ApplicationID), nil
}

// StatusChangedEvent builds the event. Its id derives from the application, the change and its time.
func StatusChangedEvent(p StatusChanged) (events.Event, error) {
	ev, err := events.NewJSON(Source, TypeStatusChanged, p)
	if err != nil {
		return ev, err
	}
	key := strings.Join([]string{TypeStatusChanged, p.ApplicationID, p.FromStatus, p.ToStatus, p.ChangedAt.UTC().Format(time.RFC3339Nano)}, "/")
	ev.ID = uuid.NewSHA1(idSpace, []byte(key)).String()
	ev.Time = p.ChangedAt.UTC()
	return ev.WithSubject(p.ApplicationID).WithCompany(p.CompanyID).WithRegion(p.Region).WithPartitionKey(p.ApplicationID), nil
}

// StageChangedEvent builds the event. Its id derives from the application, the stages and the time.
func StageChangedEvent(p StageChanged) (events.Event, error) {
	ev, err := events.NewJSON(Source, TypeStageChanged, p)
	if err != nil {
		return ev, err
	}
	key := strings.Join([]string{TypeStageChanged, p.ApplicationID, p.FromStageID, p.ToStageID, p.ChangedAt.UTC().Format(time.RFC3339Nano)}, "/")
	ev.ID = uuid.NewSHA1(idSpace, []byte(key)).String()
	ev.Time = p.ChangedAt.UTC()
	return ev.WithSubject(p.ApplicationID).WithCompany(p.CompanyID).WithRegion(p.Region).WithPartitionKey(p.ApplicationID), nil
}

// EnqueueStageChanged writes the stage changed event into the outbox using the caller's transaction. It does
// nothing unless Enabled. An error must abort the transaction (fail closed).
func EnqueueStageChanged(ctx context.Context, tx outbox.Execer, p StageChanged) error {
	if !Enabled() {
		return nil
	}
	if p.Region == "" {
		return ErrNoRegion
	}
	ev, err := StageChangedEvent(p)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, ev)
}

// EnqueueSubmitted writes the submitted event into the outbox using the caller's transaction. It does nothing
// unless Enabled. An error must abort the transaction (fail closed).
func EnqueueSubmitted(ctx context.Context, tx outbox.Execer, p Submitted) error {
	if !Enabled() {
		return nil
	}
	if p.Region == "" {
		return ErrNoRegion
	}
	ev, err := SubmittedEvent(p)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, ev)
}

// EnqueueStatusChanged writes the status changed event into the outbox using the caller's transaction. It does
// nothing unless Enabled. An error must abort the transaction (fail closed).
func EnqueueStatusChanged(ctx context.Context, tx outbox.Execer, p StatusChanged) error {
	if !Enabled() {
		return nil
	}
	if p.Region == "" {
		return ErrNoRegion
	}
	ev, err := StatusChangedEvent(p)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, ev)
}
