package appstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/appevents"
	"github.com/Angle-HR/server/internal/hiring/apply"
)

// ---- taking an application (phase 3b) ----

var _ apply.Store = (*Store)(nil)

// The two unique indexes (one application per email per job, one idempotency key per job) make a repeat insert
// do nothing. ON CONFLICT DO NOTHING without a target covers both, and RETURNING gives no row for a repeat.
const insertApplicationSQL = `
INSERT INTO hiring.applications
    (id, tenant_id, job_id, form_id, form_version, candidate_name, candidate_email, candidate_country, candidate_market,
     answers, status, flags, source, idempotency_key, notice_version, notice_acknowledged_at, consent_given_at,
     submitted_at, retention_until, lawful_basis, collection_purpose, allowed_downstream_purposes,
     idempotency_fingerprint)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10::jsonb, $11, $12::jsonb, $13, NULLIF($14, ''), $15, $16,
        $17, $18, $19, $20, $21, $22::text[], NULLIF($23, ''))
ON CONFLICT DO NOTHING
RETURNING id::text`

const bumpApplicantCountSQL = `
INSERT INTO hiring.job_applicant_counts (job_id, tenant_id, applicant_count, new_applicant_count)
VALUES ($1::uuid, $2::uuid, 1, 1)
ON CONFLICT (job_id) DO UPDATE SET
    applicant_count = hiring.job_applicant_counts.applicant_count + 1,
    new_applicant_count = hiring.job_applicant_counts.new_applicant_count + 1`

const idempotentApplicationSQL = `
SELECT COALESCE(idempotency_fingerprint, '') FROM hiring.applications WHERE job_id = $1::uuid AND idempotency_key = $2`

// IdempotentApplication finds the application saved under a job's idempotency key. It implements apply.Store.
func (s *Store) IdempotentApplication(ctx context.Context, tenantID, jobID, key string) (string, bool, error) {
	var fp string
	found := false
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, idempotentApplicationSQL, jobID, key).Scan(&fp)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("appstore: look up idempotency key: %w", err)
		}
		found = true
		return nil
	})
	return fp, found, err
}

// InsertApplication saves an application and raises the job's counters in one transaction. It implements
// apply.Store. The audit row names the application and its status, never the candidate.
func (s *Store) InsertApplication(ctx context.Context, tenantID string, a *apply.NewApplication) (bool, error) {
	answers, err := json.Marshal(a.Answers)
	if err != nil {
		return false, fmt.Errorf("appstore: encode answers: %w", err)
	}
	flags := a.Flags
	if flags == nil {
		flags = []apply.Flag{}
	}
	flagsJSON, err := json.Marshal(flags)
	if err != nil {
		return false, fmt.Errorf("appstore: encode flags: %w", err)
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
			a.IdempotencyFingerprint,
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a repeat: nothing written, nothing counted
		}
		if err != nil {
			return fmt.Errorf("appstore: insert application: %w", mapPgError(err))
		}
		for _, f := range a.Files {
			tag, err := tx.Exec(ctx, attachFileSQL, id, a.RetentionUntil, f.ID, tenantID, a.JobID, f.QuestionID)
			if err != nil {
				return fmt.Errorf("appstore: attach file: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return &apply.FileError{QuestionID: f.QuestionID}
			}
		}
		for _, sa := range a.Sensitive {
			if _, err = tx.Exec(ctx, insertSpecialAnswerSQL, tenantID, id, sa.QuestionID, sa.Category, sa.LegalCondition,
				sa.Sealed.KeyID, sa.Sealed.Nonce, sa.Sealed.Ciphertext, a.RetentionUntil, a.LawfulBasis, sa.Purpose); err != nil {
				return fmt.Errorf("appstore: save special category answer: %w", mapPgError(err))
			}
		}
		// The application starts in the job's Applied stage, and the timeline starts with its arrival.
		if err = ensureStages(ctx, tx, tenantID, a.JobID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE hiring.applications SET stage_id = (SELECT id FROM hiring.app_job_stages
			WHERE tenant_id = $2::uuid AND job_id = $3::uuid AND kind = 'applied') WHERE id = $1::uuid AND tenant_id = $2::uuid`,
			id, tenantID, a.JobID); err != nil {
			return fmt.Errorf("appstore: place application: %w", err)
		}
		if err = insertEvent(ctx, tx, tenantID, a.JobID, id, "", "applied", map[string]any{"status": a.Status, "source": a.Source}); err != nil {
			return err
		}
		if a.Status == apply.StatusFlagged {
			if _, err = tx.Exec(ctx, insertReviewTaskSQL, tenantID, a.JobID, id, flagsJSON, a.RetentionUntil, a.LawfulBasis); err != nil {
				return fmt.Errorf("appstore: create review task: %w", err)
			}
		}
		if c := a.Confirmation; c != nil {
			if err = insertOutbox(ctx, tx, tenantID, id, c, a.SubmittedAt, a.LawfulBasis); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, bumpApplicantCountSQL, a.JobID, tenantID); err != nil {
			return fmt.Errorf("appstore: count applicant: %w", err)
		}
		diff, err := json.Marshal(map[string]any{"application_id": id, "status": a.Status, "source": a.Source})
		if err != nil {
			return fmt.Errorf("appstore: encode audit: %w", err)
		}
		if _, err = tx.Exec(ctx, insertAuditSQL, a.JobID, tenantID, "", "application.received", diff); err != nil {
			return fmt.Errorf("appstore: audit application: %w", err)
		}
		if err = appevents.EnqueueSubmitted(ctx, tx, appevents.Submitted{
			ApplicationID: id, JobID: a.JobID, CompanyID: tenantID, Region: s.Region, Status: a.Status,
			Source: a.Source, SubmittedAt: a.SubmittedAt,
		}); err != nil {
			return fmt.Errorf("appstore: emit submitted event: %w", err)
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
		return fmt.Errorf("appstore: encode email data: %w", err)
	}
	if string(data) == "null" {
		data = []byte("{}")
	}
	if _, err = tx.Exec(ctx, insertOutboxSQL, tenantID, applicationID, e.Kind, e.Recipient, e.RecipientName, data,
		now.Add(OutboxRetention), basis); err != nil {
		return fmt.Errorf("appstore: queue email: %w", err)
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
			return fmt.Errorf("appstore: check reapply block: %w", err)
		}
		return nil
	})
	return blocked, err
}
