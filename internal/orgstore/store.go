// Package orgstore reads and writes the company data org owns in a region's database: the organization row,
// members and roles, agreements and the privacy contact. It is the data layer behind the org gRPC API and the
// agreements routes. Region is always passed in: the org service has no per-request region of its own.
package orgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/orgevents"
	"github.com/Angle-HR/server/internal/region"
)

// DB is the part of a pgx pool the store needs.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RegionalDB returns the database of a region.
type RegionalDB func(reg region.Region) (DB, error)

// Errors the callers map to API responses.
var (
	ErrNotFound = errors.New("orgstore: not found")
	ErrInvalid  = errors.New("orgstore: invalid")
)

// Store is the data layer.
type Store struct {
	Regional RegionalDB
	// Slugs is the global careers page address registry. Only the profile calls need it.
	Slugs SlugRegistry
}

func (s *Store) db(reg region.Region) (DB, error) {
	if reg == region.RegionGlobal || !region.Valid(reg) {
		return nil, fmt.Errorf("%w: %q is not a company region", ErrInvalid, reg)
	}
	return s.Regional(reg)
}

// Company is the organization row.
type Company struct {
	ID, LegalName, Address, OwnerUserID string
	KYBStatus                           string
	PrivacyEmail, DPOContact            string
	Region                              string
	// Public profile: what the careers page shows. Empty when not set.
	Slug, DisplayName, About, Website, LogoURL string
}

const companyCols = `o.id::text, o.legal_name, coalesce(o.business_registered_address, ''), o.owner_user_id::text,
       o.kyb_status, coalesce(o.privacy_contact_email, ''), coalesce(o.dpo_contact, ''),
       coalesce(o.slug, ''), coalesce(o.display_name, ''), coalesce(o.about, ''), coalesce(o.website, ''), coalesce(o.logo_url, '')`

func scanCompany(row pgx.Row, reg region.Region) (*Company, error) {
	var c Company
	err := row.Scan(&c.ID, &c.LegalName, &c.Address, &c.OwnerUserID, &c.KYBStatus, &c.PrivacyEmail, &c.DPOContact,
		&c.Slug, &c.DisplayName, &c.About, &c.Website, &c.LogoURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("orgstore: company: %w", err)
	}
	c.Region = string(reg)
	return &c, nil
}

// Company loads a company by id.
func (s *Store) Company(ctx context.Context, reg region.Region, id string) (*Company, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	return scanCompany(d.QueryRow(ctx, `SELECT `+companyCols+` FROM accounts.organizations o WHERE o.id = $1::uuid AND o.deleted_at IS NULL`, id), reg)
}

// CompanyByOwner loads the company a user owns.
func (s *Store) CompanyByOwner(ctx context.Context, reg region.Region, ownerID string) (*Company, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	return scanCompany(d.QueryRow(ctx,
		`SELECT `+companyCols+` FROM accounts.organizations o WHERE o.owner_user_id = $1::uuid AND o.deleted_at IS NULL`, ownerID), reg)
}

// Contact is where notices to the company go.
type Contact struct{ LegalName, NoticeEmail string }

// Contact returns the privacy contact, else the owner's email.
func (s *Store) Contact(ctx context.Context, reg region.Region, id string) (*Contact, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	var c Contact
	err = d.QueryRow(ctx, `
SELECT o.legal_name, coalesce(nullif(o.privacy_contact_email, ''), u.email, '')
FROM accounts.organizations o LEFT JOIN accounts.users u ON u.id = o.owner_user_id
WHERE o.id = $1::uuid AND o.deleted_at IS NULL`, id).Scan(&c.LegalName, &c.NoticeEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("orgstore: contact: %w", err)
	}
	return &c, nil
}

// Readiness is the company-side input to the job publish gate.
type Readiness struct {
	KYBStatus    string
	DPAAccepted  bool
	DPAAcceptedA *time.Time
	PrivacyEmail string
	DPOContact   string
}

const readinessSQL = `
SELECT o.kyb_status,
       (SELECT a.accepted_at FROM accounts.organization_agreements a
         WHERE a.organization_id = o.id AND a.kind = 'terms_dpa' AND a.version = $2),
       COALESCE(o.privacy_contact_email, ''), COALESCE(o.dpo_contact, '')
FROM accounts.organizations o WHERE o.id = $1::uuid AND o.deleted_at IS NULL`

// Readiness reads KYB status, the acceptance of dpaVersion and the privacy contact.
func (s *Store) Readiness(ctx context.Context, reg region.Region, id, dpaVersion string) (*Readiness, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	var r Readiness
	err = d.QueryRow(ctx, readinessSQL, id, dpaVersion).Scan(&r.KYBStatus, &r.DPAAcceptedA, &r.PrivacyEmail, &r.DPOContact)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("orgstore: readiness: %w", err)
	}
	r.DPAAccepted = r.DPAAcceptedA != nil
	return &r, nil
}

// SetupExtras are the two settings-page fields that belong to hiring: the latest DPIA and the automated screening
// switch. INTERIM: they are read from hiring's tables so the agreements response keeps its shape; hiring should
// serve them through its own API and the BFF compose the page.
type SetupExtras struct {
	DPIAVersion       int
	DPIARecordedAt    string
	AutomatedScreenOn bool
}

// Extras reads SetupExtras. A company with no rows has everything off.
func (s *Store) Extras(ctx context.Context, reg region.Region, id string) (SetupExtras, error) {
	d, err := s.db(reg)
	if err != nil {
		return SetupExtras{}, err
	}
	var x SetupExtras
	err = d.QueryRow(ctx, `
SELECT version, to_char(recorded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
FROM hiring.dpia_records WHERE tenant_id = $1::uuid ORDER BY version DESC LIMIT 1`, id).Scan(&x.DPIAVersion, &x.DPIARecordedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return SetupExtras{}, fmt.Errorf("orgstore: latest dpia: %w", err)
	}
	err = d.QueryRow(ctx, `SELECT automated_screening_enabled_at IS NOT NULL FROM hiring.org_settings WHERE tenant_id = $1::uuid`,
		id).Scan(&x.AutomatedScreenOn)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return SetupExtras{}, fmt.Errorf("orgstore: automated screening: %w", err)
	}
	return x, nil
}

const acceptAgreementSQL = `
INSERT INTO accounts.organization_agreements (organization_id, kind, version, accepted_by)
VALUES ($1::uuid, $2, $3, $4::uuid)
ON CONFLICT (organization_id, kind, version) DO NOTHING
RETURNING accepted_at`

// AcceptAgreement records that userID accepted kind/version for the company, and emits org.agreement.accepted.v1 in
// the same transaction (when ORG_EVENTS=on). Accepting a version twice keeps the first acceptance and emits nothing.
func (s *Store) AcceptAgreement(ctx context.Context, reg region.Region, companyID, userID, kind, version string) (time.Time, error) {
	d, err := s.db(reg)
	if err != nil {
		return time.Time{}, err
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("orgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var at time.Time
	err = tx.QueryRow(ctx, acceptAgreementSQL, companyID, kind, version, userID).Scan(&at)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Already accepted: report the stored time.
		err = tx.QueryRow(ctx, `SELECT accepted_at FROM accounts.organization_agreements
WHERE organization_id = $1::uuid AND kind = $2 AND version = $3`, companyID, kind, version).Scan(&at)
		if err != nil {
			return time.Time{}, fmt.Errorf("orgstore: agreement: %w", err)
		}
		return at, nil
	case err != nil:
		return time.Time{}, fmt.Errorf("orgstore: accept agreement: %w", err)
	}
	companyKind, err := orgevents.KindOf(ctx, tx, companyID)
	if err != nil {
		return time.Time{}, err
	}
	if err = orgevents.EnqueueAgreementAccepted(ctx, tx, orgevents.AgreementAccepted{
		CompanyKind: companyKind, CompanyID: companyID, Region: string(reg), Kind: kind, Version: version, AcceptedByUserID: userID, AcceptedAt: at,
	}); err != nil {
		return time.Time{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return time.Time{}, fmt.Errorf("orgstore: commit: %w", err)
	}
	return at, nil
}

const setPrivacyContactSQL = `
UPDATE accounts.organizations SET privacy_contact_email = NULLIF($2, ''), dpo_contact = NULLIF($3, '')
WHERE id = $1::uuid`

// SetPrivacyContact stores the contact applicants are told to write to and emits org.company.profile_updated.v1 in
// the same transaction (when ORG_EVENTS=on).
func (s *Store) SetPrivacyContact(ctx context.Context, reg region.Region, companyID, email, dpo string) error {
	d, err := s.db(reg)
	if err != nil {
		return err
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("orgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, setPrivacyContactSQL, companyID, strings.TrimSpace(email), strings.TrimSpace(dpo))
	if err != nil {
		return fmt.Errorf("orgstore: privacy contact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	kind, err := orgevents.KindOf(ctx, tx, companyID)
	if err != nil {
		return err
	}
	if err = orgevents.EnqueueProfileUpdated(ctx, tx, orgevents.CompanyProfileUpdated{
		CompanyKind: kind, CompanyID: companyID, Region: string(reg), Changed: []string{"privacy_contact"}, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("orgstore: commit: %w", err)
	}
	return nil
}

// Member is a person in a company.
type Member struct {
	UserID  string
	Roles   []string
	IsOwner bool
}

// ListMembers lists the company's members (all of them, or those in userIDs) with their roles. The owner is always
// listed with the founder role.
func (s *Store) ListMembers(ctx context.Context, reg region.Region, companyID string, userIDs []string) ([]Member, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(ctx, `
SELECT u.id::text, (o.owner_user_id = u.id),
       coalesce(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}')::text[]
FROM accounts.organizations o
JOIN accounts.users u ON u.id = o.owner_user_id
  OR u.id IN (SELECT m.user_id FROM accounts.organization_members m WHERE m.organization_id = o.id)
LEFT JOIN accounts.organization_members m ON m.organization_id = o.id AND m.user_id = u.id
LEFT JOIN accounts.organization_member_roles r ON r.member_id = m.id
WHERE o.id = $1::uuid AND (cardinality($2::uuid[]) = 0 OR u.id = ANY($2::uuid[]))
GROUP BY u.id, o.owner_user_id
ORDER BY (o.owner_user_id = u.id) DESC, u.id`, companyID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("orgstore: members: %w", err)
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err = rows.Scan(&m.UserID, &m.IsOwner, &m.Roles); err != nil {
			return nil, fmt.Errorf("orgstore: scan member: %w", err)
		}
		if m.IsOwner && !contains(m.Roles, "founder") {
			m.Roles = append(m.Roles, "founder")
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListCompanies pages through the companies of a region by id (keyset on the id), limit rows at most.
func (s *Store) ListCompanies(ctx context.Context, reg region.Region, afterID string, limit int) ([]Company, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.Query(ctx, `SELECT `+companyCols+` FROM accounts.organizations o
WHERE o.deleted_at IS NULL AND ($1 = '' OR o.id > $1::uuid) ORDER BY o.id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("orgstore: list companies: %w", err)
	}
	defer rows.Close()
	var out []Company
	for rows.Next() {
		var c Company
		if err = rows.Scan(&c.ID, &c.LegalName, &c.Address, &c.OwnerUserID, &c.KYBStatus, &c.PrivacyEmail, &c.DPOContact); err != nil {
			return nil, fmt.Errorf("orgstore: scan company: %w", err)
		}
		c.Region = string(reg)
		out = append(out, c)
	}
	return out, rows.Err()
}

// validRoles are the roles organization_member_roles accepts.
var validRoles = map[string]bool{"founder": true, "hr_1": true, "hr_2": true, "line_manager": true, "payroll": true, "employee": true, "legal": true}

// AddMember adds a user to the company with the given roles (employee when none). Adding an existing member is a
// no-op that returns the existing member id. INTERIM write path: org invites are still accepted by the identity
// half, which writes the member row itself in its own transaction.
func (s *Store) AddMember(ctx context.Context, reg region.Region, companyID, userID string, roles []string) (string, error) {
	if len(roles) == 0 {
		roles = []string{"employee"}
	}
	for _, r := range roles {
		if !validRoles[r] {
			return "", fmt.Errorf("%w: unknown role %q", ErrInvalid, r)
		}
	}
	d, err := s.db(reg)
	if err != nil {
		return "", err
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("orgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `
INSERT INTO accounts.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'member')
ON CONFLICT (organization_id, user_id) DO UPDATE SET updated_at = now()
RETURNING id::text`, companyID, userID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("orgstore: add member: %w", err)
	}
	if err = setRoles(ctx, tx, id, roles); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("orgstore: commit: %w", err)
	}
	return id, nil
}

// SetMemberRoles replaces a member's roles.
func (s *Store) SetMemberRoles(ctx context.Context, reg region.Region, companyID, userID string, roles []string) error {
	for _, r := range roles {
		if !validRoles[r] {
			return fmt.Errorf("%w: unknown role %q", ErrInvalid, r)
		}
	}
	d, err := s.db(reg)
	if err != nil {
		return err
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("orgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM accounts.organization_members
WHERE organization_id = $1::uuid AND user_id = $2::uuid`, companyID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("orgstore: member: %w", err)
	}
	if err = setRoles(ctx, tx, id, roles); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func setRoles(ctx context.Context, tx pgx.Tx, memberID string, roles []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM accounts.organization_member_roles WHERE member_id = $1::uuid`, memberID); err != nil {
		return fmt.Errorf("orgstore: clear roles: %w", err)
	}
	for _, r := range roles {
		if _, err := tx.Exec(ctx, `INSERT INTO accounts.organization_member_roles (member_id, role) VALUES ($1::uuid, $2)
ON CONFLICT DO NOTHING`, memberID, r); err != nil {
			return fmt.Errorf("orgstore: set role: %w", err)
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

const deleteCompanySQL = `
UPDATE accounts.organizations SET deleted_at = now() WHERE id = $1::uuid AND deleted_at IS NULL RETURNING deleted_at`

const reactivateCompanySQL = `
UPDATE accounts.organizations SET deleted_at = NULL WHERE id = $1::uuid AND deleted_at IS NOT NULL RETURNING now()`

// ReactivateCompany undoes DeleteCompany and emits org.company.reactivated.v1 in the same transaction (when
// ORG_EVENTS=on). Reactivating a company that is not deleted is a no-op that reports alreadyActive; an unknown one
// is ErrNotFound. What the consumers removed when the company was deleted does not come back.
func (s *Store) ReactivateCompany(ctx context.Context, reg region.Region, companyID, reason string) (at time.Time, alreadyActive bool, err error) {
	d, err := s.db(reg)
	if err != nil {
		return time.Time{}, false, err
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("orgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, reactivateCompanySQL, companyID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		var one int
		err = tx.QueryRow(ctx, `SELECT 1 FROM accounts.organizations WHERE id = $1::uuid`, companyID).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, ErrNotFound
		}
		if err != nil {
			return time.Time{}, false, fmt.Errorf("orgstore: reactivate company: %w", err)
		}
		return time.Now().UTC(), true, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("orgstore: reactivate company: %w", err)
	}
	kind, err := orgevents.KindOf(ctx, tx, companyID)
	if err != nil {
		return time.Time{}, false, err
	}
	if err = orgevents.EnqueueReactivated(ctx, tx, orgevents.CompanyReactivated{
		CompanyKind: kind, CompanyID: companyID, Region: string(reg), ReactivatedAt: at.UTC(), Reason: reason,
	}); err != nil {
		return time.Time{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return time.Time{}, false, fmt.Errorf("orgstore: commit: %w", err)
	}
	return at, false, nil
}

// DeleteCompany marks a company deleted and emits org.company.deleted.v1 in the same transaction (when ORG_EVENTS=on).
// Deleting a deleted company is a no-op that reports alreadyDeleted; an unknown one is ErrNotFound. From then on the
// company no longer resolves, lists or answers readiness; its data is removed by the consumers of the event
// (applications, hiring).
func (s *Store) DeleteCompany(ctx context.Context, reg region.Region, companyID, reason string) (at time.Time, alreadyDeleted bool, err error) {
	d, err := s.db(reg)
	if err != nil {
		return time.Time{}, false, err
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("orgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, deleteCompanySQL, companyID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT deleted_at FROM accounts.organizations WHERE id = $1::uuid AND deleted_at IS NOT NULL`, companyID).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, ErrNotFound
		}
		if err != nil {
			return time.Time{}, false, fmt.Errorf("orgstore: deleted company: %w", err)
		}
		return at, true, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("orgstore: delete company: %w", err)
	}
	kind, err := orgevents.KindOf(ctx, tx, companyID)
	if err != nil {
		return time.Time{}, false, err
	}
	if err = orgevents.EnqueueDeleted(ctx, tx, orgevents.CompanyDeleted{
		CompanyKind: kind, CompanyID: companyID, Region: string(reg), DeletedAt: at.UTC(), Reason: reason,
	}); err != nil {
		return time.Time{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return time.Time{}, false, fmt.Errorf("orgstore: commit: %w", err)
	}
	return at, false, nil
}
