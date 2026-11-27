package appstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/appevents"
	"github.com/Angle-HR/server/internal/hiring/applications"
)

// ---- the hiring team's view of applications (phase 3e) ----

var _ applications.Store = (*Store)(nil)

// The list holds no answers. The search matches name and email only, with LIKE wildcards in the text escaped.
const listApplicationsSQL = `
SELECT a.id::text, a.candidate_name, a.candidate_email, a.candidate_country::text, a.candidate_market, a.status,
       a.source, jsonb_array_length(a.flags), a.first_opened_at IS NOT NULL,
       EXISTS (SELECT 1 FROM hiring.application_files f WHERE f.application_id = a.id AND f.tenant_id = a.tenant_id),
       a.submitted_at, COALESCE(a.stage_id::text, ''),
       EXISTS (SELECT 1 FROM hiring.app_follows fo WHERE fo.application_id = a.id AND fo.user_id = NULLIF($10::text, '')::uuid),
       (SELECT count(*)::int FROM hiring.app_notes n WHERE n.application_id = a.id),
       COALESCE((SELECT array_agg(t.tag_id::text ORDER BY t.created_at) FROM hiring.app_application_tags t WHERE t.application_id = a.id), '{}')::text[]
FROM hiring.applications a
WHERE a.tenant_id = $1::uuid AND a.job_id = $2::uuid
  AND (cardinality($3::text[]) = 0 OR a.status = ANY($3::text[]))
  AND ($4::text = '' OR a.source = $4::text)
  AND (NOT $5::boolean OR a.first_opened_at IS NULL)
  AND ($6::text = '' OR a.candidate_name ILIKE '%' || $6::text || '%' OR a.candidate_email ILIKE '%' || $6::text || '%')
  AND ($7::timestamptz IS NULL OR (a.submitted_at, a.id) < ($7::timestamptz, $8::uuid))
  AND ($11::text = '' OR a.stage_id = NULLIF($11::text, '')::uuid)
  AND (NOT $12::boolean OR EXISTS (SELECT 1 FROM hiring.app_follows fo WHERE fo.application_id = a.id AND fo.user_id = NULLIF($10::text, '')::uuid))
  AND ($13::text = '' OR EXISTS (SELECT 1 FROM hiring.app_application_tags t WHERE t.application_id = a.id AND t.tag_id = NULLIF($13::text, '')::uuid))
ORDER BY a.submitted_at DESC, a.id DESC
LIMIT $9::int`

const countApplicationsSQL = `
SELECT status, count(*)::int FROM hiring.applications
WHERE tenant_id = $1::uuid AND job_id = $2::uuid GROUP BY status`

const countStagesSQL = `
SELECT stage_id::text, count(*)::int FROM hiring.applications
WHERE tenant_id = $1::uuid AND job_id = $2::uuid AND stage_id IS NOT NULL GROUP BY stage_id`

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// List implements applications.Store.
func (s *Store) List(ctx context.Context, tenantID, jobID string, q applications.StoreListQuery) ([]applications.Summary, applications.Counts, error) {
	var (
		out    []applications.Summary
		counts = applications.Counts{Status: map[string]int{}, Stage: map[string]int{}}
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
			likeEscaper.Replace(q.Search), after, afterID, q.Limit, q.ActorID, q.StageID, q.FollowedOnly, q.TagID)
		if err != nil {
			return fmt.Errorf("appstore: list applications: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var a applications.Summary
			if err = rows.Scan(&a.ID, &a.Name, &a.Email, &a.Country, &a.Market, &a.Status, &a.Source, &a.FlagCount,
				&a.Opened, &a.HasFiles, &a.SubmittedAt, &a.StageID, &a.Followed, &a.NoteCount, &a.TagIDs); err != nil {
				return fmt.Errorf("appstore: scan application: %w", err)
			}
			out = append(out, a)
		}
		if err = rows.Err(); err != nil {
			return fmt.Errorf("appstore: list applications: %w", err)
		}
		rows.Close()
		crows, err := tx.Query(ctx, countApplicationsSQL, tenantID, jobID)
		if err != nil {
			return fmt.Errorf("appstore: count applications: %w", err)
		}
		defer crows.Close()
		for crows.Next() {
			var st string
			var n int
			if err = crows.Scan(&st, &n); err != nil {
				return fmt.Errorf("appstore: scan count: %w", err)
			}
			counts.Status[st] = n
		}
		if err = crows.Err(); err != nil {
			return err
		}
		crows.Close()
		srows, err := tx.Query(ctx, countStagesSQL, tenantID, jobID)
		if err != nil {
			return fmt.Errorf("appstore: count stages: %w", err)
		}
		defer srows.Close()
		for srows.Next() {
			var id string
			var n int
			if err = srows.Scan(&id, &n); err != nil {
				return fmt.Errorf("appstore: scan stage count: %w", err)
			}
			counts.Stage[id] = n
		}
		return srows.Err()
	})
	if err != nil {
		return nil, applications.Counts{}, err
	}
	return out, counts, nil
}

const openApplicationSQL = `
SELECT a.id::text, a.candidate_name, a.candidate_email, a.candidate_country::text, a.candidate_market, a.status,
       a.source, jsonb_array_length(a.flags), a.first_opened_at IS NOT NULL, a.submitted_at,
       a.job_id::text, a.form_id::text, a.form_version, a.flags, a.answers, COALESCE(a.status_note, ''),
       a.decided_at, a.consent_given_at, a.notice_version, a.retention_until,
       COALESCE(a.stage_id::text, ''), COALESCE(a.disqualify_reason, ''),
       EXISTS (SELECT 1 FROM hiring.app_follows fo WHERE fo.application_id = a.id AND fo.user_id = NULLIF($4::text, '')::uuid),
       (SELECT count(*)::int FROM hiring.app_notes n WHERE n.application_id = a.id)
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
		return fmt.Errorf("appstore: mark opened: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if _, err = tx.Exec(ctx, decrementNewSQL, jobID, tenantID); err != nil {
			return fmt.Errorf("appstore: new applicant count: %w", err)
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
		err := tx.QueryRow(ctx, openApplicationSQL, in.AppID, in.JobID, tenantID, in.ActorID).Scan(
			&d.ID, &d.Name, &d.Email, &d.Country, &d.Market, &d.Status, &d.Source, &d.FlagCount, &d.Opened,
			&d.SubmittedAt, &d.JobID, &formID, &d.FormVersion, &d.Flags, &answers, &d.StatusNote, &d.DecidedAt,
			&d.ConsentGivenAt, &d.NoticeVersion, &d.RetentionUntil, &d.StageID, &d.DisqualifyReason, &d.Followed, &d.NoteCount)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: open application: %w", err)
		}
		if err = markOpened(ctx, tx, tenantID, in.JobID, in.AppID, in.ActorID); err != nil {
			return err
		}
		d.Opened = true
		if d.Tags, err = loadApplicationTags(ctx, tx, tenantID, in.AppID); err != nil {
			return err
		}
		d.TagIDs = make([]string, len(d.Tags))
		for i, t := range d.Tags {
			d.TagIDs[i] = t.ID
		}

		questions, err := s.Jobs.FrozenQuestions(ctx, tenantID, formID, d.FormVersion)
		if err != nil {
			return fmt.Errorf("appstore: load form of application: %w", err)
		}
		stored := map[string]json.RawMessage{}
		if err = json.Unmarshal(answers, &stored); err != nil {
			return fmt.Errorf("appstore: decode answers: %w", err)
		}

		d.Files = []applications.File{}
		byQuestion := map[string]string{}
		frows, err := tx.Query(ctx, applicationFilesSQL, in.AppID, tenantID)
		if err != nil {
			return fmt.Errorf("appstore: application files: %w", err)
		}
		defer frows.Close()
		for frows.Next() {
			var f applications.File
			if err = frows.Scan(&f.ID, &f.QuestionID, &f.Filename, &f.ContentType, &f.SizeBytes); err != nil {
				return fmt.Errorf("appstore: scan file: %w", err)
			}
			d.Files = append(d.Files, f)
			byQuestion[f.QuestionID] = f.ID
		}
		if err = frows.Err(); err != nil {
			return fmt.Errorf("appstore: application files: %w", err)
		}
		frows.Close()
		d.HasFiles = len(d.Files) > 0

		// Answers follow the form's order. Special category questions are skipped even if a value were present:
		// their answers live sealed in another schema and are never read here. An upload answer is a claim token,
		// not content, so it is replaced by the file's id.
		qs := questions
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
			return fmt.Errorf("appstore: application review: %w", err)
		default:
			if in.TrackReview && rStatus == "open" {
				if _, err = tx.Exec(ctx, trackReviewSQL, in.AppID, in.ActorID, tenantID); err != nil {
					return fmt.Errorf("appstore: track review: %w", err)
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
			return fmt.Errorf("appstore: audit view: %w", err)
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
			return fmt.Errorf("appstore: file info: %w", err)
		}
		diff, _ := json.Marshal(map[string]any{"application_id": appID, "file_id": fileID})
		if _, err = tx.Exec(ctx, insertAuditSQL, jobID, tenantID, actorID, "application.file_downloaded", diff); err != nil {
			return fmt.Errorf("appstore: audit download: %w", err)
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
SELECT a.status, a.candidate_name, a.candidate_email,
       COALESCE(s.reapply_block_months, 0)::int, COALESCE(s.send_status_emails, TRUE)
FROM hiring.applications a
LEFT JOIN hiring.org_settings s ON s.tenant_id = a.tenant_id
WHERE a.id = $1::uuid AND a.job_id = $2::uuid AND a.tenant_id = $3::uuid`

// LoadChange implements applications.Store.
func (s *Store) LoadChange(ctx context.Context, tenantID, jobID, appID string) (*applications.ChangeState, error) {
	var st applications.ChangeState
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, loadChangeSQL, appID, jobID, tenantID).Scan(&st.Status, &st.Name, &st.Email,
			&st.ReapplyBlockMonths, &st.SendStatusEmails)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: load change: %w", err)
		}
		var open bool
		var by string
		err = tx.QueryRow(ctx, loadReviewStateSQL, appID, tenantID).Scan(&open, &by)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("appstore: load review: %w", err)
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
                           ELSE retention_until END,
    stage_id = COALESCE(NULLIF($11::text, '')::uuid, stage_id),
    disqualify_reason = NULLIF($12::text, '')
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
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error { return s.applyChangeTx(ctx, tx, tenantID, ch) })
}

// applyChangeTx is ApplyChange inside a transaction that already has the tenant set, for changes that go with other writes.
func (s *Store) applyChangeTx(ctx context.Context, tx pgx.Tx, tenantID string, ch *applications.Change) error {
	final := applications.Final(ch.To)
	// A decision puts the application in Hired or Disqualified, unless the caller already chose that stage.
	stageID, priorStage, toKind := ch.StageID, "", ""
	if err := tx.QueryRow(ctx, `SELECT COALESCE(stage_id::text, '') FROM hiring.applications WHERE id = $1::uuid AND tenant_id = $2::uuid`,
		ch.AppID, tenantID).Scan(&priorStage); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("appstore: current stage: %w", err)
	}
	if stageID == "" && (ch.To == applications.StatusHired || ch.To == applications.StatusRejected) {
		toKind = applications.KindHired
		if ch.To == applications.StatusRejected {
			toKind = applications.KindDisqualified
		}
		if err := ensureStages(ctx, tx, tenantID, ch.JobID); err != nil {
			return err
		}
		id, err := stageIDByKind(ctx, tx, tenantID, ch.JobID, toKind)
		if err != nil {
			return err
		}
		stageID = id
	}
	var basis string
	err := tx.QueryRow(ctx, applyChangeSQL, ch.AppID, ch.JobID, tenantID, ch.To, ch.Note, final, ch.Now,
		ch.ActorID, ch.RetentionMonths, ch.From, stageID, ch.Reason).Scan(&basis)
	if errors.Is(err, pgx.ErrNoRows) {
		return applications.ErrStale
	}
	if err != nil {
		return fmt.Errorf("appstore: change status: %w", mapPgError(err))
	}
	if err = markOpened(ctx, tx, tenantID, ch.JobID, ch.AppID, ch.ActorID); err != nil {
		return err
	}
	if final {
		var moved int
		if err = tx.QueryRow(ctx, followOneRetentionSQL, ch.AppID, tenantID).Scan(&moved); err != nil {
			return fmt.Errorf("appstore: follow retention: %w", err)
		}
		// A decision ends any offer that is still out: its link stops working.
		if err = withdrawLiveOffers(ctx, tx, tenantID, ch.AppID, ch.Now); err != nil {
			return err
		}
	}
	if ch.Decision != "" {
		tag, err := tx.Exec(ctx, closeReviewSQL, ch.AppID, tenantID, ch.Decision, ch.ActorID, ch.Now, ch.Note)
		if err != nil {
			return fmt.Errorf("appstore: close review: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return applications.ErrNotOpened
		}
	}
	if ch.Block != nil {
		if _, err = tx.Exec(ctx, upsertBlockSQL, tenantID, ch.Block.EmailHMAC, ch.Block.Until); err != nil {
			return fmt.Errorf("appstore: reapply block: %w", err)
		}
	}
	if ch.Message != nil {
		out, merr := insertMessage(ctx, tx, tenantID, ch.Message)
		if merr != nil {
			return merr
		}
		if err = insertEvent(ctx, tx, tenantID, ch.JobID, ch.AppID, ch.ActorID, applications.EventMessage,
			map[string]any{"message_id": out.ID, "with_decision": true}); err != nil {
			return err
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
		return fmt.Errorf("appstore: encode audit: %w", err)
	}
	if _, err = tx.Exec(ctx, insertAuditSQL, ch.JobID, tenantID, ch.ActorID, "application.status_changed", diff); err != nil {
		return fmt.Errorf("appstore: audit status change: %w", err)
	}
	if err = insertEvent(ctx, tx, tenantID, ch.JobID, ch.AppID, ch.ActorID, applications.EventStatusChanged, map[string]any{
		"from": ch.From, "to": ch.To, "decision": ch.Decision, "reason": ch.Reason, "has_note": ch.Note != "",
	}); err != nil {
		return err
	}
	if err = appevents.EnqueueStatusChanged(ctx, tx, appevents.StatusChanged{
		ApplicationID: ch.AppID, JobID: ch.JobID, CompanyID: tenantID, Region: s.Region, FromStatus: ch.From,
		ToStatus: ch.To, ChangedByUserID: ch.ActorID, ChangedAt: ch.Now,
	}); err != nil {
		return fmt.Errorf("appstore: emit status changed event: %w", err)
	}
	if stageID != "" && stageID != priorStage {
		var name, kind string
		if err = tx.QueryRow(ctx, `SELECT name, kind FROM hiring.app_job_stages WHERE id = $1::uuid AND tenant_id = $2::uuid`,
			stageID, tenantID).Scan(&name, &kind); err != nil {
			return fmt.Errorf("appstore: to stage: %w", err)
		}
		if err = insertEvent(ctx, tx, tenantID, ch.JobID, ch.AppID, ch.ActorID, applications.EventStageChanged, map[string]any{
			"from_stage_id": priorStage, "to_stage_id": stageID, "to_stage": name, "to_kind": kind,
		}); err != nil {
			return err
		}
		if err = appevents.EnqueueStageChanged(ctx, tx, appevents.StageChanged{
			ApplicationID: ch.AppID, JobID: ch.JobID, CompanyID: tenantID, Region: s.Region, FromStageID: priorStage,
			ToStageID: stageID, ToStageKind: kind, ChangedByUserID: ch.ActorID, ChangedAt: ch.Now,
		}); err != nil {
			return fmt.Errorf("appstore: emit stage changed event: %w", err)
		}
	}
	return nil
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
			return fmt.Errorf("appstore: get applicant settings: %w", err)
		}
		return nil
	})
	return months, emails, err
}

// SaveApplicantSettings implements applications.Store.
func (s *Store) SaveApplicantSettings(ctx context.Context, tenantID string, months int, emails bool) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, saveApplicantSettingsSQL, tenantID, months, emails); err != nil {
			return fmt.Errorf("appstore: save applicant settings: %w", mapPgError(err))
		}
		return nil
	})
}
