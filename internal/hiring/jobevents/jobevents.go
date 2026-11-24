// Package jobevents is the contract of the job lifecycle events hiring publishes so the applications service can
// keep a local read model of jobs and never query hiring tables (see docs/microservices/BOUNDARIES.md 4.3).
//
// Every event carries the same Payload: a full snapshot of the job as an applicant-facing consumer needs it. A
// consumer upserts the snapshot when Payload.Version is higher than the one it holds and ignores it otherwise, so
// replays and out-of-order delivery are harmless. Events are written to the transactional outbox in the same
// transaction as the state change, with the job id as partition key.
//
//	Type                      When                                              Public state after
//	hiring.job.published.v1   publish, or reopen of a closed/expired job         open
//	hiring.job.updated.v1     details, rules or declarations of a live job change open or paused
//	hiring.job.paused.v1      published -> paused                                paused
//	hiring.job.resumed.v1     paused -> published                                open
//	hiring.job.closed.v1      close, expiry (closing date passed), back to draft closed
//	hiring.job.members_changed.v1  the job's hiring team is saved (any status)    n/a (see MembersPayload)
//
// The first five carry Payload. members_changed carries MembersPayload, a separate and much smaller document: the
// full current team of the job (ids and roles only), so the consumer replaces the team it holds when Version is
// higher than the one it has. It is the source for the applications service's job-access read model.
//
// Emission is off unless HIRING_EVENTS=on (see Enabled).
package jobevents

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/formschema/screening"
)

// Source is the producing service written to every event.
const Source = "/services/hiring"

// Event types.
const (
	TypePublished = "hiring.job.published.v1"
	TypeUpdated   = "hiring.job.updated.v1"
	TypePaused    = "hiring.job.paused.v1"
	TypeResumed   = "hiring.job.resumed.v1"
	TypeClosed    = "hiring.job.closed.v1"
	// TypeMembersChanged is emitted when a job's hiring team changes (see MembersPayload).
	TypeMembersChanged = "hiring.job.members_changed.v1"
)

// EnvVar switches emission. Anything but "on" (case-insensitive) leaves behaviour unchanged and writes nothing.
const EnvVar = "HIRING_EVENTS"

// Enabled is the single gate for emitting job events.
func Enabled() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvVar)), "on") }

// Close reasons carried on hiring.job.closed.v1.
const (
	ReasonClosed   = "closed"   // someone closed the job
	ReasonExpired  = "expired"  // the closing date passed
	ReasonToDraft  = "to_draft" // pulled back to draft
	ReasonArchived = "archived" // archived while still live (not reachable today)
)

// Company is what the company has told candidates, as hiring reads it today from the organization row.
type Company struct {
	Name         string `json:"name"`
	PrivacyEmail string `json:"privacy_email,omitempty"`
	DPOContact   string `json:"dpo_contact,omitempty"`
}

// Form is the frozen application form candidates apply with.
type Form struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	// Schema is the form_versions.schema document as stored: {"form_revision":n,"questions":[...]}.
	Schema json.RawMessage `json:"schema"`
}

// Notice holds the inputs of the applicant privacy notice and its version.
type Notice struct {
	Version         string                   `json:"version"`
	LawfulBasis     string                   `json:"lawful_basis,omitempty"`
	Declarations    []formschema.Declaration `json:"declarations"`
	ScreeningActive bool                     `json:"screening_active"`
}

// Payload is the data of every hiring.job.* event.
type Payload struct {
	// Version increases with every write to the job (job_postings.revision). Ignore an event whose version is not
	// higher than the one already held for the job.
	Version     int64  `json:"version"`
	JobID       string `json:"job_id"`
	PublicID    string `json:"public_id"`
	CompanyID   string `json:"company_id"`
	Region      string `json:"region"`
	Status      string `json:"status"`       // stored status: published, paused, closed, expired, draft ...
	PublicState string `json:"public_state"` // open, paused or closed, as the careers page derives it
	// ClosingDate is the last day to apply (YYYY-MM-DD, UTC) or "". A published job past it is closed to
	// applications even before the expiry task runs; derive that locally.
	ClosingDate     string   `json:"closing_date"`
	RetentionMonths int      `json:"retention_months"`
	JobNumber       int      `json:"job_number"`
	JobCode         string   `json:"job_code"`
	Title           string   `json:"title"`
	LawfulBasis     string   `json:"lawful_basis,omitempty"`
	Markets         []string `json:"markets"`
	PublishedAt     string   `json:"published_at,omitempty"`
	UpdatedAt       string   `json:"updated_at"`
	// CloseReason is set on closed events only.
	CloseReason string `json:"close_reason,omitempty"`
	// CreatedBy is the user who created the job. The creator may act on the job without being on its team, so
	// the applications service needs it next to hiring.job.members_changed.v1 to answer who has access.
	CreatedBy string `json:"created_by,omitempty"`
	// LocationMode is anywhere, specific_area or specific_timezone. An "anywhere" job has an empty markets list and is
	// open to candidates from every country.
	LocationMode string `json:"location_mode,omitempty"`

	Form         *Form                    `json:"form,omitempty"` // nil when the job never published
	Rules        []screening.Rule         `json:"rules"`          // knockout / disqualification rules as apply uses them
	Declarations []formschema.Declaration `json:"declarations"`   // special category declarations
	Company      Company                  `json:"company"`
	Notice       Notice                   `json:"notice"`
}

// Input is what Build needs; the store fills it inside the transaction that changed the job.
type Input struct {
	Region    string
	CompanyID string
	Job       jobs.Job
	Form      *Form
	Rules     []screening.Rule
	Decls     []formschema.Declaration
	Company   Company
	Reason    string
	Now       time.Time
}

// Snapshot builds the payload.
func Snapshot(in Input) Payload {
	j := in.Job
	state, ok := public.StateOf(j.Status, j.ClosingDate, in.Now)
	if !ok {
		state = public.StateClosed
	}
	markets := make([]string, 0, len(j.Markets))
	seen := map[string]bool{}
	for _, m := range j.Markets {
		if !seen[m.MarketCode] {
			seen[m.MarketCode] = true
			markets = append(markets, m.MarketCode)
		}
	}
	rules, decls := in.Rules, in.Decls
	if rules == nil {
		rules = []screening.Rule{}
	}
	if decls == nil {
		decls = []formschema.Declaration{}
	}
	return Payload{
		Version: int64(j.Revision), JobID: j.ID, PublicID: j.PublicID, CompanyID: in.CompanyID, Region: in.Region,
		Status: j.Status, PublicState: string(state), ClosingDate: j.ClosingDate, RetentionMonths: j.RetentionMonths,
		JobNumber: j.JobNumber, JobCode: j.JobCode, Title: j.Title, LawfulBasis: j.LawfulBasis, Markets: markets,
		PublishedAt: j.PublishedAt, UpdatedAt: j.UpdatedAt, CloseReason: in.Reason, CreatedBy: j.CreatedBy, LocationMode: j.LocationMode,
		Form: in.Form, Rules: rules, Declarations: decls, Company: in.Company,
		Notice: Notice{Version: public.NoticeVersion, LawfulBasis: j.LawfulBasis, Declarations: decls, ScreeningActive: len(rules) > 0},
	}
}

// namespace makes event ids; fixed so the same (type, job, version) always gives the same id.
var namespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("openhr:hiring.job"))

// EventID is the deterministic id of an event: the same job, version and type always give the same id, so the
// outbox (ON CONFLICT DO NOTHING) and the bus (de-duplication by id) make a repeat a no-op.
func EventID(typ, jobID string, version int64) string {
	return uuid.NewSHA1(namespace, []byte(fmt.Sprintf("%s|%s|%d", typ, jobID, version))).String()
}

// Build makes the event for a payload: partitioned by job id, with a deterministic id.
func Build(typ string, p Payload, now time.Time) (events.Event, error) {
	ev, err := events.NewJSON(Source, typ, p)
	if err != nil {
		return ev, err
	}
	ev.ID = EventID(typ, p.JobID, p.Version)
	ev.Time = now.UTC()
	return ev.WithSubject(p.JobID).WithCompany(p.CompanyID).WithRegion(p.Region).WithPartitionKey(p.JobID), nil
}

// Change is what one store write did to a job, as far as the events care.
type Change struct {
	From, To string
	// Content is true when the write replaced details, screening rules or declarations.
	Content bool
}

func live(status string) bool { return status == jobs.StatusPublished || status == jobs.StatusPaused }

// Decide names the event a change produces, if any, and the close reason for a closed event.
func Decide(c Change) (typ, reason string, ok bool) {
	switch {
	case c.To == jobs.StatusPublished && c.From == jobs.StatusPaused:
		return TypeResumed, "", true
	case c.To == jobs.StatusPublished && c.From != jobs.StatusPublished:
		return TypePublished, "", true
	case c.To == jobs.StatusPaused && c.From == jobs.StatusPublished:
		return TypePaused, "", true
	case live(c.From) && !live(c.To):
		switch c.To {
		case jobs.StatusExpired:
			reason = ReasonExpired
		case jobs.StatusDraft:
			reason = ReasonToDraft
		case jobs.StatusArchived:
			reason = ReasonArchived
		default:
			reason = ReasonClosed
		}
		return TypeClosed, reason, true
	case c.From == c.To && live(c.To) && c.Content:
		return TypeUpdated, "", true
	}
	return "", "", false
}

// Member is one person on a job's hiring team, by id only.
type Member struct {
	UserID string `json:"user_id"`
	// Role is the job role, for example "hiring_manager" or "collaborator" (job_members.role).
	Role string `json:"role"`
}

// MembersPayload is the data of hiring.job.members_changed.v1. It holds no personal data beyond user ids.
type MembersPayload struct {
	// Version is job_postings.revision after the write. Replace the team you hold for the job only when this is
	// higher than the version you stored with it; equal or lower is a replay or reordering and is ignored.
	Version   int64  `json:"version"`
	JobID     string `json:"job_id"`
	CompanyID string `json:"company_id"`
	Region    string `json:"region"`
	// Members is the complete team after the change, ordered by role then user id. Empty (never null) when the
	// last member was removed.
	Members []Member `json:"members"`
}

// BuildMembers makes the members_changed event: partitioned by job id, with a deterministic id.
func BuildMembers(p MembersPayload, now time.Time) (events.Event, error) {
	if p.Members == nil {
		p.Members = []Member{}
	}
	ev, err := events.NewJSON(Source, TypeMembersChanged, p)
	if err != nil {
		return ev, err
	}
	ev.ID = EventID(TypeMembersChanged, p.JobID, p.Version)
	ev.Time = now.UTC()
	return ev.WithSubject(p.JobID).WithCompany(p.CompanyID).WithRegion(p.Region).WithPartitionKey(p.JobID), nil
}
