package jobmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/formschema/screening"
)

// DB is the part of a pgx pool the model reads and writes; *pgxpool.Pool and dbrouter.PgxPool satisfy it.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrNoFallback means a method the events cannot answer was called on a Model that has no Fallback.
var ErrNoFallback = errors.New("jobmodel: no fallback job source configured")

// Model implements appjobs.JobSource from the read model. See docs/microservices/applications-jobmodel.md for which
// methods are answered locally and which are delegated to Fallback.
type Model struct {
	// Pool returns a region's database (the one holding hiring.app_jobs).
	Pool func(region string) (DB, error)
	// Fallback answers what the events cannot (JobAccess, TenantIDs, LookupJob, a notice recipient with no privacy
	// contact) and, with ReadThrough, jobs the model does not know yet. Today the DB adapter (jobsource.Source);
	// long term HiringService gRPC. May be nil: delegated calls then return ErrNoFallback.
	Fallback appjobs.JobSource
	// ReadThrough lets the model ask Fallback about a job it has no row for. LoadForApply then writes the answer
	// into the model so the next call is local; JobSummary, FrozenQuestions, JobLabels and ClosedJobs only read
	// through (they cannot build a complete row).
	ReadThrough bool

	// Index answers LookupJob (public id -> region, company) from the global read model. Without it, or for an id
	// it has not seen, LookupJob goes to Fallback (and the answer is remembered when Index is also an IndexWriter).
	Index IndexReader
	// Tenants lists a region's company ids (OrgService.ListCompanies). Without it TenantIDs is delegated.
	Tenants func(ctx context.Context, region string) ([]string, error)
	// Notice returns where a company's notices go and its name (OrgService.GetCompanyContact). It is the first
	// choice for NoticeRecipient; when it fails the privacy contact of the newest event is used, then Fallback.
	Notice func(ctx context.Context, region, tenantID string) (email, company string, err error)
	// Stats counts every use of Fallback. May be nil.
	Stats *Stats
}

var _ appjobs.JobSource = (*Model)(nil)

// LookupJob implements appjobs.Registry. The global job registry is hiring's; it is always delegated.
func (m *Model) LookupJob(ctx context.Context, publicID string) (appjobs.Ref, error) {
	if m.Index != nil {
		ref, err := m.Index.GetJob(ctx, publicID)
		if err == nil || !errors.Is(err, apperror.ErrNotFound) {
			return ref, err
		}
	}
	if m.Fallback == nil {
		if m.Index != nil {
			return appjobs.Ref{}, apperror.ErrNotFound
		}
		return appjobs.Ref{}, ErrNoFallback
	}
	if m.Index != nil {
		m.Stats.hit("lookup_job", "unindexed")
	} else {
		m.Stats.hit("lookup_job", "no_index")
	}
	ref, err := m.Fallback.LookupJob(ctx, publicID)
	if err == nil {
		if w, ok := m.Index.(IndexWriter); ok {
			// Version 0: any real event replaces it. Best effort.
			_ = w.PutJob(ctx, ref.PublicID, ref.Region, ref.OrganizationID, "00000000-0000-0000-0000-000000000000", 0)
		}
	}
	return ref, err
}

// Regional implements appjobs.JobSource.
func (m *Model) Regional(region string) (appjobs.RegionalJobs, error) {
	db, err := m.Pool(region)
	if err != nil {
		return nil, err
	}
	return &Regional{m: m, region: region, db: db}, nil
}

// Regional is the model of one region.
type Regional struct {
	m      *Model
	region string
	db     DB
}

var _ appjobs.RegionalJobs = (*Regional)(nil)

func (r *Regional) fallback() (appjobs.RegionalJobs, error) {
	if r.m.Fallback == nil {
		return nil, ErrNoFallback
	}
	return r.m.Fallback.Regional(r.region)
}

// onMiss is the fallback for a read the model could not answer because it has no row; nil when read-through is off.
func (r *Regional) onMiss() (appjobs.RegionalJobs, bool) {
	if !r.m.ReadThrough || r.m.Fallback == nil {
		return nil, false
	}
	r.m.Stats.hit("read_through", "miss")
	fb, err := r.m.Fallback.Regional(r.region)
	return fb, err == nil
}

const loadForApplySQL = `
SELECT job_id::text, job_code, title, company_name, declarations, status, COALESCE(to_char(closing_date, 'YYYY-MM-DD'), ''),
       lawful_basis, retention_months, markets, form_id, form_version, form_schema, rules, location_mode
FROM hiring.app_jobs WHERE public_id = $1 AND company_id = $2::uuid`

type frozenSchema struct {
	Questions []formschema.FormQuestion `json:"questions"`
}

// LoadForApply implements appjobs.RegionalJobs from the model. The stored status and closing date are returned
// as they are, so the caller's formschema.StateOf decides open, paused or closed exactly as with jobsource. A job
// that never published has no frozen form and is not found, as in jobsource.
func (r *Regional) LoadForApply(ctx context.Context, tenantID, publicID string) (*appjobs.ApplyTarget, error) {
	t, err := r.loadLocal(ctx, tenantID, publicID)
	if !errors.Is(err, apperror.ErrNotFound) {
		return t, err
	}
	fb, ok := r.onMiss()
	if !ok {
		return nil, err
	}
	got, ferr := fb.LoadForApply(ctx, tenantID, publicID)
	if ferr != nil {
		return nil, ferr
	}
	// Writing is best effort: the caller gets the answer even when the model cannot be updated.
	_ = r.Remember(ctx, tenantID, publicID, got)
	return got, nil
}

func (r *Regional) loadLocal(ctx context.Context, tenantID, publicID string) (*appjobs.ApplyTarget, error) {
	t := &appjobs.ApplyTarget{}
	var declarations, rules []byte
	var schema []byte
	var formID *string
	var mode string
	err := r.db.QueryRow(ctx, loadForApplySQL, publicID, tenantID).Scan(&t.JobID, &t.JobCode, &t.Title, &t.CompanyName,
		&declarations, &t.Status, &t.ClosingDate, &t.LawfulBasis, &t.RetentionMonths, &t.Markets, &formID,
		&t.FormVersion, &schema, &rules, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperror.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("jobmodel: load job for apply: %w", err)
	}
	if formID == nil || schema == nil {
		return nil, apperror.ErrNotFound // never published: no frozen form
	}
	t.FormID = *formID
	if t.Markets == nil {
		t.Markets = []string{}
	}
	t.Anywhere = mode == "anywhere" // same rule as jobsource; rows from before events carried the mode are not anywhere
	t.Declarations = []formschema.Declaration{}
	if err := json.Unmarshal(declarations, &t.Declarations); err != nil {
		return nil, fmt.Errorf("jobmodel: decode declarations: %w", err)
	}
	t.Rules = []screening.Rule{}
	if err := json.Unmarshal(rules, &t.Rules); err != nil {
		return nil, fmt.Errorf("jobmodel: decode rules: %w", err)
	}
	var fs frozenSchema
	if err := json.Unmarshal(schema, &fs); err != nil {
		return nil, fmt.Errorf("jobmodel: decode frozen form: %w", err)
	}
	t.Questions = fs.Questions
	return t, nil
}

// Remember writes what a Fallback returned for apply into the model (read-through). The row has version 0, so the
// next real event replaces it, and it never overwrites a row an event wrote. The company's privacy contact is not
// part of an ApplyTarget, so it stays empty and NoticeRecipient keeps delegating until an event arrives.
func (r *Regional) Remember(ctx context.Context, tenantID, publicID string, t *appjobs.ApplyTarget) error {
	q, err := json.Marshal(frozenSchema{Questions: t.Questions})
	if err != nil {
		return err
	}
	rules, err := json.Marshal(nonNil(t.Rules))
	if err != nil {
		return err
	}
	decls, err := json.Marshal(nonNilDecl(t.Declarations))
	if err != nil {
		return err
	}
	notice, _ := json.Marshal(map[string]any{
		"version": formschema.NoticeVersion, "lawful_basis": t.LawfulBasis, "declarations": nonNilDecl(t.Declarations),
		"screening_active": len(t.Rules) > 0,
	})
	number, _ := strconv.Atoi(strings.TrimPrefix(t.JobCode, "JB-"))
	state, ok := formschema.StateOf(t.Status, t.ClosingDate, time.Now())
	if !ok {
		state = formschema.StateClosed
	}
	formID := t.FormID
	locationMode := ""
	if t.Anywhere {
		locationMode = "anywhere"
	}
	row := row{
		LocationMode: locationMode,
		JobID:        t.JobID, PublicID: publicID, CompanyID: tenantID, Region: r.region, Status: t.Status, PublicState: string(state),
		ClosingDate: t.ClosingDate, RetentionMonths: t.RetentionMonths, JobNumber: number, JobCode: t.JobCode,
		Title: t.Title, LawfulBasis: t.LawfulBasis, Markets: nonNilStr(t.Markets), FormID: &formID,
		FormVersion: t.FormVersion, FormSchema: q, Rules: rules, Declarations: decls, Notice: notice,
		CompanyName: t.CompanyName, Version: 0, Source: SourceReadThrough, UpdatedAt: time.Now(),
	}
	if t.Status == "closed" || t.Status == "expired" {
		now := time.Now()
		row.ClosedAt = &now
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("jobmodel: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := upsert(ctx, tx, row); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nonNil(r []screening.Rule) []screening.Rule {
	if r == nil {
		return []screening.Rule{}
	}
	return r
}

func nonNilDecl(d []formschema.Declaration) []formschema.Declaration {
	if d == nil {
		return []formschema.Declaration{}
	}
	return d
}

func nonNilStr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

const jobAccessSQL = `
SELECT job_code, title, status, COALESCE(created_by::text, ''), retention_months
FROM hiring.app_jobs WHERE job_id = $1::uuid AND company_id = $2::uuid`

const jobMembersSQL = `SELECT members FROM hiring.app_job_members WHERE job_id = $1::uuid AND company_id = $2::uuid`

// Member is one person of a job's hiring team.
type Member struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// JobAccess implements appjobs.RegionalJobs from the model: the job row (with the creator) and the team the last
// members_changed event left. A job the model has no row for, or a row from before events carried the creator
// (created_by NULL), is delegated to the Fallback: access must not be guessed. A job with no team row has an empty
// team, which is what hiring holds until the first members change.
func (r *Regional) JobAccess(ctx context.Context, tenantID, jobID, userID string) (*appjobs.JobAccess, error) {
	a := &appjobs.JobAccess{}
	err := r.db.QueryRow(ctx, jobAccessSQL, jobID, tenantID).
		Scan(&a.Code, &a.Title, &a.Status, &a.CreatedBy, &a.RetentionMonths)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return r.accessFromFallback(ctx, tenantID, jobID, userID, "no_job")
	case err != nil:
		return nil, fmt.Errorf("jobmodel: job access: %w", err)
	case a.CreatedBy == "":
		return r.accessFromFallback(ctx, tenantID, jobID, userID, "no_creator")
	}
	a.JobID = jobID
	var raw []byte
	err = r.db.QueryRow(ctx, jobMembersSQL, jobID, tenantID).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("jobmodel: job members: %w", err)
	}
	if err == nil {
		var team []Member
		if err := json.Unmarshal(raw, &team); err != nil {
			return nil, fmt.Errorf("jobmodel: decode job members: %w", err)
		}
		for _, m := range team {
			if m.UserID == userID {
				a.MemberRole = m.Role
				break
			}
		}
	}
	return a, nil
}

func (r *Regional) accessFromFallback(ctx context.Context, tenantID, jobID, userID, reason string) (*appjobs.JobAccess, error) {
	fb, err := r.fallback()
	if err != nil {
		if errors.Is(err, ErrNoFallback) && reason == "no_job" {
			return nil, apperror.ErrNotFound
		}
		return nil, err
	}
	r.m.Stats.hit("job_access", reason)
	return fb.JobAccess(ctx, tenantID, jobID, userID)
}

// TenantIDs implements appjobs.RegionalJobs. With Tenants set (org gRPC) org answers; otherwise it is delegated,
// because the model only knows companies that have a job.
func (r *Regional) TenantIDs(ctx context.Context) ([]string, error) {
	if r.m.Tenants != nil {
		return r.m.Tenants(ctx, r.region)
	}
	fb, err := r.fallback()
	if err != nil {
		return nil, err
	}
	r.m.Stats.hit("tenant_ids", "no_org")
	return fb.TenantIDs(ctx)
}

const jobSummarySQL = `
SELECT job_code, title, company_name, public_id, retention_months
FROM hiring.app_jobs WHERE job_id = $1::uuid AND company_id = $2::uuid`

// JobSummary implements appjobs.RegionalJobs.
func (r *Regional) JobSummary(ctx context.Context, tenantID, jobID string) (*appjobs.JobSummary, error) {
	var j appjobs.JobSummary
	err := r.db.QueryRow(ctx, jobSummarySQL, jobID, tenantID).Scan(&j.Code, &j.Title, &j.Company, &j.PublicID, &j.RetentionMonths)
	if errors.Is(err, pgx.ErrNoRows) {
		if fb, ok := r.onMiss(); ok {
			return fb.JobSummary(ctx, tenantID, jobID)
		}
		return nil, apperror.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("jobmodel: job summary: %w", err)
	}
	return &j, nil
}

const frozenQuestionsSQL = `
SELECT schema FROM hiring.app_job_forms WHERE form_id = $1 AND version = $2 AND company_id = $3::uuid`

// FrozenQuestions implements appjobs.RegionalJobs from hiring.app_job_forms, which keeps every frozen version seen.
// A version older than the model's first event for the job is read through the Fallback when read-through is on.
func (r *Regional) FrozenQuestions(ctx context.Context, tenantID, formID string, version int) ([]formschema.FormQuestion, error) {
	var raw []byte
	err := r.db.QueryRow(ctx, frozenQuestionsSQL, formID, version, tenantID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		if fb, ok := r.onMiss(); ok {
			return fb.FrozenQuestions(ctx, tenantID, formID, version)
		}
		return nil, apperror.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("jobmodel: load frozen form: %w", err)
	}
	var fs frozenSchema
	if err := json.Unmarshal(raw, &fs); err != nil {
		return nil, fmt.Errorf("jobmodel: decode frozen form: %w", err)
	}
	return fs.Questions, nil
}

// Same clock as jobsource: the close time (or the day after the closing date, or now) plus the retention months.
const closedJobsSQL = `
SELECT job_id::text,
       COALESCE(closed_at, (closing_date + 1)::timestamptz, $2::timestamptz)
           + make_interval(months => retention_months::int)
FROM hiring.app_jobs
WHERE company_id = $1::uuid AND status IN ('closed', 'expired')`

// ClosedJobs implements appjobs.RegionalJobs. With read-through on, jobs the Fallback lists that the model does
// not hold are added: retention must err towards completeness while the model is being filled.
func (r *Regional) ClosedJobs(ctx context.Context, tenantID string, now time.Time) ([]appjobs.ClosedJob, error) {
	rows, err := r.db.Query(ctx, closedJobsSQL, tenantID, now)
	if err != nil {
		return nil, fmt.Errorf("jobmodel: list closed jobs: %w", err)
	}
	var out []appjobs.ClosedJob
	seen := map[string]bool{}
	for rows.Next() {
		var c appjobs.ClosedJob
		if err = rows.Scan(&c.JobID, &c.Until); err != nil {
			rows.Close()
			return nil, fmt.Errorf("jobmodel: scan closed job: %w", err)
		}
		seen[c.JobID] = true
		out = append(out, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("jobmodel: list closed jobs: %w", err)
	}
	if fb, ok := r.onMiss(); ok {
		more, err := fb.ClosedJobs(ctx, tenantID, now)
		if err != nil {
			return nil, err
		}
		for _, c := range more {
			if !seen[c.JobID] {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

const jobLabelsSQL = `
SELECT job_id::text, job_code, title FROM hiring.app_jobs WHERE company_id = $1::uuid AND job_id = ANY($2::uuid[])`

// JobLabels implements appjobs.RegionalJobs. Ids the model lacks are asked of the Fallback when read-through is on.
func (r *Regional) JobLabels(ctx context.Context, tenantID string, jobIDs []string) (map[string]appjobs.JobLabel, error) {
	out := map[string]appjobs.JobLabel{}
	rows, err := r.db.Query(ctx, jobLabelsSQL, tenantID, jobIDs)
	if err != nil {
		return nil, fmt.Errorf("jobmodel: job labels: %w", err)
	}
	for rows.Next() {
		var id string
		var l appjobs.JobLabel
		if err = rows.Scan(&id, &l.Code, &l.Title); err != nil {
			rows.Close()
			return nil, fmt.Errorf("jobmodel: scan job label: %w", err)
		}
		out[id] = l
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("jobmodel: job labels: %w", err)
	}
	var missing []string
	for _, id := range jobIDs {
		if _, ok := out[id]; !ok {
			missing = append(missing, id)
		}
	}
	if fb, ok := r.onMiss(); ok && len(missing) > 0 {
		more, err := fb.JobLabels(ctx, tenantID, missing)
		if err != nil {
			return nil, err
		}
		for id, l := range more {
			out[id] = l
		}
	}
	return out, nil
}

// The privacy contact of the company as its most recently changed job last saw it.
const noticeRecipientSQL = `
SELECT privacy_email, company_name FROM hiring.app_jobs
WHERE company_id = $1::uuid AND source = 'event'
ORDER BY version DESC, updated_at DESC LIMIT 1`

// NoticeRecipient implements appjobs.RegionalJobs: the privacy contact, else the owner. With Notice set (org
// gRPC) org answers first, because it is authoritative and the events only carry the privacy contact as of the
// company's newest job event. Without it, or when org fails, the privacy contact of the newest event is used; a
// company without one is delegated to the Fallback, which knows the owner.
func (r *Regional) NoticeRecipient(ctx context.Context, tenantID string) (string, string, error) {
	if r.m.Notice != nil {
		if email, company, err := r.m.Notice(ctx, r.region, tenantID); err == nil && email != "" {
			return email, company, nil
		}
	}
	var email, company string
	err := r.db.QueryRow(ctx, noticeRecipientSQL, tenantID).Scan(&email, &company)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("jobmodel: find notice recipient: %w", err)
	}
	if err == nil && email != "" {
		return email, company, nil
	}
	fb, ferr := r.fallback()
	if ferr != nil {
		return "", "", ferr
	}
	r.m.Stats.hit("notice_recipient", "no_contact")
	return fb.NoticeRecipient(ctx, tenantID)
}
