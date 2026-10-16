package hiringstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/hiring/screening"
)

// ---- public job pages and applications (phase 3a, 3b) ----

// publicJobIDSQL finds a job by its public id inside one company. The public id is unique across companies,
// but the tenant filter still applies: row-level security would show nothing without it.
const publicJobIDSQL = `
SELECT id::text FROM hiring.job_postings
WHERE public_id = $1 AND tenant_id = $2::uuid AND deleted_at IS NULL`

// frozenFormSQL reads the newest frozen form of a job: what candidates apply with. The editable form can change
// after a publish, and must not leak into the live page or the validation of an application.
const frozenFormSQL = `
SELECT f.id::text, v.version, v.schema
FROM hiring.application_forms f
JOIN LATERAL (
    SELECT version, schema FROM hiring.form_versions WHERE form_id = f.id ORDER BY version DESC LIMIT 1
) v ON TRUE
WHERE f.job_id = $1::uuid AND f.tenant_id = $2::uuid`

const publicCompanySQL = `
SELECT legal_name, COALESCE(privacy_contact_email, ''), COALESCE(dpo_contact, '')
FROM accounts.organizations WHERE id = $1::uuid`

// frozenSchema is the shape freezeFormSQL writes: the form's questions as they were at publish.
type frozenSchema struct {
	Questions []questions.FormQuestion `json:"questions"`
}

// loadFrozenForm returns the form id, newest version and questions. ErrNotFound when the job never published.
func loadFrozenForm(ctx context.Context, tx pgx.Tx, tenantID, jobID string) (string, int, []questions.FormQuestion, error) {
	var (
		formID  string
		version int
		raw     []byte
	)
	err := tx.QueryRow(ctx, frozenFormSQL, jobID, tenantID).Scan(&formID, &version, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, nil, ErrNotFound
	}
	if err != nil {
		return "", 0, nil, fmt.Errorf("hiringstore: load frozen form: %w", err)
	}
	var schema frozenSchema
	if err = json.Unmarshal(raw, &schema); err != nil {
		return "", 0, nil, fmt.Errorf("hiringstore: decode frozen form: %w", err)
	}
	if schema.Questions == nil {
		schema.Questions = []questions.FormQuestion{}
	}
	return formID, version, schema.Questions, nil
}

// LoadPosting reads a published job for the public page. It implements public.Regional.
func (s *Store) LoadPosting(ctx context.Context, tenantID, publicID string) (*public.Posting, error) {
	var out *public.Posting
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var jobID string
		if err := tx.QueryRow(ctx, publicJobIDSQL, publicID, tenantID).Scan(&jobID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("hiringstore: find public job: %w", err)
		}
		rec, err := loadRecord(ctx, tx, tenantID, jobID, false)
		if err != nil {
			return err
		}
		_, version, form, err := loadFrozenForm(ctx, tx, tenantID, jobID)
		if err != nil {
			return err
		}
		p := &public.Posting{
			Job: rec.Job, Form: form, FormVersion: version, Declarations: rec.Declarations,
			ScreeningRules: len(rec.Rules) > 0,
		}
		if err = tx.QueryRow(ctx, publicCompanySQL, tenantID).
			Scan(&p.Company.Name, &p.Company.PrivacyEmail, &p.Company.DPOContact); err != nil {
			return fmt.Errorf("hiringstore: load company: %w", err)
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const applyMarketsSQL = `
SELECT DISTINCT market_code FROM hiring.job_markets WHERE job_id = $1::uuid ORDER BY market_code`

const applyJobSQL = `
SELECT id::text, job_number, status, COALESCE(to_char(closing_date, 'YYYY-MM-DD'), ''),
       COALESCE(lawful_basis, ''), retention_months, title
FROM hiring.job_postings
WHERE public_id = $1 AND tenant_id = $2::uuid AND deleted_at IS NULL`

// LoadForApply reads what an application is checked against: the job's state, its markets, its newest frozen
// form and its screening rules. It implements apply.Store.
func (s *Store) LoadForApply(ctx context.Context, tenantID, publicID string) (*apply.Target, error) {
	var t *apply.Target
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		got := &apply.Target{}
		var number int
		err := tx.QueryRow(ctx, applyJobSQL, publicID, tenantID).Scan(
			&got.JobID, &number, &got.Status, &got.ClosingDate, &got.LawfulBasis, &got.RetentionMonths, &got.Title)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("hiringstore: load job for apply: %w", err)
		}
		got.JobCode = fmt.Sprintf("JB-%d", number)
		var privacyEmail, dpo string
		if err = tx.QueryRow(ctx, publicCompanySQL, tenantID).Scan(&got.CompanyName, &privacyEmail, &dpo); err != nil {
			return fmt.Errorf("hiringstore: load company for apply: %w", err)
		}
		if got.Declarations, err = loadDeclarations(ctx, tx, got.JobID); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, applyMarketsSQL, got.JobID)
		if err != nil {
			return fmt.Errorf("hiringstore: load markets for apply: %w", err)
		}
		got.Markets = []string{}
		for rows.Next() {
			var m string
			if err = rows.Scan(&m); err != nil {
				rows.Close()
				return fmt.Errorf("hiringstore: scan market: %w", err)
			}
			got.Markets = append(got.Markets, m)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return fmt.Errorf("hiringstore: load markets for apply: %w", err)
		}

		if got.FormID, got.FormVersion, got.Questions, err = loadFrozenForm(ctx, tx, tenantID, got.JobID); err != nil {
			return err
		}

		rules, err := tx.Query(ctx, loadRulesSQL, got.JobID)
		if err != nil {
			return fmt.Errorf("hiringstore: load rules for apply: %w", err)
		}
		got.Rules = []screening.Rule{}
		for rules.Next() {
			var r screening.Rule
			var value []byte
			if err = rules.Scan(&r.ID, &r.QuestionID, &r.Operator, &value, &r.Reason); err != nil {
				rules.Close()
				return fmt.Errorf("hiringstore: scan rule: %w", err)
			}
			r.Value = json.RawMessage(value)
			got.Rules = append(got.Rules, r)
		}
		rules.Close()
		if err = rules.Err(); err != nil {
			return fmt.Errorf("hiringstore: load rules for apply: %w", err)
		}
		t = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// The two unique indexes (one application per email per job, one idempotency key per job) make a repeat insert
// do nothing. ON CONFLICT DO NOTHING without a target covers both, and RETURNING gives no row for a repeat.
const insertApplicationSQL = `
INSERT INTO hiring.applications
    (id, tenant_id, job_id, form_id, form_version, candidate_name, candidate_email, candidate_country, candidate_market,
     answers, status, flags, source, idempotency_key, notice_version, notice_acknowledged_at, consent_given_at,
     submitted_at, retention_until, lawful_basis, collection_purpose, allowed_downstream_purposes)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10::jsonb, $11, $12::jsonb, $13, NULLIF($14, ''), $15, $16,
        $17, $18, $19, $20, $21, $22::text[])
ON CONFLICT DO NOTHING
RETURNING id::text`

const bumpApplicantCountSQL = `
INSERT INTO hiring.job_applicant_counts (job_id, tenant_id, applicant_count, new_applicant_count)
VALUES ($1::uuid, $2::uuid, 1, 1)
ON CONFLICT (job_id) DO UPDATE SET
    applicant_count = hiring.job_applicant_counts.applicant_count + 1,
    new_applicant_count = hiring.job_applicant_counts.new_applicant_count + 1`

// InsertApplication saves an application and raises the job's counters in one transaction. It implements
// apply.Store. The audit row names the application and its status, never the candidate.
func (s *Store) InsertApplication(ctx context.Context, tenantID string, a *apply.NewApplication) (bool, error) {
	answers, err := json.Marshal(a.Answers)
	if err != nil {
		return false, fmt.Errorf("hiringstore: encode answers: %w", err)
	}
	flags := a.Flags
	if flags == nil {
		flags = []apply.Flag{}
	}
	flagsJSON, err := json.Marshal(flags)
	if err != nil {
		return false, fmt.Errorf("hiringstore: encode flags: %w", err)
	}
	purposes := a.DownstreamPurposes
	if purposes == nil {
		purposes = []string{}
	}
	created := false
	err = s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, insertApplicationSQL,
			a.ID, tenantID, a.JobID, a.FormID, a.FormVersion, a.Name, a.Email, a.Country, a.Market,
			answers, a.Status, flagsJSON, a.Source, a.IdempotencyKey, a.NoticeVersion, a.NoticeAcknowledgedAt,
			a.ConsentAt, a.SubmittedAt, a.RetentionUntil, a.LawfulBasis, a.CollectionPurpose, purposes,
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a repeat: nothing written, nothing counted
		}
		if err != nil {
			return fmt.Errorf("hiringstore: insert application: %w", mapPgError(err))
		}
		for _, f := range a.Files {
			tag, err := tx.Exec(ctx, attachFileSQL, id, a.RetentionUntil, f.ID, tenantID, a.JobID, f.QuestionID)
			if err != nil {
				return fmt.Errorf("hiringstore: attach file: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return &apply.FileError{QuestionID: f.QuestionID}
			}
		}
		for _, sa := range a.Sensitive {
			if _, err = tx.Exec(ctx, insertSpecialAnswerSQL, tenantID, id, sa.QuestionID, sa.Category, sa.LegalCondition,
				sa.Sealed.KeyID, sa.Sealed.Nonce, sa.Sealed.Ciphertext, a.RetentionUntil, a.LawfulBasis, sa.Purpose); err != nil {
				return fmt.Errorf("hiringstore: save special category answer: %w", mapPgError(err))
			}
		}
		if a.Status == apply.StatusFlagged {
			if _, err = tx.Exec(ctx, insertReviewTaskSQL, tenantID, a.JobID, id, flagsJSON, a.RetentionUntil, a.LawfulBasis); err != nil {
				return fmt.Errorf("hiringstore: create review task: %w", err)
			}
		}
		if c := a.Confirmation; c != nil {
			if err = insertOutbox(ctx, tx, tenantID, id, c, a.SubmittedAt, a.LawfulBasis); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, bumpApplicantCountSQL, a.JobID, tenantID); err != nil {
			return fmt.Errorf("hiringstore: count applicant: %w", err)
		}
		diff, err := json.Marshal(map[string]any{"application_id": id, "status": a.Status, "source": a.Source})
		if err != nil {
			return fmt.Errorf("hiringstore: encode audit: %w", err)
		}
		if _, err = tx.Exec(ctx, insertAuditSQL, a.JobID, tenantID, "", "application.received", diff); err != nil {
			return fmt.Errorf("hiringstore: audit application: %w", err)
		}
		created = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

// Special category answers sit in their own schema, encrypted. Only the ciphertext and the metadata that says what
// it is and why it may be held are stored; the application's retention date applies to it too.
const insertSpecialAnswerSQL = `
INSERT INTO hiring_restricted.special_answers
    (tenant_id, application_id, question_id, category, legal_condition, key_id, nonce, ciphertext, retention_until,
     lawful_basis, collection_purpose)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

// A flagged application gets a review task: a person must open it before the flag is decided.
const insertReviewTaskSQL = `
INSERT INTO hiring.review_tasks (tenant_id, job_id, application_id, reasons, retention_until, lawful_basis, collection_purpose)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::jsonb, $5, $6, 'Reviewing an application that a screening rule or knockout flagged')`

const insertOutboxSQL = `
INSERT INTO hiring.email_outbox
    (tenant_id, application_id, kind, recipient, recipient_name, data, retention_until, lawful_basis, collection_purpose)
VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6::jsonb, $7, $8, 'Sending a message about your application')`

// OutboxRetention is how long an email waits in the outbox, sent or not, before the retention task deletes it.
const OutboxRetention = 7 * 24 * time.Hour

func insertOutbox(ctx context.Context, tx pgx.Tx, tenantID, applicationID string, e *apply.OutboxEmail, now time.Time, basis string) error {
	data, err := json.Marshal(e.Data)
	if err != nil {
		return fmt.Errorf("hiringstore: encode email data: %w", err)
	}
	if string(data) == "null" {
		data = []byte("{}")
	}
	if _, err = tx.Exec(ctx, insertOutboxSQL, tenantID, applicationID, e.Kind, e.Recipient, e.RecipientName, data,
		now.Add(OutboxRetention), basis); err != nil {
		return fmt.Errorf("hiringstore: queue email: %w", err)
	}
	return nil
}

const reapplyBlockedSQL = `
SELECT EXISTS (SELECT 1 FROM hiring.reapply_blocks
               WHERE tenant_id = $1::uuid AND email_hmac = $2 AND blocked_until > now())`

// ReapplyBlocked reports whether a candidate (keyed hash of the email) may not apply to the company now. It
// implements apply.Store.
func (s *Store) ReapplyBlocked(ctx context.Context, tenantID, emailHMAC string) (bool, error) {
	blocked := false
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, reapplyBlockedSQL, tenantID, emailHMAC).Scan(&blocked); err != nil {
			return fmt.Errorf("hiringstore: check reapply block: %w", err)
		}
		return nil
	})
	return blocked, err
}
