// Package jobsource implements appjobs.JobSource over the tables hiring owns (job_postings, job_members, the
// frozen form versions, disqualification rules) and the company's organization row. Nothing else reads those
// tables on behalf of the applications side, so this package is the only thing that changes when the applications
// service gets its own event-fed read model.
package jobsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/formschema/screening"
)

// Source is the job source of the monolith: the global registry for public ids, one regional pool per region.
type Source struct {
	Global *hiringstore.Global
	// Pool returns a region's database.
	Pool func(region string) (hiringstore.DB, error)
}

var _ appjobs.JobSource = (*Source)(nil)

// LookupJob implements appjobs.Registry.
func (s *Source) LookupJob(ctx context.Context, publicID string) (appjobs.Ref, error) {
	e, err := s.Global.LookupJob(ctx, publicID)
	if err != nil {
		return appjobs.Ref{}, err
	}
	return appjobs.Ref{PublicID: e.PublicID, Region: e.Region, OrganizationID: e.OrganizationID}, nil
}

// Regional implements appjobs.JobSource.
func (s *Source) Regional(region string) (appjobs.RegionalJobs, error) {
	pool, err := s.Pool(region)
	if err != nil {
		return nil, err
	}
	return NewRegional(pool), nil
}

// Regional is the job source of one region's database.
type Regional struct{ store *hiringstore.Store }

var _ appjobs.RegionalJobs = (*Regional)(nil)

// NewRegional returns the job source of the region that db belongs to.
func NewRegional(db hiringstore.DB) *Regional { return &Regional{store: &hiringstore.Store{DB: db}} }

const jobAccessSQL = `
SELECT j.id::text, 'JB-' || j.job_number::text, COALESCE(j.title, ''), j.status, j.created_by::text,
       COALESCE(m.role, ''), j.retention_months::int
FROM hiring.job_postings j
LEFT JOIN hiring.job_members m ON m.job_id = j.id AND m.user_id = $3::uuid
WHERE j.id = $1::uuid AND j.tenant_id = $2::uuid AND j.deleted_at IS NULL`

// JobAccess implements appjobs.RegionalJobs.
func (r *Regional) JobAccess(ctx context.Context, tenantID, jobID, userID string) (*appjobs.JobAccess, error) {
	var a appjobs.JobAccess
	err := r.store.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, jobAccessSQL, jobID, tenantID, userID).
			Scan(&a.JobID, &a.Code, &a.Title, &a.Status, &a.CreatedBy, &a.MemberRole, &a.RetentionMonths)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("jobsource: job access: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// The job and its company, for status emails. A deleted job is not found, as in the old join.
const jobSummarySQL = `
SELECT 'JB-' || j.job_number::text, COALESCE(j.title, ''), o.legal_name, COALESCE(j.public_id, ''), j.retention_months::int
FROM hiring.job_postings j
JOIN accounts.organizations o ON o.id = j.tenant_id
WHERE j.id = $1::uuid AND j.tenant_id = $2::uuid`

// JobSummary implements appjobs.RegionalJobs.
func (r *Regional) JobSummary(ctx context.Context, tenantID, jobID string) (*appjobs.JobSummary, error) {
	var j appjobs.JobSummary
	err := r.store.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, jobSummarySQL, jobID, tenantID).
			Scan(&j.Code, &j.Title, &j.Company, &j.PublicID, &j.RetentionMonths)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("jobsource: job summary: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &j, nil
}

const frozenQuestionsSQL = `
SELECT v.schema FROM hiring.form_versions v
JOIN hiring.application_forms f ON f.id = v.form_id
WHERE v.form_id = $1::uuid AND v.version = $2 AND f.tenant_id = $3::uuid`

// frozenSchema is the shape freezeFormSQL writes: the form's questions as they were at publish.
type frozenSchema struct {
	Questions []formschema.FormQuestion `json:"questions"`
}

// FrozenQuestions implements appjobs.RegionalJobs.
func (r *Regional) FrozenQuestions(ctx context.Context, tenantID, formID string, version int) ([]formschema.FormQuestion, error) {
	var out []formschema.FormQuestion
	err := r.store.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, frozenQuestionsSQL, formID, version, tenantID).Scan(&raw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperror.ErrNotFound
			}
			return fmt.Errorf("jobsource: load frozen form: %w", err)
		}
		var schema frozenSchema
		if err := json.Unmarshal(raw, &schema); err != nil {
			return fmt.Errorf("jobsource: decode frozen form: %w", err)
		}
		out = schema.Questions
		return nil
	})
	return out, err
}

const applyMarketsSQL = `
SELECT DISTINCT market_code FROM hiring.job_markets WHERE job_id = $1::uuid ORDER BY market_code`

const applyJobSQL = `
SELECT id::text, job_number, status, COALESCE(to_char(closing_date, 'YYYY-MM-DD'), ''),
       COALESCE(lawful_basis, ''), retention_months, title, COALESCE(location_mode, '')
FROM hiring.job_postings
WHERE public_id = $1 AND tenant_id = $2::uuid AND deleted_at IS NULL`

// LoadForApply implements appjobs.RegionalJobs: the job's state, markets, newest frozen form and screening rules.
func (r *Regional) LoadForApply(ctx context.Context, tenantID, publicID string) (*appjobs.ApplyTarget, error) {
	var t *appjobs.ApplyTarget
	err := r.store.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		got := &appjobs.ApplyTarget{}
		var number int
		var mode string
		err := tx.QueryRow(ctx, applyJobSQL, publicID, tenantID).Scan(
			&got.JobID, &number, &got.Status, &got.ClosingDate, &got.LawfulBasis, &got.RetentionMonths, &got.Title, &mode)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("jobsource: load job for apply: %w", err)
		}
		got.JobCode = fmt.Sprintf("JB-%d", number)
		got.Anywhere = mode == "anywhere"
		if got.CompanyName, _, _, err = hiringstore.LoadPublicCompany(ctx, tx, tenantID); err != nil {
			return fmt.Errorf("jobsource: load company for apply: %w", err)
		}
		if got.Declarations, err = hiringstore.LoadDeclarations(ctx, tx, got.JobID); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, applyMarketsSQL, got.JobID)
		if err != nil {
			return fmt.Errorf("jobsource: load markets for apply: %w", err)
		}
		got.Markets = []string{}
		for rows.Next() {
			var m string
			if err = rows.Scan(&m); err != nil {
				rows.Close()
				return fmt.Errorf("jobsource: scan market: %w", err)
			}
			got.Markets = append(got.Markets, m)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return fmt.Errorf("jobsource: load markets for apply: %w", err)
		}

		if got.FormID, got.FormVersion, got.Questions, err = hiringstore.LoadFrozenForm(ctx, tx, tenantID, got.JobID); err != nil {
			return err
		}

		rules, err := tx.Query(ctx, hiringstore.LoadRulesSQL, got.JobID)
		if err != nil {
			return fmt.Errorf("jobsource: load rules for apply: %w", err)
		}
		got.Rules = []screening.Rule{}
		for rules.Next() {
			var rule screening.Rule
			var value []byte
			if err = rules.Scan(&rule.ID, &rule.QuestionID, &rule.Operator, &value, &rule.Reason); err != nil {
				rules.Close()
				return fmt.Errorf("jobsource: scan rule: %w", err)
			}
			rule.Value = json.RawMessage(value)
			got.Rules = append(got.Rules, rule)
		}
		rules.Close()
		if err = rules.Err(); err != nil {
			return fmt.Errorf("jobsource: load rules for apply: %w", err)
		}
		t = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// An undecided application's clock starts when its job closes: the close date (or the day after the closing
// date, or now) plus the job's retention months.
const closedJobsSQL = `
SELECT j.id::text,
       COALESCE(j.closed_at, (j.closing_date + 1)::timestamptz, $2::timestamptz)
           + make_interval(months => j.retention_months::int)
FROM hiring.job_postings j
WHERE j.tenant_id = $1::uuid AND j.status IN ('closed', 'expired') AND j.deleted_at IS NULL`

// ClosedJobs implements appjobs.RegionalJobs.
func (r *Regional) ClosedJobs(ctx context.Context, tenantID string, now time.Time) ([]appjobs.ClosedJob, error) {
	var out []appjobs.ClosedJob
	err := r.store.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, closedJobsSQL, tenantID, now)
		if err != nil {
			return fmt.Errorf("jobsource: list closed jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c appjobs.ClosedJob
			if err = rows.Scan(&c.JobID, &c.Until); err != nil {
				return fmt.Errorf("jobsource: scan closed job: %w", err)
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

const jobLabelsSQL = `
SELECT id::text, 'JB-' || job_number::text, COALESCE(title, '')
FROM hiring.job_postings WHERE tenant_id = $1::uuid AND id = ANY($2::uuid[])`

// JobLabels implements appjobs.RegionalJobs.
func (r *Regional) JobLabels(ctx context.Context, tenantID string, jobIDs []string) (map[string]appjobs.JobLabel, error) {
	out := map[string]appjobs.JobLabel{}
	err := r.store.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, jobLabelsSQL, tenantID, jobIDs)
		if err != nil {
			return fmt.Errorf("jobsource: job labels: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var l appjobs.JobLabel
			if err = rows.Scan(&id, &l.Code, &l.Title); err != nil {
				return fmt.Errorf("jobsource: scan job label: %w", err)
			}
			out[id] = l
		}
		return rows.Err()
	})
	return out, err
}

// The notice goes to the company's privacy contact, or to its owner when none was given.
const noticeRecipientSQL = `
SELECT COALESCE(NULLIF(o.privacy_contact_email, ''), u.email), o.legal_name
FROM accounts.organizations o JOIN accounts.users u ON u.id = o.owner_user_id
WHERE o.id = $1::uuid`

// NoticeRecipient implements appjobs.RegionalJobs.
func (r *Regional) NoticeRecipient(ctx context.Context, tenantID string) (string, string, error) {
	var to, company string
	err := r.store.InTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, noticeRecipientSQL, tenantID).Scan(&to, &company); err != nil {
			return fmt.Errorf("jobsource: find notice recipient: %w", err)
		}
		return nil
	})
	return to, company, err
}

// TenantIDs implements appjobs.RegionalJobs.
func (r *Regional) TenantIDs(ctx context.Context) ([]string, error) { return r.store.TenantIDs(ctx) }
