package hiringstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/gates"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/screening"
)

// memberRetentionMonths is how long a hiring-team record is kept after it is written. The retention schedule
// for staff access records (PL-04) is not final; this is a placeholder to be replaced by the schedule.
const memberRetentionMonths = 36

// ---- loading ----

const loadMembersSQL = `
SELECT user_id::text, role FROM hiring.job_members WHERE job_id = $1::uuid ORDER BY role, user_id`

const loadRulesSQL = `
SELECT id::text, field, operator, value, reason FROM hiring.disqualification_rules
WHERE job_id = $1::uuid ORDER BY position, id`

const loadConfirmationsSQL = `
SELECT gate_id, gate_version, confirmed FROM hiring.job_gate_confirmations WHERE job_id = $1::uuid ORDER BY gate_id`

const loadFormVersionSQL = `
SELECT COALESCE(max(v.version), 0) FROM hiring.form_versions v
JOIN hiring.application_forms f ON f.id = v.form_id WHERE f.job_id = $1::uuid`

// loadPhase2 fills the hiring team, screening rules, gate confirmations and form version of a record.
func loadPhase2(ctx context.Context, tx pgx.Tx, id string, rec *JobRecord) error {
	rows, err := tx.Query(ctx, loadMembersSQL, id)
	if err != nil {
		return fmt.Errorf("hiringstore: load members: %w", err)
	}
	rec.Members = []hiringtypes.Member{}
	for rows.Next() {
		var m hiringtypes.Member
		if err = rows.Scan(&m.UserID, &m.Role); err != nil {
			rows.Close()
			return fmt.Errorf("hiringstore: scan member: %w", err)
		}
		rec.Members = append(rec.Members, m)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("hiringstore: load members: %w", err)
	}

	rows, err = tx.Query(ctx, loadRulesSQL, id)
	if err != nil {
		return fmt.Errorf("hiringstore: load rules: %w", err)
	}
	rec.Rules = []screening.Rule{}
	for rows.Next() {
		var r screening.Rule
		var value []byte
		if err = rows.Scan(&r.ID, &r.QuestionID, &r.Operator, &value, &r.Reason); err != nil {
			rows.Close()
			return fmt.Errorf("hiringstore: scan rule: %w", err)
		}
		r.Value = json.RawMessage(value)
		rec.Rules = append(rec.Rules, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("hiringstore: load rules: %w", err)
	}

	rows, err = tx.Query(ctx, loadConfirmationsSQL, id)
	if err != nil {
		return fmt.Errorf("hiringstore: load confirmations: %w", err)
	}
	rec.Confirmations = []gates.Confirmation{}
	for rows.Next() {
		var c gates.Confirmation
		if err = rows.Scan(&c.GateID, &c.Version, &c.Confirmed); err != nil {
			rows.Close()
			return fmt.Errorf("hiringstore: scan confirmation: %w", err)
		}
		rec.Confirmations = append(rec.Confirmations, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("hiringstore: load confirmations: %w", err)
	}

	if err = tx.QueryRow(ctx, loadFormVersionSQL, id).Scan(&rec.FormVersion); err != nil {
		return fmt.Errorf("hiringstore: load form version: %w", err)
	}
	return nil
}

// ---- writing ----

// progressSQL records progress and a new revision for updates that do not rewrite the details.
const progressSQL = `
UPDATE hiring.job_postings SET current_step = $3, completed_sections = $4::text[], revision = revision + 1
WHERE id = $1::uuid AND tenant_id = $2::uuid AND deleted_at IS NULL
RETURNING revision`

// statusSQL moves the job to another status. published_at is set on the first publish only; closed_at is set
// when the job stops being open and cleared when it opens again.
const statusSQL = `
UPDATE hiring.job_postings SET
    status = $3,
    public_id = COALESCE(public_id, NULLIF($4, '')),
    published_at = CASE WHEN $3 = 'published' THEN COALESCE(published_at, now()) ELSE published_at END,
    closed_at = CASE WHEN $3 IN ('closed', 'expired', 'archived') THEN COALESCE(closed_at, now())
                     WHEN $3 = 'published' THEN NULL ELSE closed_at END,
    current_step = $5, completed_sections = $6::text[], revision = revision + 1
WHERE id = $1::uuid AND tenant_id = $2::uuid AND deleted_at IS NULL
RETURNING revision`

const freezeFormSQL = `
INSERT INTO hiring.form_versions (form_id, tenant_id, version, schema)
SELECT f.id, f.tenant_id,
       COALESCE((SELECT max(v.version) FROM hiring.form_versions v WHERE v.form_id = f.id), 0) + 1,
       jsonb_build_object(
           'form_revision', f.revision,
           'questions', COALESCE((SELECT jsonb_agg(to_jsonb(q) - 'form_id' - 'tenant_id' ORDER BY q.position)
                                  FROM hiring.form_questions q WHERE q.form_id = f.id), '[]'::jsonb))
FROM hiring.application_forms f WHERE f.job_id = $1::uuid AND f.tenant_id = $2::uuid`

const insertLogSQL = `
INSERT INTO hiring.compliance_log (job_id, tenant_id, gate_id, severity, event, actor_id)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, NULLIF($6, '')::uuid)`

const deleteMembersSQL = `DELETE FROM hiring.job_members WHERE job_id = $1::uuid`

const insertMembersSQL = `
INSERT INTO hiring.job_members
    (job_id, tenant_id, user_id, role, retention_until, lawful_basis, collection_purpose)
SELECT $1::uuid, $2::uuid, t.u, t.r, now() + make_interval(months => $5), 'legitimate_interests',
       'Controlling who can open this job posting and its candidates'
FROM unnest($3::uuid[], $4::text[]) AS t(u, r)`

const existingRuleIDsSQL = `SELECT id::text FROM hiring.disqualification_rules WHERE job_id = $1::uuid`

const deleteRulesSQL = `DELETE FROM hiring.disqualification_rules WHERE job_id = $1::uuid`

const insertRuleSQL = `
INSERT INTO hiring.disqualification_rules (id, job_id, tenant_id, field, operator, value, reason, position)
VALUES (COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2::uuid, $3::uuid, $4, $5, $6::jsonb, $7, $8)`

const setDPIAConfirmSQL = `
UPDATE hiring.job_postings SET
    dpia_confirmed_at = CASE WHEN $3 THEN COALESCE(dpia_confirmed_at, now()) ELSE NULL END
WHERE id = $1::uuid AND tenant_id = $2::uuid`

const upsertConfirmSQL = `
INSERT INTO hiring.job_gate_confirmations (job_id, tenant_id, gate_id, gate_version, confirmed, confirmed_by, confirmed_at)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, NULLIF($6, '')::uuid, now())
ON CONFLICT (job_id, gate_id) DO UPDATE SET
    gate_version = EXCLUDED.gate_version, confirmed = EXCLUDED.confirmed,
    confirmed_by = EXCLUDED.confirmed_by, confirmed_at = now()`

// applyPhase2 writes the status, team, rules and confirmation parts of an UpdateResult.
func applyPhase2(ctx context.Context, tx pgx.Tx, tenantID, actorID, id string, res *UpdateResult) error {
	switch {
	case res.Status != nil:
		if err := writeStatus(ctx, tx, tenantID, actorID, id, res); err != nil {
			return err
		}
	case res.Details == nil:
		// The details write bumps the revision itself; every other change bumps it here.
		completed := res.Completed
		if completed == nil {
			completed = []string{}
		}
		var revision int
		err := tx.QueryRow(ctx, progressSQL, id, tenantID, res.Step, completed).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("hiringstore: save progress: %w", mapPgError(err))
		}
	}
	if res.Members != nil {
		if err := writeMembers(ctx, tx, tenantID, id, *res.Members); err != nil {
			return err
		}
	}
	if res.Rules != nil {
		if err := writeRules(ctx, tx, tenantID, id, *res.Rules); err != nil {
			return err
		}
	}
	if res.DPIAConfirm != nil {
		if _, err := tx.Exec(ctx, setDPIAConfirmSQL, id, tenantID, *res.DPIAConfirm); err != nil {
			return fmt.Errorf("hiringstore: dpia scope: %w", err)
		}
	}
	if c := res.Confirm; c != nil {
		if _, err := tx.Exec(ctx, upsertConfirmSQL, id, tenantID, c.GateID, c.Version, c.Confirmed, actorID); err != nil {
			return fmt.Errorf("hiringstore: confirm gate: %w", mapPgError(err))
		}
		event := "confirmed"
		if !c.Confirmed {
			event = "withdrawn"
		}
		if _, err := tx.Exec(ctx, insertLogSQL, id, tenantID, c.GateID, c.Severity, event, actorID); err != nil {
			return fmt.Errorf("hiringstore: compliance log: %w", err)
		}
	}
	return nil
}

func writeStatus(ctx context.Context, tx pgx.Tx, tenantID, actorID, id string, res *UpdateResult) error {
	sc := res.Status
	completed := res.Completed
	if completed == nil {
		completed = []string{}
	}
	var revision int
	err := tx.QueryRow(ctx, statusSQL, id, tenantID, sc.To, sc.PublicID, res.Step, completed).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("hiringstore: change status: %w", mapPgError(err))
	}
	if sc.FreezeForm {
		if _, err = tx.Exec(ctx, freezeFormSQL, id, tenantID); err != nil {
			return fmt.Errorf("hiringstore: freeze form: %w", err)
		}
	}
	for _, l := range sc.Log {
		if _, err = tx.Exec(ctx, insertLogSQL, id, tenantID, l.GateID, l.Severity, l.Event, actorID); err != nil {
			return fmt.Errorf("hiringstore: compliance log: %w", err)
		}
	}
	return nil
}

func writeMembers(ctx context.Context, tx pgx.Tx, tenantID, id string, members []hiringtypes.Member) error {
	if _, err := tx.Exec(ctx, deleteMembersSQL, id); err != nil {
		return fmt.Errorf("hiringstore: clear members: %w", err)
	}
	if len(members) == 0 {
		return nil
	}
	users, roles := make([]string, len(members)), make([]string, len(members))
	for i, m := range members {
		users[i], roles[i] = m.UserID, m.Role
	}
	if _, err := tx.Exec(ctx, insertMembersSQL, id, tenantID, users, roles, memberRetentionMonths); err != nil {
		return fmt.Errorf("hiringstore: save members: %w", mapPgError(err))
	}
	return nil
}

func writeRules(ctx context.Context, tx pgx.Tx, tenantID, id string, rules []screening.Rule) error {
	existing := map[string]bool{}
	rows, err := tx.Query(ctx, existingRuleIDsSQL, id)
	if err != nil {
		return fmt.Errorf("hiringstore: list rules: %w", err)
	}
	for rows.Next() {
		var rid string
		if err = rows.Scan(&rid); err != nil {
			rows.Close()
			return fmt.Errorf("hiringstore: scan rule id: %w", err)
		}
		existing[strings.ToLower(rid)] = true
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("hiringstore: list rules: %w", err)
	}
	if _, err = tx.Exec(ctx, deleteRulesSQL, id); err != nil {
		return fmt.Errorf("hiringstore: clear rules: %w", err)
	}
	for i, r := range rules {
		rid := strings.ToLower(r.ID)
		if !existing[rid] { // an id that is not this job's own is never reused
			rid = ""
		}
		if _, err = tx.Exec(ctx, insertRuleSQL, rid, id, tenantID, r.QuestionID, r.Operator, []byte(r.Value),
			strings.TrimSpace(r.Reason), i); err != nil {
			return fmt.Errorf("hiringstore: save rule %d: %w", i, mapPgError(err))
		}
	}
	return nil
}

// ---- company ----

const orgMembersSQL = `
SELECT u.id::text,
       COALESCE(NULLIF(trim(concat_ws(' ', u.first_name, u.last_name)), ''), u.legal_full_name, ''),
       u.email
FROM accounts.users u
WHERE u.id = ANY($2::uuid[]) AND u.deleted_at IS NULL
  AND (EXISTS (SELECT 1 FROM accounts.organization_members m
               WHERE m.user_id = u.id AND m.organization_id = $1::uuid)
       OR EXISTS (SELECT 1 FROM accounts.organizations o WHERE o.owner_user_id = u.id AND o.id = $1::uuid))`

// OrgMembers returns the people in the company among userIDs. Accounts are not tenant-scoped tables, so this
// runs outside a tenant transaction; the company id in the query is the scope.
func (s *Store) OrgMembers(
	ctx context.Context, tenantID string, userIDs []string,
) (map[string]hiringtypes.Member, error) {
	out := map[string]hiringtypes.Member{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, orgMembersSQL, tenantID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: org members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m hiringtypes.Member
		if err = rows.Scan(&m.UserID, &m.Name, &m.Email); err != nil {
			return nil, fmt.Errorf("hiringstore: scan org member: %w", err)
		}
		out[m.UserID] = m
	}
	return out, rows.Err()
}

const companySetupSQL = `
SELECT o.kyb_status,
       (SELECT a.accepted_at FROM accounts.organization_agreements a
         WHERE a.organization_id = o.id AND a.kind = 'terms_dpa' AND a.version = $2),
       COALESCE(o.privacy_contact_email, ''), COALESCE(o.dpo_contact, '')
FROM accounts.organizations o WHERE o.id = $1::uuid`

const latestDPIASQL = `
SELECT version, to_char(recorded_at AT TIME ZONE 'UTC', '` + tsLayout + `')
FROM hiring.dpia_records WHERE tenant_id = $1::uuid ORDER BY version DESC LIMIT 1`

// CompanySetup reads the company's publish prerequisites. dpaVersion is the agreement version that counts as
// current.
func (s *Store) CompanySetup(ctx context.Context, tenantID, dpaVersion string) (*hiringtypes.CompanySetup, error) {
	out := &hiringtypes.CompanySetup{DPAVersion: dpaVersion}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var acceptedAt *time.Time
		err := tx.QueryRow(ctx, companySetupSQL, tenantID, dpaVersion).Scan(
			&out.KYBStatus, &acceptedAt, &out.PrivacyContact, &out.DPOContact)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("hiringstore: company setup: %w", err)
		}
		if acceptedAt != nil {
			out.DPAAccepted, out.DPAAcceptedAt = true, acceptedAt.UTC().Format(time.RFC3339)
		}
		err = tx.QueryRow(ctx, latestDPIASQL, tenantID).Scan(&out.DPIAVersion, &out.DPIARecordedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("hiringstore: latest dpia: %w", err)
		}
		return nil
	})
	return out, err
}

// CompanyState reduces CompanySetup to what the publish checks read.
func (s *Store) CompanyState(ctx context.Context, tenantID, dpaVersion string) (hiringtypes.CompanyState, error) {
	cs, err := s.CompanySetup(ctx, tenantID, dpaVersion)
	if err != nil {
		return hiringtypes.CompanyState{}, err
	}
	return hiringtypes.CompanyState{
		Verified: cs.KYBStatus == "verified", DPAAccepted: cs.DPAAccepted,
		PrivacyContactSet: strings.TrimSpace(cs.PrivacyContact) != "", DPIARecorded: cs.DPIAVersion > 0,
	}, nil
}

const acceptAgreementSQL = `
INSERT INTO accounts.organization_agreements (organization_id, kind, version, accepted_by)
VALUES ($1::uuid, 'terms_dpa', $2, $3::uuid)
ON CONFLICT (organization_id, kind, version) DO NOTHING`

// AcceptDPA records that a person accepted a version of the terms and DPA for the company.
func (s *Store) AcceptDPA(ctx context.Context, tenantID, userID, version string) error {
	_, err := s.DB.Exec(ctx, acceptAgreementSQL, tenantID, version, userID)
	if err != nil {
		return fmt.Errorf("hiringstore: accept agreement: %w", mapPgError(err))
	}
	return nil
}

const setPrivacyContactSQL = `
UPDATE accounts.organizations SET privacy_contact_email = NULLIF($2, ''), dpo_contact = NULLIF($3, '')
WHERE id = $1::uuid`

// SetPrivacyContact stores the contact applicants are told to write to.
func (s *Store) SetPrivacyContact(ctx context.Context, tenantID, email, dpo string) error {
	tag, err := s.DB.Exec(ctx, setPrivacyContactSQL, tenantID, email, dpo)
	if err != nil {
		return fmt.Errorf("hiringstore: privacy contact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const insertDPIASQL = `
INSERT INTO hiring.dpia_records (tenant_id, version, recorded_by, scope)
VALUES ($1::uuid, COALESCE((SELECT max(version) FROM hiring.dpia_records WHERE tenant_id = $1::uuid), 0) + 1,
        $2::uuid, $3::jsonb)
RETURNING version`

// RecordDPIA stores the company's DPIA as the next version and returns the version number.
func (s *Store) RecordDPIA(ctx context.Context, tenantID, userID string, scope json.RawMessage) (int, error) {
	var version int
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, insertDPIASQL, tenantID, userID, []byte(scope)).Scan(&version); err != nil {
			return fmt.Errorf("hiringstore: record dpia: %w", mapPgError(err))
		}
		return nil
	})
	return version, err
}
