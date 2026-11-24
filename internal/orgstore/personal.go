package orgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybstore"
	"github.com/Angle-HR/server/internal/orgevents"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/verifyqueue"
)

// KindBusiness and KindIndividual are the values of accounts.organizations.kind.
const (
	KindBusiness   = orgevents.KindBusiness
	KindIndividual = orgevents.KindIndividual
)

// PersonalCompanyRoleID is the company-role catalog entry ("Others") written to a personal organization, whose
// company_role_id column is NOT NULL. An individual is not asked for a role.
const PersonalCompanyRoleID = "60000000-0000-4000-8000-000000000008"

// ErrNotPersonal means the account owns an organization that is not a personal one.
var ErrNotPersonal = errors.New("orgstore: the account owns a business organization")

// Personal is what a personal organization is made from.
type Personal struct {
	UserID string
	// Name is the person's name; it becomes the organization's legal_name.
	Name string
	// CountryID and CountrySlug come from the profile step (the slug is looked up in the global catalog: the
	// regional database has the id only). CountrySlug is written to the review case as its country_code.
	CountryID, CountrySlug string
	// IndustryID, BusinessTypeID and EmployeeCount are optional; individuals are asked for the industry only.
	IndustryID, BusinessTypeID string
	EmployeeCount              int
}

// CaseQueue opens a review case (kyb.CaseQueue without Resolve).
type CaseQueue interface {
	EnqueueCase(ctx context.Context, organizationID, countryCode, caseType string) error
}

// PersonalOrg is the outcome of EnsurePersonal.
type PersonalOrg struct {
	ID string
	// Status is the organization's kyb_status after the call: pending (waiting for the reviewer), verified or failed.
	Status string
	// Deleted is true when the personal organization was deleted by an operator; nothing is recreated then.
	Deleted bool
	Created bool
}

const insertPersonalSQL = `
INSERT INTO accounts.organizations
    (owner_user_id, legal_name, company_role_id, business_type_id, industry_id, employee_count, country_id, kind)
VALUES ($1::uuid, $2, $3::uuid, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, 0), NULLIF($7, '')::uuid, 'individual')
ON CONFLICT (owner_user_id) DO NOTHING`

const selectPersonalSQL = `
SELECT id::text, kind, kyb_status, deleted_at IS NOT NULL FROM accounts.organizations WHERE owner_user_id = $1::uuid`

// EnsurePersonal creates the user's personal organization when it does not exist and makes sure it has been handed
// to the reviewer. Every step is idempotent, so a failed or repeated call (onboarding retried, backfill rerun) only
// completes what is missing. An organization that was deleted is left alone. The owner needs no member row: an owner
// always resolves with the founder role.
func (s *Store) EnsurePersonal(ctx context.Context, reg region.Region, p Personal, q CaseQueue) (*PersonalOrg, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.UserID == "" || p.Name == "" {
		return nil, fmt.Errorf("%w: a personal organization needs a user and a name", ErrInvalid)
	}
	tag, err := d.Exec(ctx, insertPersonalSQL, p.UserID, p.Name, PersonalCompanyRoleID, p.BusinessTypeID, p.IndustryID,
		p.EmployeeCount, p.CountryID)
	if err != nil {
		return nil, fmt.Errorf("orgstore: create personal organization: %w", err)
	}
	out := PersonalOrg{Created: tag.RowsAffected() > 0}
	var kind string
	if err = d.QueryRow(ctx, selectPersonalSQL, p.UserID).Scan(&out.ID, &kind, &out.Status, &out.Deleted); err != nil {
		return nil, fmt.Errorf("orgstore: personal organization: %w", err)
	}
	if kind != KindIndividual {
		return nil, ErrNotPersonal
	}
	if out.Deleted {
		return &out, nil
	}
	if out.Status == string(kyb.StatusNotStarted) || out.Status == string(kyb.StatusPending) {
		rec, rerr := s.requestPersonalReview(ctx, reg, out.ID, p.UserID, p.CountrySlug, q)
		if rerr != nil {
			return nil, rerr
		}
		out.Status = string(rec.Status)
	}
	return &out, nil
}

// RequestPersonalReview puts a personal organization in front of the reviewer: the verification record becomes
// pending (tier 2, attempts counted, history event written, org events for the transition when ORG_EVENTS=on) and a
// case with type individual is opened. A pending organization only has its case re-opened if it is missing (the call
// that created it may have died between the two writes). A verified one answers kyb.ErrAlreadyVerified. A failed one
// goes back to pending: that is "request a new review".
func (s *Store) RequestPersonalReview(
	ctx context.Context, reg region.Region, orgID, actorID string, q CaseQueue,
) (*kyb.Record, error) {
	return s.requestPersonalReview(ctx, reg, orgID, actorID, "", q)
}

// requestPersonalReview is RequestPersonalReview; countryCode (the country's catalog slug) is used when the
// organization has no verification record yet.
func (s *Store) requestPersonalReview(
	ctx context.Context, reg region.Region, orgID, actorID, countryCode string, q CaseQueue,
) (*kyb.Record, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	store := &kybstore.Store{DB: d, OnSave: orgevents.KYBSaveHook(string(reg))}
	rec, err := store.Load(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var name, country string
	err = d.QueryRow(ctx, `
SELECT legal_name, coalesce((SELECT v.country_code FROM accounts.organization_verifications v WHERE v.organization_id = o.id), '')
FROM accounts.organizations o WHERE o.id = $1::uuid AND o.kind = 'individual' AND o.deleted_at IS NULL`, orgID).Scan(&name, &country)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("orgstore: personal organization: %w", err)
	}
	if rec != nil && rec.Status == kyb.StatusVerified {
		return rec, kyb.ErrAlreadyVerified
	}
	if rec == nil || rec.Status != kyb.StatusPending {
		now := time.Now().UTC()
		if rec == nil {
			rec = &kyb.Record{OrganizationID: orgID}
		}
		if country == "" {
			country = countryCode
		}
		if country == "" {
			country = "individual"
		}
		rec.CountryCode, rec.LegalName, rec.Tier, rec.Status = country, name, int(kyb.Tier2), kyb.StatusPending
		rec.FailureReason, rec.ReviewerID, rec.VerifiedAt, rec.CheckedAt = "", "", nil, &now
		rec.Attempts++
		rec.NoticesSent = 0
		ev := kyb.Event{OrganizationID: orgID, Name: "queued_for_review", Status: kyb.StatusPending, ActorID: actorID,
			Detail: map[string]any{"case_type": verifyqueue.CaseIndividual}, At: now}
		if err = store.Save(ctx, rec, ev); err != nil {
			return nil, err
		}
	}
	if err = q.EnqueueCase(ctx, orgID, rec.CountryCode, verifyqueue.CaseIndividual); err != nil {
		return nil, err
	}
	return rec, nil
}

// KindOfCompany returns the kind of a (not deleted) company.
func (s *Store) KindOfCompany(ctx context.Context, reg region.Region, companyID string) (string, error) {
	d, err := s.db(reg)
	if err != nil {
		return "", err
	}
	var kind string
	err = d.QueryRow(ctx, `SELECT kind FROM accounts.organizations WHERE id = $1::uuid AND deleted_at IS NULL`, companyID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("orgstore: company kind: %w", err)
	}
	return kind, nil
}
