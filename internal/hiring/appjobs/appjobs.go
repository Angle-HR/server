// Package appjobs is the seam between the applications side (apply, uploads, applications, retention) and
// whoever owns jobs. The applications side never reads a hiring table: it asks a JobSource.
//
// Today the JobSource is an adapter over the hiring tables (package jobsource). When applications becomes its own
// service the same interface is implemented by a local read model fed by hiring.job.* events, with
// HiringService.GetPublicJob as the sync fallback (docs/microservices/BOUNDARIES.md, items 9-13 and 15).
//
// The package depends on shared libraries only (pkg/formschema), never on a hiring package.
package appjobs

import (
	"context"
	"time"

	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/formschema/screening"
)

// Roles a person can hold in a job's hiring team, as JobAccess.MemberRole reports them.
const (
	MemberHiringManager = "hiring_manager"
	MemberRecruiter     = "recruiter"
)

// Ref finds a published job: the region and company that hold it. It is what the global registry knows.
type Ref struct {
	PublicID       string
	Region         string
	OrganizationID string
}

// Registry resolves a public job id.
type Registry interface {
	// LookupJob returns apperror.ErrNotFound for an unknown id.
	LookupJob(ctx context.Context, publicID string) (Ref, error)
}

// JobSource is everything the applications side learns about jobs and their company. Lookups by public id go
// through the Registry; everything else is per region, because a company's data lives in its own region.
type JobSource interface {
	Registry
	// Regional returns the view of one region.
	Regional(region string) (RegionalJobs, error)
}

// RegionalJobs is the job and company data of one region. Every method takes the company (tenant) id and returns
// apperror.ErrNotFound for an unknown or deleted job.
type RegionalJobs interface {
	// LoadForApply reads a published job by public id with its newest frozen form and its screening rules.
	LoadForApply(ctx context.Context, tenantID, publicID string) (*ApplyTarget, error)
	// JobAccess is the job as an access check needs it, with the caller's role in the job's team.
	JobAccess(ctx context.Context, tenantID, jobID, userID string) (*JobAccess, error)
	// JobSummary is what a status email and the retention clock need about a job and its company.
	JobSummary(ctx context.Context, tenantID, jobID string) (*JobSummary, error)
	// FrozenQuestions returns the questions of one frozen form version.
	FrozenQuestions(ctx context.Context, tenantID, formID string, version int) ([]formschema.FormQuestion, error)
	// ClosedJobs lists the company's closed or expired jobs with the date their applications' retention clock
	// ends: the close date (or closing date plus a day, or now) plus the job's retention months.
	ClosedJobs(ctx context.Context, tenantID string, now time.Time) ([]ClosedJob, error)
	// JobLabels returns the job code and title of each job id it still knows. Missing jobs are left out.
	JobLabels(ctx context.Context, tenantID string, jobIDs []string) (map[string]JobLabel, error)
	// NoticeRecipient is where the retention notice goes: the privacy contact, else the owner.
	NoticeRecipient(ctx context.Context, tenantID string) (email, company string, err error)
	// TenantIDs lists every company in the region.
	TenantIDs(ctx context.Context) ([]string, error)
}

// ApplyTarget is a published job as the apply step needs it, read in one transaction.
type ApplyTarget struct {
	JobID           string
	JobCode         string
	Title           string
	CompanyName     string
	Declarations    []formschema.Declaration // the legal condition and purpose of each special category on the job
	Region          string                   // set by the caller from the registry
	TenantID        string                   // set by the caller from the registry
	Status          string
	ClosingDate     string // last day to apply, YYYY-MM-DD, or ""
	LawfulBasis     string
	RetentionMonths int
	Markets         []string // market codes the job is open to
	// Anywhere marks a job whose location is "anywhere": it has no market list and is open to candidates from every
	// country (their ISO code is still validated).
	Anywhere    bool
	FormID      string
	FormVersion int // newest frozen form version
	Questions   []formschema.FormQuestion
	Rules       []screening.Rule
}

// JobAccess is a job as access checks need it: who created it and the caller's role on it.
type JobAccess struct {
	JobID           string
	Code            string
	Title           string
	Status          string
	CreatedBy       string
	MemberRole      string // the caller's role in the job's team, "" when not a member
	RetentionMonths int
}

// JobSummary is a job as a status email needs it.
type JobSummary struct {
	Code            string
	Title           string
	Company         string
	PublicID        string
	RetentionMonths int
}

// ClosedJob is a closed or expired job and the date the retention clock of its undecided applications ends.
type ClosedJob struct {
	JobID string
	Until time.Time
}

// JobLabel names a job in a notice.
type JobLabel struct{ Code, Title string }
