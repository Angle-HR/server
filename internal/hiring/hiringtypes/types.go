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

	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
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
}

// ListFilter selects jobs for the list view.
type ListFilter struct {
	Statuses     []string // empty: every status
	DepartmentID string
	Query        string
	OnlyMine     string // user id: only jobs this user created or is a member of (empty: all jobs)
	Cursor       string
	Limit        int
}

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
