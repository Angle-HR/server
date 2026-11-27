package hiringstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/pkg/formschema"
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

// publicCompanyFullSQL is publicCompanySQL plus the public profile.
const publicCompanyFullSQL = `
SELECT legal_name, COALESCE(privacy_contact_email, ''), COALESCE(dpo_contact, ''),
       COALESCE(slug, ''), COALESCE(display_name, ''), COALESCE(about, ''), COALESCE(website, ''), COALESCE(logo_url, '')
FROM accounts.organizations WHERE id = $1::uuid`

// frozenSchema is the shape freezeFormSQL writes: the form's questions as they were at publish.
type frozenSchema struct {
	Questions []formschema.FormQuestion `json:"questions"`
}

// loadFrozenForm returns the form id, newest version and questions. ErrNotFound when the job never published.
func loadFrozenForm(ctx context.Context, tx pgx.Tx, tenantID, jobID string) (string, int, []formschema.FormQuestion, error) {
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
		schema.Questions = []formschema.FormQuestion{}
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
		prof, err := s.companyProfile(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		p.Company.Name, p.Company.PrivacyEmail, p.Company.DPOContact = prof.Name, prof.PrivacyEmail, prof.DPOContact
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// InTenantTx runs fn in a transaction scoped to one company. It lets the JobSource adapter (package jobsource)
// read hiring tables with the same row-level-security rules as the rest of this package.
func (s *Store) InTenantTx(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	return s.inTenantTx(ctx, tenantID, fn)
}

// LoadDeclarations reads the special category declarations of a job.
func LoadDeclarations(ctx context.Context, tx pgx.Tx, jobID string) ([]formschema.Declaration, error) {
	return loadDeclarations(ctx, tx, jobID)
}

// LoadFrozenForm returns the form id, newest frozen version and questions of a job. ErrNotFound when the job
// never published.
func LoadFrozenForm(ctx context.Context, tx pgx.Tx, tenantID, jobID string) (string, int, []formschema.FormQuestion, error) {
	return loadFrozenForm(ctx, tx, tenantID, jobID)
}

// LoadPublicCompany reads what the company told candidates about itself.
func LoadPublicCompany(ctx context.Context, tx pgx.Tx, tenantID string) (name, privacyEmail, dpo string, err error) {
	if err = tx.QueryRow(ctx, publicCompanySQL, tenantID).Scan(&name, &privacyEmail, &dpo); err != nil {
		return "", "", "", fmt.Errorf("hiringstore: load company: %w", err)
	}
	return name, privacyEmail, dpo, nil
}

// LoadRulesSQL reads the screening rules of a job ($1 = job id): id, question id, operator, value, reason.
const LoadRulesSQL = loadRulesSQL
