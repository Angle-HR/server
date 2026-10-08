package hiringstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
)

// Shared types, aliased so the store's API reads in terms of them.
type (
	Department = hiringtypes.Department
	Template   = hiringtypes.Template
	Settings   = hiringtypes.Settings
)

// Template kinds.
const (
	KindJobDetails      = hiringtypes.KindJobDetails
	KindApplicationForm = hiringtypes.KindApplicationForm
)

// defaultTemplateName is the name a user's "Keep this setup" template is saved under.
const defaultTemplateName = "My default"

const listDepartmentsSQL = `
SELECT id::text, name FROM hiring.departments WHERE tenant_id = $1::uuid ORDER BY lower(name), id`

// The no-op update makes RETURNING fire when the department already exists (case-insensitively), so
// "create or pick" is a single idempotent call.
const upsertDepartmentSQL = `
INSERT INTO hiring.departments (tenant_id, name) VALUES ($1::uuid, $2::text)
ON CONFLICT (tenant_id, lower(name)) DO UPDATE SET name = hiring.departments.name
RETURNING id::text, name`

// ListDepartments returns the company's departments, A to Z.
func (s *Store) ListDepartments(ctx context.Context, tenantID string) ([]Department, error) {
	out := []Department{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listDepartmentsSQL, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Department
			if err := rows.Scan(&d.ID, &d.Name); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// EnsureDepartment returns the department with this name, creating it if needed.
func (s *Store) EnsureDepartment(ctx context.Context, tenantID, name string) (*Department, error) {
	var d Department
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, upsertDepartmentSQL, tenantID, name).Scan(&d.ID, &d.Name)
	})
	if err != nil {
		return nil, mapPgError(err)
	}
	return &d, nil
}

const templateColumns = `id::text, kind, name, payload, is_default, coalesce(created_by::text, ''),
    to_char(updated_at AT TIME ZONE 'UTC', '` + tsLayout + `')`

// Company templates are visible to everyone; a default is visible only to its owner.
const listTemplatesSQL = `
SELECT ` + templateColumns + ` FROM hiring.templates
WHERE tenant_id = $1::uuid AND ($2::text = '' OR kind = $2::text) AND (user_id IS NULL OR user_id = $3::uuid)
ORDER BY is_default DESC, lower(name), id`

const getTemplateSQL = `
SELECT ` + templateColumns + ` FROM hiring.templates
WHERE id = $1::uuid AND tenant_id = $2::uuid AND (user_id IS NULL OR user_id = $3::uuid)`

const insertTemplateSQL = `
INSERT INTO hiring.templates (tenant_id, kind, name, payload, created_by)
VALUES ($1::uuid, $2::text, $3::text, $4::jsonb, $5::uuid)
RETURNING ` + templateColumns

// A user's default is replaced, never duplicated.
const upsertDefaultTemplateSQL = `
INSERT INTO hiring.templates (tenant_id, kind, name, payload, is_default, user_id, created_by)
VALUES ($1::uuid, $2::text, $3::text, $4::jsonb, true, $5::uuid, $5::uuid)
ON CONFLICT (tenant_id, user_id, kind) WHERE is_default
DO UPDATE SET payload = EXCLUDED.payload, name = EXCLUDED.name
RETURNING ` + templateColumns

const deleteTemplateSQL = `
DELETE FROM hiring.templates
WHERE id = $1::uuid AND tenant_id = $2::uuid AND (user_id = $3::uuid OR user_id IS NULL)`

func scanTemplate(row pgx.Row) (*Template, error) {
	var t Template
	var payload []byte
	if err := row.Scan(&t.ID, &t.Kind, &t.Name, &payload, &t.IsDefault, &t.CreatedBy, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.Payload = json.RawMessage(payload)
	return &t, nil
}

// ListTemplates returns the company's templates of a kind (all kinds when empty) plus the caller's defaults.
func (s *Store) ListTemplates(ctx context.Context, tenantID, userID, kind string) ([]Template, error) {
	out := []Template{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listTemplatesSQL, tenantID, kind, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTemplate(rows)
			if err != nil {
				return err
			}
			out = append(out, *t)
		}
		return rows.Err()
	})
	return out, err
}

// GetTemplate loads one template the caller may see.
func (s *Store) GetTemplate(ctx context.Context, tenantID, userID, id string) (*Template, error) {
	var t *Template
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) (err error) {
		t, err = scanTemplate(tx.QueryRow(ctx, getTemplateSQL, id, tenantID, userID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// CreateTemplate saves a named company template. ErrConflict means the name is taken for that kind.
func (s *Store) CreateTemplate(
	ctx context.Context, tenantID, userID, kind, name string, payload json.RawMessage,
) (*Template, error) {
	var t *Template
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) (err error) {
		t, err = scanTemplate(tx.QueryRow(ctx, insertTemplateSQL, tenantID, kind, name, []byte(payload), userID))
		return err
	})
	if err != nil {
		return nil, mapPgError(err)
	}
	return t, nil
}

// SaveDefaultTemplate keeps the caller's setup for their future jobs, replacing any previous default.
func (s *Store) SaveDefaultTemplate(
	ctx context.Context, tenantID, userID, kind string, payload json.RawMessage,
) (*Template, error) {
	var t *Template
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) (err error) {
		row := tx.QueryRow(ctx, upsertDefaultTemplateSQL, tenantID, kind, defaultTemplateName, []byte(payload), userID)
		t, err = scanTemplate(row)
		return err
	})
	if err != nil {
		return nil, mapPgError(err)
	}
	return t, nil
}

// DeleteTemplate removes a template. Company templates can be removed by anyone the handler allows; a default
// only by its owner (the query hides other users' defaults).
func (s *Store) DeleteTemplate(ctx context.Context, tenantID, userID, id string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, deleteTemplateSQL, id, tenantID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

const getSettingsSQL = `
SELECT automated_screening_enabled_at IS NOT NULL,
       coalesce(to_char(automated_screening_enabled_at AT TIME ZONE 'UTC', '` + tsLayout + `'), '')
FROM hiring.org_settings WHERE tenant_id = $1::uuid`

const setScreeningSQL = `
INSERT INTO hiring.org_settings (tenant_id, automated_screening_enabled_at, automated_screening_enabled_by)
VALUES ($1::uuid, CASE WHEN $2::boolean THEN now() END, CASE WHEN $2::boolean THEN $3::uuid END)
ON CONFLICT (tenant_id) DO UPDATE SET
    automated_screening_enabled_at = CASE WHEN $2::boolean
        THEN coalesce(hiring.org_settings.automated_screening_enabled_at, now()) END,
    automated_screening_enabled_by = CASE WHEN $2::boolean
        THEN coalesce(hiring.org_settings.automated_screening_enabled_by, $3::uuid) END`

// GetSettings returns the company's settings; a company with no row has everything off.
func (s *Store) GetSettings(ctx context.Context, tenantID string) (Settings, error) {
	var st Settings
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, getSettingsSQL, tenantID).Scan(&st.AutomatedScreeningEnabled, &st.EnabledAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return st, err
}

// SetAutomatedScreening turns knockout questions and automatic flagging on or off for the company.
func (s *Store) SetAutomatedScreening(ctx context.Context, tenantID, actorID string, on bool) error {
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, setScreeningSQL, tenantID, on, actorID); err != nil {
			return fmt.Errorf("hiringstore: set screening: %w", err)
		}
		return nil
	})
	return err
}
