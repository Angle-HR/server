package appstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/applications"
)

var _ applications.InterviewStore = (*Store)(nil)

const interviewCols = `i.id::text, i.job_id::text, i.application_id::text, i.starts_at, i.duration_min, i.timezone, i.kind, i.location,
       i.notes, COALESCE((SELECT array_agg(x::text) FROM unnest(i.interviewer_ids) x), '{}')::text[], i.status, COALESCE(i.created_by::text, ''),
       i.created_at, i.updated_at, i.sequence`

func scanInterview(row pgx.Row, extra ...any) (*applications.Interview, error) {
	var iv applications.Interview
	dst := append([]any{&iv.ID, &iv.JobID, &iv.ApplicationID, &iv.StartsAt, &iv.DurationMin, &iv.Timezone, &iv.Kind, &iv.Location,
		&iv.Notes, &iv.InterviewerIDs, &iv.Status, &iv.CreatedBy, &iv.CreatedAt, &iv.UpdatedAt, &iv.Sequence}, extra...)
	if err := row.Scan(dst...); err != nil {
		return nil, err
	}
	iv.EndsAt = iv.StartsAt.Add(time.Duration(iv.DurationMin) * time.Minute)
	if iv.InterviewerIDs == nil {
		iv.InterviewerIDs = []string{}
	}
	return &iv, nil
}

// Interviews implements applications.InterviewStore.
func (s *Store) Interviews(ctx context.Context, tenantID, jobID, appID string) ([]applications.Interview, error) {
	out := []applications.Interview{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+interviewCols+` FROM hiring.app_interviews i
WHERE i.tenant_id = $1::uuid AND i.application_id = $2::uuid ORDER BY i.starts_at, i.id LIMIT 100`, tenantID, appID)
		if err != nil {
			return fmt.Errorf("appstore: list interviews: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			iv, err := scanInterview(rows)
			if err != nil {
				return fmt.Errorf("appstore: scan interview: %w", err)
			}
			out = append(out, *iv)
		}
		return rows.Err()
	})
	return out, err
}

// Interview implements applications.InterviewStore.
func (s *Store) Interview(ctx context.Context, tenantID, jobID, appID, id string) (*applications.Interview, error) {
	var iv *applications.Interview
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		iv, err = scanInterview(tx.QueryRow(ctx, `SELECT `+interviewCols+` FROM hiring.app_interviews i
WHERE i.id = $1::uuid AND i.tenant_id = $2::uuid AND i.job_id = $3::uuid AND i.application_id = $4::uuid`, id, tenantID, jobID, appID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return iv, err
}

// CreateInterview implements applications.InterviewStore.
func (s *Store) CreateInterview(ctx context.Context, tenantID, actorID string, iv *applications.Interview, msg *applications.NewMessage) error {
	if iv.InterviewerIDs == nil {
		iv.InterviewerIDs = []string{}
	}
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, iv.JobID, iv.ApplicationID); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM hiring.app_interviews WHERE tenant_id = $1::uuid AND application_id = $2::uuid AND status = 'scheduled'`,
			tenantID, iv.ApplicationID).Scan(&active); err != nil {
			return fmt.Errorf("appstore: count interviews: %w", err)
		}
		if active >= applications.MaxActiveInterviews {
			return applications.ErrTooManyInterviews
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO hiring.app_interviews (id, tenant_id, job_id, application_id, created_by, starts_at, duration_min, timezone, kind, location, notes, interviewer_ids, status, sequence)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, NULLIF($5, '')::uuid, $6, $7, $8, $9, $10, $11, $12::uuid[], $13, $14)`,
			iv.ID, tenantID, iv.JobID, iv.ApplicationID, actorID, iv.StartsAt, iv.DurationMin, iv.Timezone, iv.Kind, iv.Location, iv.Notes,
			iv.InterviewerIDs, iv.Status, iv.Sequence); err != nil {
			return fmt.Errorf("appstore: insert interview: %w", err)
		}
		iv.CreatedAt, iv.UpdatedAt = time.Now().UTC(), time.Now().UTC()
		if err := insertEvent(ctx, tx, tenantID, iv.JobID, iv.ApplicationID, actorID, applications.EventInterviewScheduled,
			map[string]any{"interview_id": iv.ID, "starts_at": iv.StartsAt, "kind": iv.Kind, "invited": msg != nil}); err != nil {
			return err
		}
		return queueWith(ctx, tx, tenantID, actorID, msg)
	})
}

// queueWith writes a message that goes with another change, without the daily limits.
func queueWith(ctx context.Context, tx pgx.Tx, tenantID, actorID string, msg *applications.NewMessage) error {
	if msg == nil {
		return nil
	}
	out, err := insertMessage(ctx, tx, tenantID, msg)
	if err != nil {
		return err
	}
	return insertEvent(ctx, tx, tenantID, msg.JobID, msg.AppID, actorID, applications.EventMessage, map[string]any{"message_id": out.ID, "calendar": true})
}

// SaveInterview implements applications.InterviewStore.
func (s *Store) SaveInterview(ctx context.Context, tenantID, actorID string, iv *applications.Interview, prevSeq int, event string, msg *applications.NewMessage) error {
	if iv.InterviewerIDs == nil {
		iv.InterviewerIDs = []string{}
	}
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
UPDATE hiring.app_interviews SET starts_at = $5, duration_min = $6, timezone = $7, kind = $8, location = $9, notes = $10,
       interviewer_ids = $11::uuid[], status = $12, sequence = $13, updated_at = now()
WHERE id = $1::uuid AND tenant_id = $2::uuid AND job_id = $3::uuid AND sequence = $4`,
			iv.ID, tenantID, iv.JobID, prevSeq, iv.StartsAt, iv.DurationMin, iv.Timezone, iv.Kind, iv.Location, iv.Notes,
			iv.InterviewerIDs, iv.Status, iv.Sequence)
		if err != nil {
			return fmt.Errorf("appstore: save interview: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return applications.ErrStale
		}
		iv.UpdatedAt = time.Now().UTC()
		if err = insertEvent(ctx, tx, tenantID, iv.JobID, iv.ApplicationID, actorID, event,
			map[string]any{"interview_id": iv.ID, "starts_at": iv.StartsAt, "notified": msg != nil}); err != nil {
			return err
		}
		return queueWith(ctx, tx, tenantID, actorID, msg)
	})
}

// MyInterviews implements applications.InterviewStore.
func (s *Store) MyInterviews(ctx context.Context, tenantID, userID string, from, to time.Time, limit int) ([]applications.Interview, error) {
	out := []applications.Interview{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+interviewCols+`, a.candidate_name FROM hiring.app_interviews i
JOIN hiring.applications a ON a.id = i.application_id
WHERE i.tenant_id = $1::uuid AND i.status = 'scheduled' AND $2::uuid = ANY (i.interviewer_ids)
  AND i.starts_at >= $3 AND i.starts_at < $4 AND a.status NOT IN ('rejected', 'hired', 'withdrawn')
ORDER BY i.starts_at, i.id LIMIT $5`, tenantID, userID, from, to, limit)
		if err != nil {
			return fmt.Errorf("appstore: my interviews: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			iv, err := scanInterview(rows, &name)
			if err != nil {
				return fmt.Errorf("appstore: scan interview: %w", err)
			}
			iv.CandidateName = name
			out = append(out, *iv)
		}
		return rows.Err()
	})
	return out, err
}
