// Package jobs holds the job-posting domain: the draft model, the rules that validate it,
// duplicate detection and the public (candidate-facing) view. It depends only on the standard
// library so the rules can be tested without a database.
package jobs

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Job statuses. Phase 1 only creates and edits drafts.
const (
	StatusDraft           = "draft"
	StatusPendingApproval = "pending_approval"
	StatusPublished       = "published"
	StatusPaused          = "paused"
	StatusClosed          = "closed"
	StatusArchived        = "archived"
	StatusExpired         = "expired"
)

// Data sections a draft is tracked by (the PRD steps and the Figma screens both save into these).
const (
	SectionBasics         = "basics"
	SectionMarkets        = "markets"
	SectionCompensation   = "compensation"
	SectionScreening      = "screening"
	SectionDocuments      = "documents"
	SectionDataProtection = "data_protection"
)

// Wizard steps, in order (Figma flow: Job details, Application form, Permissions, Share).
const (
	StepDetails         = "details"
	StepApplicationForm = "application_form"
	StepPermissions     = "permissions"
	StepShare           = "share"
)

// Location modes.
const (
	LocationAnywhere         = "anywhere"
	LocationSpecificArea     = "specific_area"
	LocationSpecificTimezone = "specific_timezone"
)

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// Allowed values for the enum-like job fields.
var (
	LocationModes     = set(LocationAnywhere, LocationSpecificArea, LocationSpecificTimezone)
	WorkplaceTypes    = set("onsite", "hybrid", "remote")
	EmploymentTypes   = set("full_time", "part_time", "contract", "internship")
	TravelFrequencies = set("none", "occasional", "frequent")
	VisaPolicies      = set("yes", "no", "case_by_case")
	PayTypes          = set("exact", "range")
	PayPeriods        = set("hour", "month", "year")
	LawfulBases       = set("contract", "legitimate_interests", "consent", "legal_obligation")
	RetentionOptions  = map[int]bool{3: true, 6: true, 12: true}

	// Currencies are limited to the launch markets (SGD, PHP and AED left with their markets on 7 Oct).
	Currencies = set("GBP", "EUR", "USD", "NGN", "KES", "INR")
)

// RetentionChoices lists the retention periods an employer can pick, in months.
var RetentionChoices = []int{3, 6, 12}

// CurrencyCodes lists the supported currencies in display order.
var CurrencyCodes = []string{"GBP", "EUR", "USD", "NGN", "KES", "INR"}

// DescriptionKeys are the six fixed sections of the structured job description.
var DescriptionKeys = []string{
	"about_company", "role", "responsibilities", "requirements", "benefits", "hiring_process",
}

// DefaultRetentionMonths is the retention period of a new job.
const DefaultRetentionMonths = 6

// Sections is the structured job description: the rich-text editor's JSON, one document per key.
type Sections map[string]json.RawMessage

// Skill is one entry in a job's skills list: a catalog skill or free text.
type Skill struct {
	SkillID     string `json:"skill_id,omitempty"`
	CustomLabel string `json:"custom_label,omitempty"`
	Name        string `json:"name,omitempty"` // resolved label on output; ignored on input
}

// MarketSel is one place a job is open to: a market, optionally narrowed to a state, city or time zone.
type MarketSel struct {
	MarketCode  string `json:"market_code"`
	Subdivision string `json:"subdivision,omitempty"`
	City        string `json:"city,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

// Pay is the job's compensation. Amounts are minor units (pence, cents, kobo).
type Pay struct {
	Type     string `json:"type"`
	Currency string `json:"currency"`
	Period   string `json:"period"`
	Min      *int64 `json:"min"`
	Max      *int64 `json:"max"`
	Visible  bool   `json:"visible"`
}

// Complete reports whether every pay field needed to describe the pay is set.
func (p Pay) Complete() bool {
	if p.Type == "" || p.Currency == "" || p.Period == "" || p.Min == nil {
		return false
	}
	return p.Type != "range" || p.Max != nil
}

// Details is everything the employer enters about the role (wizard step 1 and its data-protection settings).
type Details struct {
	Title             string      `json:"title"`
	DepartmentID      string      `json:"department_id"`
	ClosingDate       string      `json:"closing_date"`
	LocationMode      string      `json:"location_mode"`
	LocationText      string      `json:"location_text"`
	UseCompanyAddress bool        `json:"use_company_address"`
	WorkplaceType     string      `json:"workplace_type"`
	TravelFrequency   string      `json:"travel_frequency"`
	VisaSponsorship   string      `json:"visa_sponsorship"`
	Description       Sections    `json:"description_sections"`
	IndustryID        string      `json:"industry_id"`
	CustomIndustry    string      `json:"custom_industry"`
	EmploymentType    string      `json:"employment_type"`
	SeniorityLevelID  string      `json:"seniority_level_id"`
	ExperienceRangeID string      `json:"experience_range_id"`
	Pay               Pay         `json:"pay"`
	ShowOnCareerPage  bool        `json:"show_on_career_page"`
	Skills            []Skill     `json:"skills"`
	Markets           []MarketSel `json:"markets"`
	LawfulBasis       string      `json:"lawful_basis"`
	LIAReference      string      `json:"lia_reference"`
	RetentionMonths   int         `json:"retention_months"`
	AssessmentURL     string      `json:"assessment_url"`
}

// NewDetails returns the starting values of a new draft.
func NewDetails() Details {
	return Details{
		Pay:              Pay{Visible: true},
		ShowOnCareerPage: true,
		RetentionMonths:  DefaultRetentionMonths,
		Description:      Sections{},
	}
}

// Job is a stored job posting with the derived values the list and detail views show.
type Job struct {
	ID                string   `json:"id"`
	JobNumber         int      `json:"job_number"`
	JobCode           string   `json:"job_code"`
	CreatedBy         string   `json:"created_by"`
	Status            string   `json:"status"`
	CurrentStep       string   `json:"current_step"`
	CompletedSections []string `json:"completed_sections"`
	Revision          int      `json:"revision"`
	DepartmentName    string   `json:"department_name,omitempty"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
	PublishedAt       string   `json:"published_at,omitempty"`
	Details
}

// JobCode formats a per-organization job number the way the UI shows it.
func JobCode(n int) string { return fmt.Sprintf("JB-%d", n) }

// FieldError points at the part of a request that is wrong, e.g. markets[1].timezone.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Warning is advice shown next to a field; it never blocks a save.
type Warning struct {
	Code     string `json:"code"`
	Title    string `json:"title,omitempty"`
	Message  string `json:"message"`
	Market   string `json:"market,omitempty"`
	Severity string `json:"severity,omitempty"`
}

func errf(path, format string, args ...any) FieldError {
	return FieldError{Path: path, Message: fmt.Sprintf(format, args...)}
}

func trim(s string) string { return strings.TrimSpace(s) }
