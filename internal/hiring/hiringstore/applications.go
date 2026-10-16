package hiringstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/applications"
)

// ---- the hiring team's view of applications (phase 3e) ----

var _ applications.Store = (*Store)(nil)

const jobAccessSQL = `
SELECT j.id::text, 'JB-' || j.job_number::text, COALESCE(j.title, ''), j.status, j.created_by::text,
       COALESCE(m.role, ''), j.retention_months::int
FROM hiring.job_postings j
LEFT JOIN hiring.job_members m ON m.job_id = j.id AND m.user_id = $3::uuid
WHERE j.id = $1::uuid AND j.tenant_id = $2::uuid AND j.deleted_at IS NULL`

// JobAccess implements applications.Store.
func (s *Store) JobAccess(ctx context.Context, tenantID, jobID, userID string) (*applications.JobAccess, error) {
	var a applications.JobAccess
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, jobAccessSQL, jobID, tenantID, userID).
			Scan(&a.JobID, &a.Code, &a.Title, &a.Status, &a.CreatedBy, &a.MemberRole, &a.RetentionMonths)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("hiringstore: job access: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// The list holds no answers. The search matches name and email only, with LIKE wildcards in the text escaped.
const listApplicationsSQL = `
SELECT a.id::text, a.candidate_name, a.candidate_email, a.candidate_country::text, a.candidate_market, a.status,
       a.source, jsonb_array_length(a.flags), a.first_opened_at IS NOT NULL,
       EXISTS (SELECT 1 FROM hiring.application_files f WHERE f.application_id = a.id AND f.tenant_id = a.tenant_id),
       a.submitted_at
FROM hiring.applications a
WHERE a.tenant_id = $1::uuid AND a.job_id = $2::uuid
  AND (cardinality($3::text[]) = 0 OR a.status = ANY($3::text[]))
  AND ($4::text = '' OR a.source = $4::text)
  AND (NOT $5::boolean OR a.first_opened_at IS NULL)
  AND ($6::text = '' OR a.candidate_name ILIKE '%' || $6::text || '%' OR a.candidate_email ILIKE '%' || $6::text || '%')
  AND ($7::timestamptz IS NULL OR (a.submitted_at, a.id) < ($7::timestamptz, $8::uuid))
ORDER BY a.submitted_at DESC, a.id DESC
LIMIT $9::int`

const countApplicationsSQL = `
SELECT status, count(*)::int FROM hiring.applications
WHERE tenant_id = $1::uuid AND job_id = $2::uuid GROUP BY status`

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// List implements applications.Store.
func (s *Store) List(ctx context.Context, tenantID, jobID string, q applications.StoreListQuery) ([]applications.Summary, map[string]int, error) {
	var (
		out    []applications.Summary
		counts = map[string]int{}
	)
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		statuses := q.Statuses
		if statuses == nil {
			statuses = []string{}
		}
		var after *time.Time
		afterID := "00000000-0000-0000-0000-000000000000"
		if !q.AfterTime.IsZero() {
			t := q.AfterTime
			after, afterID = &t, q.AfterID
		}
		rows, err := tx.Query(ctx, listApplicationsSQL, tenantID, jobID, statuses, q.Source, q.NewOnly,
			likeEscaper.Replace(q.Search), after, afterID, q.Limit)
		if err != nil {
			return fmt.Errorf("hiringstore: list applications: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var a applications.Summary
			if err = rows.Scan(&a.ID, &a.Name, &a.Email, &a.Country, &a.Market, &a.Status, &a.Source, &a.FlagCount,
				&a.Opened, &a.HasFiles, &a.SubmittedAt); err != nil {
				return fmt.Errorf("hiringstore: scan application: %w", err)
			}
			out = append(out, a)
		}
		if err = rows.Err(); err != nil {
			return fmt.Errorf("hiringstore: list applications: %w", err)
		}
		rows.Close()
		crows, err := tx.Query(ctx, countApplicationsSQL, tenantID, jobID)
		if err != nil {
			return fmt.Errorf("hiringstore: count applications: %w", err)
		}
		defer crows.Close()
		for crows.Next() {
			var st string
			var n int
			if err = crows.Scan(&st, &n); err != nil {
				return fmt.Errorf("hiringstore: scan count: %w", err)
			}
			counts[st] = n
		}
		return crows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	return out, counts, nil
}

const openApplicationSQL = `
SELECT a.id::text, a.candidate_name, a.candidate_email, a.candidate_country::text, a.candidate_market, a.status,
       a.source, jsonb_array_length(a.flags), a.first_opened_at IS NOT NULL, a.submitted_at,
       a.job_id::text, a.form_id::text, a.form_version, a.flags, a.answers, COALESCE(a.status_note, ''),
       a.decided_at, a.consent_given_at, a.notice_version, a.retention_until
FROM hiring.applications a
WHERE a.id = $1::uuid AND a.job_id = $2::uuid AND a.tenant_id = $3::uuid
FOR UPDATE`

// The application's first opening takes it out of the job's "new" count. Only the first one does.
const markOpenedSQL = `
UPDATE hiring.applications SET first_opened_at = now(), first_opened_by = $2::uuid
WHERE id = $1::uuid AND tenant_id = $3::uuid AND first_opened_at IS NULL`

const decrementNewSQL = `
UPDATE hiring.job_applicant_counts SET new_applicant_count = GREATEST(new_applicant_count - 1, 0)
WHERE job_id = $1::uuid AND tenant_id = $2::uuid`

const openedFormSQL = `
SELECT v.schema FROM hiring.form_versions v
JOIN hiring.application_forms f ON f.id = v.form_id
WHERE v.form_id = $1::uuid AND v.version = $2 AND f.tenant_id = $3::uuid`

const applicationFilesSQL = `
SELECT id::text, question_id, original_filename, content_type, size_bytes
FROM hiring.application_files
WHERE application_id = $1::uuid AND tenant_id = $2::uuid AND status = 'claimed'
ORDER BY created_at, id`

const applicationReviewSQL = `
SELECT status, reasons, COALESCE(decision, ''), decided_at, COALESCE(note, ''), COALESCE(opened_by::text, '')
FROM hiring.review_tasks WHERE application_id = $1::uuid AND tenant_id = $2::uuid`

// The last person to open a flagged application is the one who may decide it.
const trackReviewSQL = `
UPDATE hiring.review_tasks SET opened_by = $2::uuid, opened_at = now()
WHERE application_id = $1::uuid AND tenant_id = $3::uuid AND status = 'open'`

// markOpened takes an application out of the job's new count the first time somebody looks at it.
func markOpened(ctx context.Context, tx pgx.Tx, tenantID, jobID, appID, actorID string) error {
	tag, err := tx.Exec(ctx, markOpenedSQL, appID, actorID, tenantID)
	if err != nil {
		return fmt.Errorf("hiringstore: mark opened: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if _, err = tx.Exec(ctx, decrementNewSQL, jobID, tenantID); err != nil {
			return fmt.Errorf("hiringstore: new applicant count: %w", err)
		}
	}
	return nil
}

// Open implements applications.Store.
func (s *Store) Open(ctx context.Context, tenantID string, in applications.OpenInput) (*applications.Detail, error) {
	var d applications.Detail
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var (
			formID  string
			answers []byte
		)
		err := tx.QueryRow(ctx, openApplicationSQL, in.AppID, in.JobID, tenantID).Scan(
			&d.ID, &d.Name, &d.Email, &d.Country, &d.Market, &d.Status, &d.Source, &d.FlagCount, &d.Opened,
			&d.SubmittedAt, &d.JobID, &formID, &d.FormVersion, &d.Flags, &answers, &d.StatusNote, &d.DecidedAt,
			&d.ConsentGivenAt, &d.NoticeVersion, &d.RetentionUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("hiringstore: open application: %w", err)
		}
		if err = markOpened(ctx, tx, tenantID, in.JobID, in.AppID, in.ActorID); err != nil {
			return err
		}
		d.Opened = true

		var raw []byte
		if err = tx.QueryRow(ctx, openedFormSQL, formID, d.FormVersion, tenantID).Scan(&raw); err != nil {
			return fmt.Errorf("hiringstore: load form of application: %w", err)
		}
		var schema frozenSchema
		if err = json.Unmarshal(raw, &schema); err != nil {
			return fmt.Errorf("hiringstore: decode form of application: %w", err)
		}
		stored := map[string]json.RawMessage{}
		if err = json.Unmarshal(answers, &stored); err != nil {
			return fmt.Errorf("hiringstore: decode answers: %w", err)
		}

		d.Files = []applications.File{}
		byQuestion := map[string]string{}
		frows, err := tx.Query(ctx, applicationFilesSQL, in.AppID, tenantID)
		if err != nil {
			return fmt.Errorf("hiringstore: application files: %w", err)
		}
		defer frows.Close()
		for frows.Next() {
			var f applications.File
			if err = frows.Scan(&f.ID, &f.QuestionID, &f.Filename, &f.ContentType, &f.SizeBytes); err != nil {
				return fmt.Errorf("hiringstore: scan file: %w", err)
			}
			d.Files = append(d.Files, f)
			byQuestion[f.QuestionID] = f.ID
		}
		if err = frows.Err(); err != nil {
			return fmt.Errorf("hiringstore: application files: %w", err)
		}
		frows.Close()

		// Answers follow the form's order. Special category questions are skipped even if a value were present:
		// their answers live sealed in another schema and are never read here. An upload answer is a claim token,
		// not content, so it is replaced by the file's id.
		qs := schema.Questions
		sort.SliceStable(qs, func(i, j int) bool { return qs[i].Position < qs[j].Position })
		d.Answers = []applications.Answer{}
		for _, q := range qs {
			if q.SpecialCategory != "" {
				continue
			}
			a := applications.Answer{QuestionID: q.ID, Section: q.Section, Type: q.Type, Label: q.Label}
			if fid, ok := byQuestion[q.ID]; ok {
				a.FileID = fid
			} else if v, ok := stored[q.ID]; ok && !isUploadType(q.Type) {
				a.Value = v
			} else {
				continue
			}
			d.Answers = append(d.Answers, a)
		}

		var (
			rStatus, rDecision, rNote, rOpenedBy string
			rReasons                             []byte
			rDecidedAt                           *time.Time
		)
		err = tx.QueryRow(ctx, applicationReviewSQL, in.AppID, tenantID).
			Scan(&rStatus, &rReasons, &rDecision, &rDecidedAt, &rNote, &rOpenedBy)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("hiringstore: application review: %w", err)
		default:
			if in.TrackReview && rStatus == "open" {
				if _, err = tx.Exec(ctx, trackReviewSQL, in.AppID, in.ActorID, tenantID); err != nil {
					return fmt.Errorf("hiringstore: track review: %w", err)
				}
				rOpenedBy = in.ActorID
			}
			d.Review = &applications.Review{
				Status: rStatus, Reasons: rReasons, Decision: rDecision, DecidedAt: rDecidedAt, Note: rNote,
				OpenedByMe: rStatus == "open" && rOpenedBy == in.ActorID && in.ActorID != "",
			}
		}

		diff, _ := json.Marshal(map[string]any{"application_id": in.AppID})
		if _, err = tx.Exec(ctx, insertAuditSQL, in.JobID, tenantID, in.ActorID, "application.viewed", diff); err != nil {
			return fmt.Errorf("hiringstore: audit view: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func isUploadType(t string) bool {
	return t == "image_upload" || t == "file_upload" || t == "autofill_resume"
}

const fileInfoSQL = `
SELECT id::text, original_filename, content_type, COALESCE(size_bytes, 0), job_id::text
FROM hiring.application_files
WHERE id = $1::uuid AND application_id = $2::uuid AND job_id = $3::uuid AND tenant_id = $4::uuid AND status = 'claimed'`

// FileInfo implements applications.Store. Handing out a link is a read of personal data, so it is audited here,
// in the same transaction as the lookup.
func (s *Store) FileInfo(ctx context.Context, tenantID, jobID, appID, fileID, actorID string) (*applications.FileInfo, error) {
	var f applications.FileInfo
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, fileInfoSQL, fileID, appID, jobID, tenantID).
			Scan(&f.ID, &f.Filename, &f.ContentType, &f.SizeBytes, &f.JobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("hiringstore: file info: %w", err)
		}
		diff, _ := json.Marshal(map[string]any{"application_id": appID, "file_id": fileID})
		if _, err = tx.Exec(ctx, insertAuditSQL, jobID, tenantID, actorID, "application.file_downloaded", diff); err != nil {
			return fmt.Errorf("hiringstore: audit download: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &f, nil
}

const loadReviewStateSQL = `SELECT status = 'open', COALESCE(opened_by::text, '')
FROM hiring.review_tasks WHERE application_id = $1::uuid AND tenant_id = $2::uuid`

const loadChangeSQL = `
SELECT a.status, a.candidate_name, a.candidate_email, 'JB-' || j.job_number::text, COALESCE(j.title, ''),
       o.legal_name, COALESCE(j.public_id, ''), j.retention_months::int,
       COALESCE(s.reapply_block_months, 0)::int, COALESCE(s.send_status_emails, TRUE)
FROM hiring.applications a
JOIN hiring.job_postings j ON j.id = a.job_id AND j.tenant_id = a.tenant_id
JOIN accounts.organizations o ON o.id = a.tenant_id
LEFT JOIN hiring.org_settings s ON s.tenant_id = a.tenant_id
WHERE a.id = $1::uuid AND a.job_id = $2::uuid AND a.tenant_id = $3::uuid`

// LoadChange implements applications.Store.
func (s *Store) LoadChange(ctx context.Context, tenantID, jobID, appID string) (*applications.ChangeState, error) {
	var st applications.ChangeState
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, loadChangeSQL, appID, jobID, tenantID).Scan(&st.Status, &st.Name, &st.Email,
			&st.JobCode, &st.JobTitle, &st.Company, &st.PublicID, &st.RetentionMonths, &st.ReapplyBlockMonths,
			&st.SendStatusEmails)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("hiringstore: load change: %w", err)
		}
		var open bool
		var by string
		err = tx.QueryRow(ctx, loadReviewStateSQL, appID, tenantID).Scan(&open, &by)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("hiringstore: load review: %w", err)
		default:
			st.Review = &applications.ReviewState{Open: open, OpenedBy: by}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// The change is guarded by the status the decision was made from. A final status starts the retention clock:
// the date can only move earlier, never later, and everything that belongs to the application follows it.
const applyChangeSQL = `
UPDATE hiring.applications SET
    status = $4,
    status_note = NULLIF($5, ''),
    decided_at = CASE WHEN $6::boolean THEN $7::timestamptz END,
    decided_by = CASE WHEN $6::boolean THEN $8::uuid END,
    retention_until = CASE WHEN $6::boolean THEN LEAST(retention_until, $7::timestamptz + make_interval(months => $9::int))
                           ELSE retention_until END
WHERE id = $1::uuid AND job_id = $2::uuid AND tenant_id = $3::uuid AND status = $10 AND status NOT IN ('rejected', 'hired', 'withdrawn')
RETURNING lawful_basis`

const followOneRetentionSQL = `
WITH f AS (UPDATE hiring.application_files c SET retention_until = a.retention_until FROM hiring.applications a
           WHERE c.application_id = a.id AND a.id = $1::uuid AND c.tenant_id = $2::uuid AND c.retention_until > a.retention_until RETURNING 1),
     s AS (UPDATE hiring_restricted.special_answers c SET retention_until = a.retention_until FROM hiring.applications a
           WHERE c.application_id = a.id AND a.id = $1::uuid AND c.tenant_id = $2::uuid AND c.retention_until > a.retention_until RETURNING 1),
     r AS (UPDATE hiring.review_tasks c SET retention_until = a.retention_until FROM hiring.applications a
           WHERE c.application_id = a.id AND a.id = $1::uuid AND c.tenant_id = $2::uuid AND c.retention_until > a.retention_until RETURNING 1)
SELECT (SELECT count(*) FROM f) + (SELECT count(*) FROM s) + (SELECT count(*) FROM r)`

// Only the person who opened the application since it was flagged may close its review.
const closeReviewSQL = `
UPDATE hiring.review_tasks SET status = 'done', decision = $3, decided_by = $4::uuid, decided_at = $5::timestamptz,
    note = NULLIF($6, '')
WHERE application_id = $1::uuid AND tenant_id = $2::uuid AND status = 'open' AND opened_by = $4::uuid`

const upsertBlockSQL = `
INSERT INTO hiring.reapply_blocks
    (tenant_id, email_hmac, blocked_until, retention_until, lawful_basis, collection_purpose)
VALUES ($1::uuid, $2, $3::timestamptz, $3::timestamptz, 'legitimate_interests', 'Not accepting a new application after a rejection')
ON CONFLICT (tenant_id, email_hmac) DO UPDATE SET
    blocked_until = GREATEST(hiring.reapply_blocks.blocked_until, EXCLUDED.blocked_until),
    retention_until = GREATEST(hiring.reapply_blocks.retention_until, EXCLUDED.retention_until)`

// ApplyChange implements applications.Store.
func (s *Store) ApplyChange(ctx context.Context, tenantID string, ch *applications.Change) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		final := applications.Final(ch.To)
		var basis string
		err := tx.QueryRow(ctx, applyChangeSQL, ch.AppID, ch.JobID, tenantID, ch.To, ch.Note, final, ch.Now,
			ch.ActorID, ch.RetentionMonths, ch.From).Scan(&basis)
		if errors.Is(err, pgx.ErrNoRows) {
			return applications.ErrStale
		}
		if err != nil {
			return fmt.Errorf("hiringstore: change status: %w", mapPgError(err))
		}
		if err = markOpened(ctx, tx, tenantID, ch.JobID, ch.AppID, ch.ActorID); err != nil {
			return err
		}
		if final {
			var moved int
			if err = tx.QueryRow(ctx, followOneRetentionSQL, ch.AppID, tenantID).Scan(&moved); err != nil {
				return fmt.Errorf("hiringstore: follow retention: %w", err)
			}
		}
		if ch.Decision != "" {
			tag, err := tx.Exec(ctx, closeReviewSQL, ch.AppID, tenantID, ch.Decision, ch.ActorID, ch.Now, ch.Note)
			if err != nil {
				return fmt.Errorf("hiringstore: close review: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return applications.ErrNotOpened
			}
		}
		if ch.Block != nil {
			if _, err = tx.Exec(ctx, upsertBlockSQL, tenantID, ch.Block.EmailHMAC, ch.Block.Until); err != nil {
				return fmt.Errorf("hiringstore: reapply block: %w", err)
			}
		}
		if ch.Email != nil {
			if err = insertOutbox(ctx, tx, tenantID, ch.AppID, ch.Email, ch.Now, basis); err != nil {
				return err
			}
		}
		diff, err := json.Marshal(map[string]any{
			"application_id": ch.AppID, "from": ch.From, "to": ch.To, "decision": ch.Decision, "has_note": ch.Note != "",
		})
		if err != nil {
			return fmt.Errorf("hiringstore: encode audit: %w", err)
		}
		if _, err = tx.Exec(ctx, insertAuditSQL, ch.JobID, tenantID, ch.ActorID, "application.status_changed", diff); err != nil {
			return fmt.Errorf("hiringstore: audit status change: %w", err)
		}
		return nil
	})
}

const getApplicantSettingsSQL = `
SELECT reapply_block_months::int, send_status_emails FROM hiring.org_settings WHERE tenant_id = $1::uuid`

const saveApplicantSettingsSQL = `
INSERT INTO hiring.org_settings (tenant_id, reapply_block_months, send_status_emails)
VALUES ($1::uuid, $2::smallint, $3::boolean)
ON CONFLICT (tenant_id) DO UPDATE SET reapply_block_months = EXCLUDED.reapply_block_months,
    send_status_emails = EXCLUDED.send_status_emails`

// GetApplicantSettings implements applications.Store. A company with no row has the defaults.
func (s *Store) GetApplicantSettings(ctx context.Context, tenantID string) (int, bool, error) {
	months, emails := 0, true
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, getApplicantSettingsSQL, tenantID).Scan(&months, &emails)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("hiringstore: get applicant settings: %w", err)
		}
		return nil
	})
	return months, emails, err
}

// SaveApplicantSettings implements applications.Store.
func (s *Store) SaveApplicantSettings(ctx context.Context, tenantID string, months int, emails bool) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, saveApplicantSettingsSQL, tenantID, months, emails); err != nil {
			return fmt.Errorf("hiringstore: save applicant settings: %w", mapPgError(err))
		}
		return nil
	})
}
