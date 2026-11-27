package appstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/appevents"
	"github.com/Angle-HR/server/internal/hiring/applications"
)

// ---- pipeline: stages, notes, tags, follows, activity (migration 000026) ----

var _ applications.PipelineStore = (*Store)(nil)

// The default pipeline of every job. Custom stages are inserted before Hired.
const seedStagesSQL = `
INSERT INTO hiring.app_job_stages (tenant_id, job_id, key, name, kind, position)
SELECT $1::uuid, $2::uuid, d.key, d.name, d.key, d.pos
FROM (VALUES ('applied', 'Applied', 0), ('shortlisted', 'Shortlisted', 1), ('interview', 'Interview', 2),
             ('offer', 'Offer', 3), ('hired', 'Hired', 4), ('disqualified', 'Disqualified', 5)) AS d(key, name, pos)
WHERE NOT EXISTS (SELECT 1 FROM hiring.app_job_stages WHERE tenant_id = $1::uuid AND job_id = $2::uuid)
ON CONFLICT (tenant_id, job_id, key) DO NOTHING`

// ensureStages gives the job its default stages the first time anything needs them.
func ensureStages(ctx context.Context, tx pgx.Tx, tenantID, jobID string) error {
	if _, err := tx.Exec(ctx, seedStagesSQL, tenantID, jobID); err != nil {
		return fmt.Errorf("appstore: seed stages: %w", err)
	}
	return nil
}

const stageByKindSQL = `SELECT id::text FROM hiring.app_job_stages WHERE tenant_id = $1::uuid AND job_id = $2::uuid AND kind = $3`

func stageIDByKind(ctx context.Context, tx pgx.Tx, tenantID, jobID, kind string) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, stageByKindSQL, tenantID, jobID, kind).Scan(&id); err != nil {
		return "", fmt.Errorf("appstore: stage of kind %s: %w", kind, err)
	}
	return id, nil
}

const insertEventSQL = `
INSERT INTO hiring.app_events (tenant_id, job_id, application_id, actor_id, type, data)
VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4, '')::uuid, $5, $6::jsonb)`

// insertEvent writes a timeline entry. data holds ids and short labels, never note text or candidate details.
func insertEvent(ctx context.Context, tx pgx.Tx, tenantID, jobID, appID, actorID, typ string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("appstore: encode event: %w", err)
	}
	if _, err = tx.Exec(ctx, insertEventSQL, tenantID, jobID, appID, actorID, typ, raw); err != nil {
		return fmt.Errorf("appstore: write event: %w", err)
	}
	return nil
}

const appOnJobSQL = `SELECT 1 FROM hiring.applications WHERE id = $1::uuid AND job_id = $2::uuid AND tenant_id = $3::uuid`

// requireApp returns ErrNotFound unless the application is on the job.
func requireApp(ctx context.Context, tx pgx.Tx, tenantID, jobID, appID string) error {
	var one int
	err := tx.QueryRow(ctx, appOnJobSQL, appID, jobID, tenantID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("appstore: find application: %w", err)
	}
	return nil
}

func auditStage(ctx context.Context, tx pgx.Tx, tenantID, jobID, actorID, action string, diff map[string]any) error {
	raw, err := json.Marshal(diff)
	if err != nil {
		return fmt.Errorf("appstore: encode audit: %w", err)
	}
	if _, err = tx.Exec(ctx, insertAuditSQL, jobID, tenantID, actorID, action, raw); err != nil {
		return fmt.Errorf("appstore: audit %s: %w", action, err)
	}
	return nil
}

// ---- stages ----

const listStagesSQL = `
SELECT s.id::text, s.key, s.name, s.kind, s.position,
       (SELECT count(*)::int FROM hiring.applications a WHERE a.stage_id = s.id)
FROM hiring.app_job_stages s
WHERE s.tenant_id = $1::uuid AND s.job_id = $2::uuid
ORDER BY s.position, s.id`

func loadStages(ctx context.Context, tx pgx.Tx, tenantID, jobID string) ([]applications.Stage, error) {
	rows, err := tx.Query(ctx, listStagesSQL, tenantID, jobID)
	if err != nil {
		return nil, fmt.Errorf("appstore: list stages: %w", err)
	}
	defer rows.Close()
	out := []applications.Stage{}
	for rows.Next() {
		var s applications.Stage
		if err = rows.Scan(&s.ID, &s.Key, &s.Name, &s.Kind, &s.Position, &s.Count); err != nil {
			return nil, fmt.Errorf("appstore: scan stage: %w", err)
		}
		s.Final = applications.FinalKind(s.Kind)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Stages implements applications.PipelineStore.
func (s *Store) Stages(ctx context.Context, tenantID, jobID string) ([]applications.Stage, error) {
	var out []applications.Stage
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := ensureStages(ctx, tx, tenantID, jobID); err != nil {
			return err
		}
		var err error
		out, err = loadStages(ctx, tx, tenantID, jobID)
		return err
	})
	return out, err
}

const nameTakenSQL = `
SELECT EXISTS (SELECT 1 FROM hiring.app_job_stages
               WHERE tenant_id = $1::uuid AND job_id = $2::uuid AND lower(name) = lower($3) AND id <> $4::uuid)`

func stageNameTaken(ctx context.Context, tx pgx.Tx, tenantID, jobID, name, exceptID string) (bool, error) {
	var taken bool
	err := tx.QueryRow(ctx, nameTakenSQL, tenantID, jobID, name, exceptID).Scan(&taken)
	return taken, err
}

const (
	hiredPositionSQL = `SELECT position FROM hiring.app_job_stages WHERE tenant_id = $1::uuid AND job_id = $2::uuid AND kind = 'hired'`
	shiftStagesSQL   = `UPDATE hiring.app_job_stages SET position = position + 1 WHERE tenant_id = $1::uuid AND job_id = $2::uuid AND position >= $3`
	insertStageSQL   = `
INSERT INTO hiring.app_job_stages (tenant_id, job_id, key, name, kind, position)
VALUES ($1::uuid, $2::uuid, 'custom-' || substr(gen_random_uuid()::text, 1, 8), $3, 'custom', $4)
RETURNING id::text, key`
	compactStagesSQL = `
UPDATE hiring.app_job_stages s SET position = r.n
FROM (SELECT id, (row_number() OVER (ORDER BY position, id) - 1)::int AS n
      FROM hiring.app_job_stages WHERE tenant_id = $1::uuid AND job_id = $2::uuid) r
WHERE s.id = r.id AND s.position <> r.n`
)

// CreateStage implements applications.PipelineStore. The new stage goes right before Hired.
func (s *Store) CreateStage(ctx context.Context, tenantID, jobID, actorID, name string) (*applications.Stage, error) {
	var out applications.Stage
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := ensureStages(ctx, tx, tenantID, jobID); err != nil {
			return err
		}
		existing, err := loadStages(ctx, tx, tenantID, jobID)
		if err != nil {
			return err
		}
		if len(existing) >= applications.MaxStages {
			return applications.ErrTooManyStages
		}
		if taken, err := stageNameTaken(ctx, tx, tenantID, jobID, name, "00000000-0000-0000-0000-000000000000"); err != nil {
			return fmt.Errorf("appstore: stage name: %w", err)
		} else if taken {
			return applications.ErrExists
		}
		var pos int
		if err = tx.QueryRow(ctx, hiredPositionSQL, tenantID, jobID).Scan(&pos); err != nil {
			return fmt.Errorf("appstore: hired stage position: %w", err)
		}
		if _, err = tx.Exec(ctx, shiftStagesSQL, tenantID, jobID, pos); err != nil {
			return fmt.Errorf("appstore: make room for stage: %w", err)
		}
		out = applications.Stage{Name: name, Kind: applications.KindCustom, Position: pos}
		if err = tx.QueryRow(ctx, insertStageSQL, tenantID, jobID, name, pos).Scan(&out.ID, &out.Key); err != nil {
			return fmt.Errorf("appstore: insert stage: %w", mapPgError(err))
		}
		return auditStage(ctx, tx, tenantID, jobID, actorID, "job.stage_created", map[string]any{"stage_id": out.ID, "name": name})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

const stageKindSQL = `SELECT kind FROM hiring.app_job_stages WHERE id = $1::uuid AND tenant_id = $2::uuid AND job_id = $3::uuid FOR UPDATE`

// RenameStage implements applications.PipelineStore.
func (s *Store) RenameStage(ctx context.Context, tenantID, jobID, stageID, actorID, name string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var kind string
		err := tx.QueryRow(ctx, stageKindSQL, stageID, tenantID, jobID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: find stage: %w", err)
		}
		switch kind {
		case applications.KindApplied, applications.KindHired, applications.KindDisqualified:
			return applications.ErrStageLocked
		}
		if taken, err := stageNameTaken(ctx, tx, tenantID, jobID, name, stageID); err != nil {
			return fmt.Errorf("appstore: stage name: %w", err)
		} else if taken {
			return applications.ErrExists
		}
		if _, err = tx.Exec(ctx, `UPDATE hiring.app_job_stages SET name = $4 WHERE id = $1::uuid AND tenant_id = $2::uuid AND job_id = $3::uuid`,
			stageID, tenantID, jobID, name); err != nil {
			return fmt.Errorf("appstore: rename stage: %w", err)
		}
		return auditStage(ctx, tx, tenantID, jobID, actorID, "job.stage_renamed", map[string]any{"stage_id": stageID, "name": name})
	})
}

// DeleteStage implements applications.PipelineStore.
func (s *Store) DeleteStage(ctx context.Context, tenantID, jobID, stageID, actorID string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var kind string
		err := tx.QueryRow(ctx, stageKindSQL, stageID, tenantID, jobID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: find stage: %w", err)
		}
		if kind != applications.KindCustom {
			return applications.ErrStageLocked
		}
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*)::int FROM hiring.applications WHERE stage_id = $1::uuid AND tenant_id = $2::uuid`,
			stageID, tenantID).Scan(&n); err != nil {
			return fmt.Errorf("appstore: count stage: %w", err)
		}
		if n > 0 {
			return applications.ErrStageInUse
		}
		if _, err = tx.Exec(ctx, `DELETE FROM hiring.app_job_stages WHERE id = $1::uuid AND tenant_id = $2::uuid`, stageID, tenantID); err != nil {
			return fmt.Errorf("appstore: delete stage: %w", err)
		}
		if _, err = tx.Exec(ctx, compactStagesSQL, tenantID, jobID); err != nil {
			return fmt.Errorf("appstore: compact stages: %w", err)
		}
		return auditStage(ctx, tx, tenantID, jobID, actorID, "job.stage_deleted", map[string]any{"stage_id": stageID})
	})
}

// ReorderStages implements applications.PipelineStore. ids must be exactly the open stages, Applied first.
func (s *Store) ReorderStages(ctx context.Context, tenantID, jobID, actorID string, ids []string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := ensureStages(ctx, tx, tenantID, jobID); err != nil {
			return err
		}
		stages, err := loadStages(ctx, tx, tenantID, jobID)
		if err != nil {
			return err
		}
		open := map[string]applications.Stage{}
		var closed []applications.Stage
		for _, st := range stages {
			if st.Final {
				closed = append(closed, st)
			} else {
				open[st.ID] = st
			}
		}
		if len(ids) != len(open) {
			return applications.ErrInvalidOrder
		}
		for _, id := range ids {
			if _, ok := open[id]; !ok {
				return applications.ErrInvalidOrder
			}
		}
		if open[ids[0]].Kind != applications.KindApplied {
			return applications.ErrInvalidOrder
		}
		order := append([]string{}, ids...)
		for _, st := range closed {
			order = append(order, st.ID)
		}
		pos := make([]int32, len(order))
		for i := range order {
			pos[i] = int32(i)
		}
		if _, err = tx.Exec(ctx, `
UPDATE hiring.app_job_stages s SET position = o.pos
FROM unnest($3::uuid[], $4::int[]) AS o(id, pos)
WHERE s.id = o.id AND s.tenant_id = $1::uuid AND s.job_id = $2::uuid`, tenantID, jobID, order, pos); err != nil {
			return fmt.Errorf("appstore: reorder stages: %w", err)
		}
		return auditStage(ctx, tx, tenantID, jobID, actorID, "job.stages_reordered", map[string]any{"order": order})
	})
}

// ---- moving between stages ----

const moveStateSQL = `
SELECT a.status, COALESCE(a.stage_id::text, ''), s.id::text, s.key, s.name, s.kind, s.position
FROM hiring.applications a
JOIN hiring.app_job_stages s ON s.id = $4::uuid AND s.tenant_id = a.tenant_id AND s.job_id = a.job_id
WHERE a.id = $1::uuid AND a.job_id = $2::uuid AND a.tenant_id = $3::uuid`

// MoveState implements applications.PipelineStore.
func (s *Store) MoveState(ctx context.Context, tenantID, jobID, appID, stageID string) (*applications.MoveState, error) {
	var ms applications.MoveState
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, moveStateSQL, appID, jobID, tenantID, stageID).Scan(
			&ms.Status, &ms.StageID, &ms.Target.ID, &ms.Target.Key, &ms.Target.Name, &ms.Target.Kind, &ms.Target.Position)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: move state: %w", err)
		}
		ms.Target.Final = applications.FinalKind(ms.Target.Kind)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &ms, nil
}

const moveOpenSQL = `
UPDATE hiring.applications SET stage_id = $4::uuid, status = $5
WHERE id = $1::uuid AND job_id = $2::uuid AND tenant_id = $3::uuid AND status = $6
  AND status IN ('received', 'in_review')
RETURNING 1`

// MoveToOpenStage implements applications.PipelineStore.
func (s *Store) MoveToOpenStage(ctx context.Context, tenantID string, m *applications.OpenMove) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var one int
		err := tx.QueryRow(ctx, moveOpenSQL, m.AppID, m.JobID, tenantID, m.To.ID, m.ToStatus, m.FromStatus).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return applications.ErrStale
		}
		if err != nil {
			return fmt.Errorf("appstore: move to stage: %w", err)
		}
		if err = markOpened(ctx, tx, tenantID, m.JobID, m.AppID, m.ActorID); err != nil {
			return err
		}
		var fromName string
		if m.FromStageID != "" {
			if err = tx.QueryRow(ctx, `SELECT name FROM hiring.app_job_stages WHERE id = $1::uuid AND tenant_id = $2::uuid`,
				m.FromStageID, tenantID).Scan(&fromName); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("appstore: from stage: %w", err)
			}
		}
		if err = insertEvent(ctx, tx, tenantID, m.JobID, m.AppID, m.ActorID, applications.EventStageChanged, map[string]any{
			"from_stage_id": m.FromStageID, "from_stage": fromName, "to_stage_id": m.To.ID, "to_stage": m.To.Name, "to_kind": m.To.Kind,
		}); err != nil {
			return err
		}
		if m.ToStatus != m.FromStatus {
			if err = insertEvent(ctx, tx, tenantID, m.JobID, m.AppID, m.ActorID, applications.EventStatusChanged,
				map[string]any{"from": m.FromStatus, "to": m.ToStatus}); err != nil {
				return err
			}
			if err = appevents.EnqueueStatusChanged(ctx, tx, appevents.StatusChanged{
				ApplicationID: m.AppID, JobID: m.JobID, CompanyID: tenantID, Region: s.Region, FromStatus: m.FromStatus,
				ToStatus: m.ToStatus, ChangedByUserID: m.ActorID, ChangedAt: m.Now,
			}); err != nil {
				return fmt.Errorf("appstore: emit status changed event: %w", err)
			}
		}
		if err = auditStage(ctx, tx, tenantID, m.JobID, m.ActorID, "application.stage_changed", map[string]any{
			"application_id": m.AppID, "from_stage_id": m.FromStageID, "to_stage_id": m.To.ID,
		}); err != nil {
			return err
		}
		if err = appevents.EnqueueStageChanged(ctx, tx, appevents.StageChanged{
			ApplicationID: m.AppID, JobID: m.JobID, CompanyID: tenantID, Region: s.Region, FromStageID: m.FromStageID,
			ToStageID: m.To.ID, ToStageKind: m.To.Kind, ChangedByUserID: m.ActorID, ChangedAt: m.Now,
		}); err != nil {
			return fmt.Errorf("appstore: emit stage changed event: %w", err)
		}
		return nil
	})
}

// ---- notes ----

const listNotesSQL = `
SELECT id::text, COALESCE(author_id::text, ''), body, created_at, updated_at
FROM hiring.app_notes WHERE tenant_id = $1::uuid AND application_id = $2::uuid
ORDER BY created_at DESC, id DESC LIMIT 200`

// Notes implements applications.PipelineStore.
func (s *Store) Notes(ctx context.Context, tenantID, jobID, appID, viewerID string) ([]applications.Note, error) {
	out := []applications.Note{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, listNotesSQL, tenantID, appID)
		if err != nil {
			return fmt.Errorf("appstore: list notes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var n applications.Note
			if err = rows.Scan(&n.ID, &n.AuthorID, &n.Body, &n.CreatedAt, &n.UpdatedAt); err != nil {
				return fmt.Errorf("appstore: scan note: %w", err)
			}
			n.Mine = n.AuthorID != "" && n.AuthorID == viewerID
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

// AddNote implements applications.PipelineStore.
func (s *Store) AddNote(ctx context.Context, tenantID, jobID, appID, authorID, body string) (*applications.Note, error) {
	var n applications.Note
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
INSERT INTO hiring.app_notes (tenant_id, application_id, author_id, body) VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
RETURNING id::text, COALESCE(author_id::text, ''), body, created_at, updated_at`, tenantID, appID, authorID, body).
			Scan(&n.ID, &n.AuthorID, &n.Body, &n.CreatedAt, &n.UpdatedAt)
		if err != nil {
			return fmt.Errorf("appstore: add note: %w", err)
		}
		n.Mine = true
		return insertEvent(ctx, tx, tenantID, jobID, appID, authorID, applications.EventNoteAdded, map[string]any{"note_id": n.ID})
	})
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// EditNote implements applications.PipelineStore. Only the author's own note is found.
func (s *Store) EditNote(ctx context.Context, tenantID, jobID, appID, noteID, authorID, body string) (*applications.Note, error) {
	var n applications.Note
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
UPDATE hiring.app_notes SET body = $5, updated_at = now()
WHERE id = $1::uuid AND application_id = $2::uuid AND tenant_id = $3::uuid AND author_id = $4::uuid
RETURNING id::text, COALESCE(author_id::text, ''), body, created_at, updated_at`, noteID, appID, tenantID, authorID, body).
			Scan(&n.ID, &n.AuthorID, &n.Body, &n.CreatedAt, &n.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: edit note: %w", err)
		}
		n.Mine = true
		return insertEvent(ctx, tx, tenantID, jobID, appID, authorID, applications.EventNoteEdited, map[string]any{"note_id": n.ID})
	})
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// DeleteNote implements applications.PipelineStore.
func (s *Store) DeleteNote(ctx context.Context, tenantID, jobID, appID, noteID, authorID string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
DELETE FROM hiring.app_notes WHERE id = $1::uuid AND application_id = $2::uuid AND tenant_id = $3::uuid AND author_id = $4::uuid`,
			noteID, appID, tenantID, authorID)
		if err != nil {
			return fmt.Errorf("appstore: delete note: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return insertEvent(ctx, tx, tenantID, jobID, appID, authorID, applications.EventNoteDeleted, map[string]any{"note_id": noteID})
	})
}

// ---- tags ----

// Tags implements applications.PipelineStore.
func (s *Store) Tags(ctx context.Context, tenantID string) ([]applications.Tag, error) {
	out := []applications.Tag{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, name, color FROM hiring.app_tags WHERE tenant_id = $1::uuid ORDER BY lower(name)`, tenantID)
		if err != nil {
			return fmt.Errorf("appstore: list tags: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var t applications.Tag
			if err = rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
				return fmt.Errorf("appstore: scan tag: %w", err)
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// CreateTag implements applications.PipelineStore.
func (s *Store) CreateTag(ctx context.Context, tenantID, actorID, name, color string) (*applications.Tag, error) {
	var t applications.Tag
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
INSERT INTO hiring.app_tags (tenant_id, name, color, created_by) VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid)
RETURNING id::text, name, color`, tenantID, name, color, actorID).Scan(&t.ID, &t.Name, &t.Color)
		if err != nil {
			if mapPgError(err) == ErrConflict {
				return applications.ErrExists
			}
			return fmt.Errorf("appstore: create tag: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// DeleteTag implements applications.PipelineStore.
func (s *Store) DeleteTag(ctx context.Context, tenantID, tagID string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM hiring.app_tags WHERE id = $1::uuid AND tenant_id = $2::uuid`, tagID, tenantID)
		if err != nil {
			return fmt.Errorf("appstore: delete tag: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

const applicationTagsSQL = `
SELECT t.id::text, t.name, t.color FROM hiring.app_application_tags at
JOIN hiring.app_tags t ON t.id = at.tag_id
WHERE at.application_id = $1::uuid AND at.tenant_id = $2::uuid ORDER BY lower(t.name)`

func loadApplicationTags(ctx context.Context, tx pgx.Tx, tenantID, appID string) ([]applications.Tag, error) {
	rows, err := tx.Query(ctx, applicationTagsSQL, appID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("appstore: application tags: %w", err)
	}
	defer rows.Close()
	out := []applications.Tag{}
	for rows.Next() {
		var t applications.Tag
		if err = rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
			return nil, fmt.Errorf("appstore: scan tag: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTags implements applications.PipelineStore. Every id must be one of the company's tags.
func (s *Store) SetTags(ctx context.Context, tenantID, jobID, appID, actorID string, tagIDs []string) ([]applications.Tag, error) {
	var out []applications.Tag
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		var known int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM hiring.app_tags WHERE tenant_id = $1::uuid AND id = ANY($2::uuid[])`,
			tenantID, tagIDs).Scan(&known); err != nil {
			return fmt.Errorf("appstore: check tags: %w", err)
		}
		if known != len(tagIDs) {
			return applications.ErrUnknownTag
		}
		before, err := loadApplicationTags(ctx, tx, tenantID, appID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM hiring.app_application_tags WHERE application_id = $1::uuid AND tenant_id = $2::uuid AND NOT (tag_id = ANY($3::uuid[]))`,
			appID, tenantID, tagIDs); err != nil {
			return fmt.Errorf("appstore: remove tags: %w", err)
		}
		if _, err = tx.Exec(ctx, `
INSERT INTO hiring.app_application_tags (application_id, tag_id, tenant_id, added_by)
SELECT $1::uuid, t, $2::uuid, NULLIF($4, '')::uuid FROM unnest($3::uuid[]) t ON CONFLICT DO NOTHING`,
			appID, tenantID, tagIDs, actorID); err != nil {
			return fmt.Errorf("appstore: add tags: %w", err)
		}
		if out, err = loadApplicationTags(ctx, tx, tenantID, appID); err != nil {
			return err
		}
		added, removed := tagDiff(before, out)
		if len(added)+len(removed) == 0 {
			return nil
		}
		return insertEvent(ctx, tx, tenantID, jobID, appID, actorID, applications.EventTagsChanged,
			map[string]any{"added": added, "removed": removed})
	})
	return out, err
}

func tagDiff(before, after []applications.Tag) (added, removed []string) {
	was, is := map[string]string{}, map[string]string{}
	for _, t := range before {
		was[t.ID] = t.Name
	}
	for _, t := range after {
		is[t.ID] = t.Name
	}
	added, removed = []string{}, []string{}
	for _, t := range after {
		if _, ok := was[t.ID]; !ok {
			added = append(added, t.Name)
		}
	}
	for _, t := range before {
		if _, ok := is[t.ID]; !ok {
			removed = append(removed, t.Name)
		}
	}
	return added, removed
}

// ---- follows and activity ----

// Follow implements applications.PipelineStore.
func (s *Store) Follow(ctx context.Context, tenantID, jobID, appID, userID string, on bool) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		var err error
		if on {
			_, err = tx.Exec(ctx, `INSERT INTO hiring.app_follows (application_id, user_id, tenant_id) VALUES ($1::uuid, $2::uuid, $3::uuid) ON CONFLICT DO NOTHING`,
				appID, userID, tenantID)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM hiring.app_follows WHERE application_id = $1::uuid AND user_id = $2::uuid AND tenant_id = $3::uuid`,
				appID, userID, tenantID)
		}
		if err != nil {
			return fmt.Errorf("appstore: follow: %w", err)
		}
		return nil
	})
}

// Activity implements applications.PipelineStore.
func (s *Store) Activity(ctx context.Context, tenantID, jobID, appID string, beforeID int64, limit int) ([]applications.Event, error) {
	var out []applications.Event
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
SELECT id, COALESCE(actor_id::text, ''), type, data, created_at FROM hiring.app_events
WHERE tenant_id = $1::uuid AND application_id = $2::uuid AND ($3::bigint = 0 OR id < $3::bigint)
ORDER BY id DESC LIMIT $4::int`, tenantID, appID, beforeID, limit)
		if err != nil {
			return fmt.Errorf("appstore: activity: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e applications.Event
			var at time.Time
			if err = rows.Scan(&e.ID, &e.ActorID, &e.Type, &e.Data, &at); err != nil {
				return fmt.Errorf("appstore: scan event: %w", err)
			}
			e.CreatedAt = at
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}
