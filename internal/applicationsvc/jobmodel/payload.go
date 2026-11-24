// Package jobmodel is the applications service's local, event-fed read model of jobs (BOUNDARIES.md 4.3). The
// Projector applies hiring.job.* events to hiring.app_jobs; the Model implements appjobs.JobSource from that table so
// public apply no longer reads hiring tables. What events cannot answer is delegated to a Fallback source; see
// docs/microservices/applications-jobmodel.md.
//
// The package does not import any hiring package. The event payload is decoded into the consumer-side copy below;
// payload_contract_test.go checks it stays compatible with the producer's (internal/hiring/jobevents).
package jobmodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Event types hiring emits. They are the contract; keep them in step with internal/hiring/jobevents.
const (
	TypePublished = "hiring.job.published.v1"
	TypeUpdated   = "hiring.job.updated.v1"
	TypePaused    = "hiring.job.paused.v1"
	TypeResumed   = "hiring.job.resumed.v1"
	TypeClosed    = "hiring.job.closed.v1"
	// TypeMembersChanged carries a job's complete hiring team (ids and roles).
	TypeMembersChanged = "hiring.job.members_changed.v1"
)

// SubjectFilter selects every job lifecycle event.
const SubjectFilter = "hiring.job.>"

// Consumer is the durable consumer name and the events_inbox consumer; replicas share it.
const Consumer = "applications-jobs"

// Row sources, see the source column.
const (
	SourceEvent       = "event"
	SourceReadThrough = "read_through"
)

func knownType(t string) bool {
	switch t {
	case TypePublished, TypeUpdated, TypePaused, TypeResumed, TypeClosed:
		return true
	}
	return false
}

// payload is the data of every hiring.job.* event, as far as applications reads it.
type payload struct {
	Version         int64    `json:"version"`
	JobID           string   `json:"job_id"`
	PublicID        string   `json:"public_id"`
	CompanyID       string   `json:"company_id"`
	Region          string   `json:"region"`
	Status          string   `json:"status"`
	PublicState     string   `json:"public_state"`
	ClosingDate     string   `json:"closing_date"`
	RetentionMonths int      `json:"retention_months"`
	JobNumber       int      `json:"job_number"`
	JobCode         string   `json:"job_code"`
	Title           string   `json:"title"`
	LawfulBasis     string   `json:"lawful_basis"`
	Markets         []string `json:"markets"`
	PublishedAt     string   `json:"published_at"`
	UpdatedAt       string   `json:"updated_at"`
	CloseReason     string   `json:"close_reason"`
	CreatedBy       string   `json:"created_by"`
	LocationMode    string   `json:"location_mode"`
	Form            *struct {
		ID      string          `json:"id"`
		Version int             `json:"version"`
		Schema  json.RawMessage `json:"schema"`
	} `json:"form"`
	Rules        json.RawMessage `json:"rules"`
	Declarations json.RawMessage `json:"declarations"`
	Company      struct {
		Name         string `json:"name"`
		PrivacyEmail string `json:"privacy_email"`
		DPOContact   string `json:"dpo_contact"`
	} `json:"company"`
	Notice json.RawMessage `json:"notice"`
}

// row is one hiring.app_jobs row in write form.
type row struct {
	JobID, PublicID, CompanyID, Region, Status, PublicState string
	ClosingDate                                             string // YYYY-MM-DD or ""
	ClosedAt                                                *time.Time
	RetentionMonths, JobNumber                              int
	JobCode, Title, LawfulBasis                             string
	Markets                                                 []string
	FormID                                                  *string
	FormVersion                                             int
	FormSchema                                              []byte // nil: no frozen form
	Rules, Declarations, Notice                             []byte
	CompanyName, PrivacyEmail, DPOContact, CloseReason      string
	CreatedBy                                               string // "" when the event predates the field
	LocationMode                                            string // "" when the event predates the field
	Version                                                 int64
	Source                                                  string
	PublishedAt                                             *time.Time
	UpdatedAt                                               time.Time
}

func parseTime(s string) *time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

func jsonOr(raw json.RawMessage, def string) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return []byte(def)
	}
	return raw
}

// toRow validates a payload and turns it into a row. An error means the event can never be applied.
func (p payload) toRow(now time.Time) (row, error) {
	switch {
	case p.JobID == "" || p.PublicID == "" || p.CompanyID == "":
		return row{}, errors.New("job_id, public_id and company_id are required")
	case p.Region == "":
		return row{}, errors.New("region is required")
	case p.Version < 1:
		return row{}, fmt.Errorf("version %d is not positive", p.Version)
	case p.Status == "":
		return row{}, errors.New("status is required")
	}
	r := row{
		JobID: p.JobID, PublicID: p.PublicID, CompanyID: p.CompanyID, Region: p.Region, Status: p.Status,
		PublicState: p.PublicState, ClosingDate: p.ClosingDate, RetentionMonths: p.RetentionMonths,
		JobNumber: p.JobNumber, JobCode: p.JobCode, Title: p.Title, LawfulBasis: p.LawfulBasis,
		Rules: jsonOr(p.Rules, "[]"), Declarations: jsonOr(p.Declarations, "[]"), Notice: jsonOr(p.Notice, "{}"),
		CompanyName: p.Company.Name, PrivacyEmail: p.Company.PrivacyEmail, DPOContact: p.Company.DPOContact,
		CloseReason: p.CloseReason, CreatedBy: p.CreatedBy, LocationMode: p.LocationMode, Version: p.Version, Source: SourceEvent, PublishedAt: parseTime(p.PublishedAt),
	}
	if r.JobCode == "" && r.JobNumber > 0 {
		r.JobCode = fmt.Sprintf("JB-%d", r.JobNumber)
	}
	// The same distinct, sorted market list jobsource reads.
	seen := map[string]bool{}
	r.Markets = []string{}
	for _, m := range p.Markets {
		if !seen[m] {
			seen[m] = true
			r.Markets = append(r.Markets, m)
		}
	}
	sort.Strings(r.Markets)
	if p.Form != nil && p.Form.ID != "" && len(p.Form.Schema) > 0 {
		id := p.Form.ID
		r.FormID, r.FormVersion, r.FormSchema = &id, p.Form.Version, p.Form.Schema
	}
	updated := parseTime(p.UpdatedAt)
	if updated == nil {
		u := now
		updated = &u
	}
	r.UpdatedAt = *updated
	// hiring sets closed_at to now() in the write that closes the job, which is also the job's updated_at.
	if p.Status == "closed" || p.Status == "expired" {
		r.ClosedAt = updated
	}
	return r, nil
}

// membersPayload is the data of hiring.job.members_changed.v1.
type membersPayload struct {
	Version   int64  `json:"version"`
	JobID     string `json:"job_id"`
	CompanyID string `json:"company_id"`
	Region    string `json:"region"`
	Members   []struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	} `json:"members"`
}
