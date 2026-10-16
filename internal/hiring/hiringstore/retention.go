package hiringstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/outbox"
	"github.com/Angle-HR/server/internal/hiring/retention"
)

// ---- retention (phase 3d) ----

// An application nobody decided keeps its provisional date (set when it arrived) until its job closes; then the
// clock is the job's close date (or closing date plus a day) plus the job's retention months. LEAST makes sure a
// date is only ever pulled back, never pushed out.
const shortenRetentionSQL = `
UPDATE hiring.applications a
SET retention_until = LEAST(a.retention_until, base.until)
FROM (
    SELECT j.id,
           COALESCE(j.closed_at, (j.closing_date + 1)::timestamptz, $2::timestamptz)
               + make_interval(months => j.retention_months::int) AS until
    FROM hiring.job_postings j
    WHERE j.tenant_id = $1::uuid AND j.status IN ('closed', 'expired') AND j.deleted_at IS NULL
) base
WHERE a.job_id = base.id AND a.tenant_id = $1::uuid
  AND a.status IN ('received', 'flagged', 'in_review') AND a.retention_until > base.until`

// Everything that belongs to an application follows its date.
const followRetentionSQL = `
WITH f AS (UPDATE hiring.application_files c SET retention_until = a.retention_until FROM hiring.applications a
           WHERE c.application_id = a.id AND c.tenant_id = $1::uuid AND c.retention_until > a.retention_until RETURNING 1),
     s AS (UPDATE hiring_restricted.special_answers c SET retention_until = a.retention_until FROM hiring.applications a
           WHERE c.application_id = a.id AND c.tenant_id = $1::uuid AND c.retention_until > a.retention_until RETURNING 1),
     r AS (UPDATE hiring.review_tasks c SET retention_until = a.retention_until FROM hiring.applications a
           WHERE c.application_id = a.id AND c.tenant_id = $1::uuid AND c.retention_until > a.retention_until RETURNING 1)
SELECT (SELECT count(*) FROM f) + (SELECT count(*) FROM s) + (SELECT count(*) FROM r)`

// ShortenForClosedJobs implements retention.Store. It returns the number of applications changed.
func (s *Store) ShortenForClosedJobs(ctx context.Context, tenantID string, now time.Time) (int, error) {
	n := 0
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, shortenRetentionSQL, tenantID, now)
		if err != nil {
			return fmt.Errorf("hiringstore: shorten retention: %w", err)
		}
		n = int(tag.RowsAffected())
		if n == 0 {
			return nil
		}
		var moved int
		if err = tx.QueryRow(ctx, followRetentionSQL, tenantID).Scan(&moved); err != nil {
			return fmt.Errorf("hiringstore: shorten retention of attached rows: %w", err)
		}
		return nil
	})
	return n, err
}

const dueApplicationsSQL = `
SELECT a.id::text, a.job_id::text, 'JB-' || j.job_number::text, j.title,
       COALESCE((SELECT array_agg(f.id::text ORDER BY f.id) FROM hiring.application_files f
                 WHERE f.application_id = a.id AND f.tenant_id = a.tenant_id), '{}')::text[]
FROM hiring.applications a
JOIN hiring.job_postings j ON j.id = a.job_id AND j.tenant_id = a.tenant_id
WHERE a.tenant_id = $1::uuid AND a.retention_until < $2
ORDER BY a.retention_until, a.id
LIMIT $3`

// DueApplications implements retention.Store.
func (s *Store) DueApplications(ctx context.Context, tenantID string, now time.Time, limit int) ([]retention.Due, error) {
	var out []retention.Due
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, dueApplicationsSQL, tenantID, now, limit)
		if err != nil {
			return fmt.Errorf("hiringstore: list due applications: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d retention.Due
			if err = rows.Scan(&d.ID, &d.JobID, &d.JobCode, &d.JobTitle, &d.FileIDs); err != nil {
				return fmt.Errorf("hiringstore: scan due application: %w", err)
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// The row is locked and checked again, so an application whose date was moved after it was listed is left alone.
const lockDueApplicationSQL = `
SELECT 1 FROM hiring.applications WHERE id = $1::uuid AND tenant_id = $2::uuid AND retention_until < $3 FOR UPDATE`

const deleteApplicationFilesSQL = `DELETE FROM hiring.application_files WHERE application_id = $1::uuid AND tenant_id = $2::uuid`

// The compliance log is append-only and has no foreign key, so the entry outlives the application. It holds the
// application id and the reason, nothing about the candidate.
const complianceDeletionSQL = `
INSERT INTO hiring.compliance_log (job_id, tenant_id, gate_id, severity, event)
VALUES ($1::uuid, $2::uuid, 'retention', 'informational', $3)`

// The special category answers, review task and unsent emails go with the application (ON DELETE CASCADE).
const deleteApplicationSQL = `DELETE FROM hiring.applications WHERE id = $1::uuid AND tenant_id = $2::uuid`

// DeleteApplication implements retention.Store.
func (s *Store) DeleteApplication(ctx context.Context, tenantID string, d retention.Due, now time.Time) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var one int
		err := tx.QueryRow(ctx, lockDueApplicationSQL, d.ID, tenantID, now).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already deleted, or no longer due
		}
		if err != nil {
			return fmt.Errorf("hiringstore: lock application: %w", err)
		}
		if _, err = tx.Exec(ctx, deleteApplicationFilesSQL, d.ID, tenantID); err != nil {
			return fmt.Errorf("hiringstore: delete application files: %w", err)
		}
		event := "application_deleted: retention period ended (application " + d.ID + ")"
		if _, err = tx.Exec(ctx, complianceDeletionSQL, d.JobID, tenantID, event); err != nil {
			return fmt.Errorf("hiringstore: log deletion: %w", err)
		}
		if _, err = tx.Exec(ctx, deleteApplicationSQL, d.ID, tenantID); err != nil {
			return fmt.Errorf("hiringstore: delete application: %w", err)
		}
		return nil
	})
}

const deleteExpiredBlocksSQL = `DELETE FROM hiring.reapply_blocks WHERE tenant_id = $1::uuid AND retention_until < $2`

// DeleteExpiredBlocks implements retention.Store.
func (s *Store) DeleteExpiredBlocks(ctx context.Context, tenantID string, now time.Time) (int, error) {
	n := 0
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, deleteExpiredBlocksSQL, tenantID, now)
		if err != nil {
			return fmt.Errorf("hiringstore: delete expired reapply blocks: %w", err)
		}
		n = int(tag.RowsAffected())
		return nil
	})
	return n, err
}

const deleteOldEmailsSQL = `DELETE FROM hiring.email_outbox WHERE tenant_id = $1::uuid AND retention_until < $2`

// DeleteOldEmails implements retention.Store.
func (s *Store) DeleteOldEmails(ctx context.Context, tenantID string, now time.Time) (int, error) {
	n := 0
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, deleteOldEmailsSQL, tenantID, now)
		if err != nil {
			return fmt.Errorf("hiringstore: delete old emails: %w", err)
		}
		n = int(tag.RowsAffected())
		return nil
	})
	return n, err
}

// The notice goes to the company's privacy contact, or to its owner when none was given.
const noticeRecipientSQL = `
SELECT COALESCE(NULLIF(o.privacy_contact_email, ''), u.email), o.legal_name
FROM accounts.organizations o JOIN accounts.users u ON u.id = o.owner_user_id
WHERE o.id = $1::uuid`

const noticePurpose = "Telling the company which applications were deleted"

const insertNoticeSQL = `
INSERT INTO hiring.email_outbox (tenant_id, kind, recipient, data, retention_until, lawful_basis, collection_purpose)
VALUES ($1::uuid, 'retention_notice', $2, $3::jsonb, $4, 'legitimate_interests', $5)`

// QueueNotice implements retention.Store.
func (s *Store) QueueNotice(ctx context.Context, tenantID string, now time.Time, items []retention.NoticeItem) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var to, company string
		if err := tx.QueryRow(ctx, noticeRecipientSQL, tenantID).Scan(&to, &company); err != nil {
			return fmt.Errorf("hiringstore: find notice recipient: %w", err)
		}
		data, err := json.Marshal(map[string]any{"company": company, "items": items})
		if err != nil {
			return fmt.Errorf("hiringstore: encode notice: %w", err)
		}
		if _, err = tx.Exec(ctx, insertNoticeSQL, tenantID, to, data, now.Add(OutboxRetention), noticePurpose); err != nil {
			return fmt.Errorf("hiringstore: queue notice: %w", err)
		}
		return nil
	})
}

// ---- email outbox (phase 3d) ----

// SKIP LOCKED lets two runs share the work; the lease moves next_attempt_at forward, so a crash between sending
// and marking only repeats the email after the lease, never at once.
const leaseEmailsSQL = `
UPDATE hiring.email_outbox o
SET attempts = o.attempts + 1, next_attempt_at = $2::timestamptz + make_interval(secs => $4::float8)
WHERE o.id IN (
    SELECT id FROM hiring.email_outbox
    WHERE tenant_id = $1::uuid AND status = 'pending' AND next_attempt_at <= $2
    ORDER BY next_attempt_at, id
    LIMIT $3
    FOR UPDATE SKIP LOCKED)
RETURNING o.id::text, o.kind, o.recipient, o.recipient_name, o.data, o.attempts`

// Lease implements outbox.Store.
func (s *Store) Lease(ctx context.Context, tenantID string, now time.Time, limit int, lease time.Duration) ([]outbox.Email, error) {
	var out []outbox.Email
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, leaseEmailsSQL, tenantID, now, limit, lease.Seconds())
		if err != nil {
			return fmt.Errorf("hiringstore: lease emails: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e outbox.Email
			var data []byte
			if err = rows.Scan(&e.ID, &e.Kind, &e.Recipient, &e.Name, &data, &e.Attempts); err != nil {
				return fmt.Errorf("hiringstore: scan email: %w", err)
			}
			e.Data = json.RawMessage(data)
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

const markEmailSentSQL = `
UPDATE hiring.email_outbox SET status = 'sent', sent_at = $3, last_error = ''
WHERE id = $1::uuid AND tenant_id = $2::uuid`

// MarkSent implements outbox.Store.
func (s *Store) MarkSent(ctx context.Context, tenantID, id string, now time.Time) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, markEmailSentSQL, id, tenantID, now); err != nil {
			return fmt.Errorf("hiringstore: mark email sent: %w", err)
		}
		return nil
	})
}

const markEmailFailedSQL = `
UPDATE hiring.email_outbox
SET last_error = $3, status = CASE WHEN $4::boolean THEN 'failed' ELSE status END, next_attempt_at = $5
WHERE id = $1::uuid AND tenant_id = $2::uuid`

// MarkFailed implements outbox.Store.
func (s *Store) MarkFailed(ctx context.Context, tenantID, id, reason string, final bool, next time.Time) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, markEmailFailedSQL, id, tenantID, reason, final, next); err != nil {
			return fmt.Errorf("hiringstore: mark email failed: %w", err)
		}
		return nil
	})
}
