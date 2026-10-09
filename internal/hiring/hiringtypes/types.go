// Package hiringtypes holds the plain data types and errors shared by the hiring store and the code that uses
// it. It has no database dependency, so the draft service can be built and tested without one.
package hiringtypes

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/hiring/gates"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/hiring/screening"
)

// Errors the HTTP layer maps to responses.
var (
	// ErrNotFound means the row does not exist, was deleted, or belongs to another company.
	ErrNotFound = errors.New("hiringstore: not found")
	// ErrConflict means a unique value (a template name, say) is already taken.
	ErrConflict = errors.New("hiringstore: already exists")
	// ErrBadReference means a foreign key pointed at a row that does not exist for this company.
	ErrBadReference = errors.New("hiringstore: unknown reference")
	// ErrLawfulBasisRequired means special categories were declared before the job has a lawful basis.
	ErrLawfulBasisRequired = errors.New("hiringstore: lawful basis required")
	// ErrNotDraft means the job is past the draft stage and cannot be removed this way.
	ErrNotDraft = errors.New("hiringstore: job is not a draft")
)

// StaleRevisionError means the job changed since the caller loaded it.
type StaleRevisionError struct{ Current int }

func (e *StaleRevisionError) Error() string {
	return fmt.Sprintf("hiringstore: stale revision (current %d)", e.Current)
}

// JobRecord is a job with its application form.
type JobRecord struct {
	Job          jobs.Job
	Form         []questions.FormQuestion
	Declarations []questions.Declaration
	FormRevision int

	// Phase 2.
	Members         []Member
	Rules           []screening.Rule
	Confirmations   []gates.Confirmation
	DPIAConfirmedAt string // when the job's owner confirmed screening is within the company DPIA ("" = not yet)
	FormVersion     int    // newest frozen form_versions row (0 = never published)
}

// Member is a person on a job's hiring team. The job's creator has access without being listed.
type Member struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
	Name   string `json:"name,omitempty"`  // output only
	Email  string `json:"email,omitempty"` // output only
}

// Person is someone in the company who can be added to a hiring team.
type Person struct {
	UserID string   `json:"user_id"`
	Name   string   `json:"name"`
	Email  string   `json:"email"`
	Roles  []string `json:"roles"`
}

// Hiring team roles stored in hiring.job_members.
const (
	MemberHiringManager = "hiring_manager"
	MemberRecruiter     = "recruiter"
	MemberInterviewer   = "interviewer"
	MemberViewer        = "viewer"
)

// LogEntry is one compliance_log row written with a status change: one per gate, so the log shows what the
// publisher saw and confirmed.
type LogEntry struct {
	GateID   string
	Severity string
	Event    string
}

// StatusChange moves a job to another status in the same write as the checks that allowed it.
type StatusChange struct {
	To         string
	FreezeForm bool       // write a new form_versions row (every publish does)
	PublicID   string     // set on first publish
	Log        []LogEntry // compliance_log rows, same transaction
}

// GateConfirm records one customer confirmation (or withdrawal) of a gate.
type GateConfirm struct {
	GateID    string
	Version   int
	Confirmed bool
	Severity  string
}

// CreateInput is a new draft.
type CreateInput struct {
	TenantID  string
	UserID    string
	Details   jobs.Details
	Completed []string
	Step      string
	Form      []questions.FormQuestion
	Decls     []questions.Declaration
}

// UpdateResult is what an update callback asks the store to write.
type UpdateResult struct {
	Details     *jobs.Details             // replace the job's details (nil leaves them)
	Completed   []string                  // completed data sections after the update
	Step        string                    // wizard step after the update
	Form        *[]questions.FormQuestion // replace the whole form (nil leaves it)
	Decls       *[]questions.Declaration  // replace the special category declarations (nil leaves them)
	AuditAction string
	AuditDiff   map[string]any

	Status      *StatusChange     // change the job's status
	Members     *[]Member         // replace the hiring team
	Rules       *[]screening.Rule // replace the screening rules
	DPIAConfirm *bool             // set or clear the job's DPIA scope confirmation
	Confirm     *GateConfirm      // confirm or withdraw one gate
}

// ListFilter selects jobs for the list view.
type ListFilter struct {
	Statuses     []string // empty: every status
	DepartmentID string
	Query        string
	OnlyMine     string // user id: only jobs this user created or is a member of (empty: all jobs)
	Cursor       string
	Limit        int

	CreatedBy      string // user id: only jobs this user created
	Assignee       string // user id: only jobs this user is a hiring team member of
	EmploymentType string
	WorkplaceType  string
	LocationMode   string
	Market         string // market code, e.g. GB
	CreatedFrom    string // YYYY-MM-DD, inclusive
	CreatedTo      string // YYYY-MM-DD, inclusive
	Sort           string // SortUpdated (default) or SortCreated
	Ascending      bool   // oldest first; the default is newest first
}

// Sort keys the jobs list accepts. Both are timestamps, so the cursor stays a (time, id) pair.
const (
	SortUpdated = "updated_at"
	SortCreated = "created_at"
)

// ListItem is one row of the jobs list.
type ListItem struct {
	ID             string   `json:"id"`
	JobCode        string   `json:"job_code"`
	Status         string   `json:"status"`
	Title          string   `json:"title"`
	DepartmentName string   `json:"department_name,omitempty"`
	EmploymentType string   `json:"employment_type,omitempty"`
	LocationMode   string   `json:"location_mode,omitempty"`
	Markets        []string `json:"markets"`
	CurrentStep    string   `json:"current_step"`
	Revision       int      `json:"revision"`
	CreatedBy      string   `json:"created_by"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`

	WorkplaceType string       `json:"workplace_type,omitempty"`
	PublishedAt   string       `json:"published_at,omitempty"`
	ClosingDate   string       `json:"closing_date,omitempty"`
	Managers      []ListPerson `json:"managers"`
}

// ListPerson is a person shown on a jobs list row, such as a hiring manager.
type ListPerson struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

// DefaultListLimit and MaxListLimit bound a page.
const (
	DefaultListLimit = 25
	MaxListLimit     = 100
)

// Department is a company department a job can belong to.
type Department struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Template kinds.
const (
	KindJobDetails      = "job_details"
	KindApplicationForm = "application_form"
)

// Template is a saved starting point: a named company template, or a user's default ("Keep this setup").
type Template struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Payload   json.RawMessage `json:"payload"`
	IsDefault bool            `json:"is_default"`
	CreatedBy string          `json:"created_by,omitempty"`
	UpdatedAt string          `json:"updated_at"`
}

// Settings are company-level hiring settings.
type Settings struct {
	AutomatedScreeningEnabled bool   `json:"automated_screening_enabled"`
	EnabledAt                 string `json:"automated_screening_enabled_at,omitempty"`
}

// CatalogItem is one option in a pick list.
type CatalogItem struct {
	ID   string `json:"id"`
	Slug string `json:"slug,omitempty"`
	Name string `json:"name"`
}

// RefCheck says which referenced catalog ids exist.
type RefCheck struct {
	UnknownSkills []string
	IndustryOK    bool
	SeniorityOK   bool
	ExperienceOK  bool
}

// EncodeCursor and DecodeCursor carry the keyset position (updated_at, id) between pages.
func EncodeCursor(updatedAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(updatedAt + "|" + id))
}

// DecodeCursor parses a cursor made by EncodeCursor.
func DecodeCursor(c string) (updatedAt, id string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", "", errors.New("invalid cursor")
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 || !jobs.IsUUID(parts[1]) {
		return "", "", errors.New("invalid cursor")
	}
	if _, err = time.Parse(time.RFC3339Nano, parts[0]); err != nil {
		return "", "", errors.New("invalid cursor")
	}
	return parts[0], parts[1], nil
}

// RegistryEntry is a published job's row in the global registry: just enough to find its region.
type RegistryEntry struct {
	PublicID       string
	Region         string
	OrganizationID string
	Status         string
	PublishedAt    time.Time
	ValidThrough   string // closing date, YYYY-MM-DD, or ""
}

// CompanyState is what the publish checks need to know about the company.
type CompanyState struct {
	Verified          bool // KYB status is verified
	DPAAccepted       bool // the current terms and DPA version is accepted
	PrivacyContactSet bool
	DPIARecorded      bool // Legal recorded the company DPIA
}

// CompanySetup is the company's publish prerequisites as the settings page shows them.
type CompanySetup struct {
	KYBStatus         string `json:"kyb_status"`
	DPAVersion        string `json:"dpa_version"` // the version that must be accepted
	DPAAccepted       bool   `json:"dpa_accepted"`
	DPAAcceptedAt     string `json:"dpa_accepted_at,omitempty"`
	PrivacyContact    string `json:"privacy_contact_email"`
	DPOContact        string `json:"dpo_contact"`
	DPIAVersion       int    `json:"dpia_version"` // 0 = not recorded
	DPIARecordedAt    string `json:"dpia_recorded_at,omitempty"`
	AutomatedScreenOn bool   `json:"automated_screening_enabled"`
}
