// Package callerresolver is the INTERIM implementation of apphttp.CallerResolver: it resolves company, owner flag
// and roles of a signed-in user from the accounts.* tables of the user's regional database.
//
// It exists on the monolith side because the accounts tables belong to org. It is shared by the monolith (through
// hiringhttp.PostgresHiringDirectory) and by the applications service, which runs it against the shared regional
// databases until org exposes membership over gRPC (docs/microservices/BOUNDARIES.md item 1). When that lands an
// org client replaces Resolver and this package is deleted.
package callerresolver

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
)

// DB is the part of a pgx pool the lookup needs.
type DB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RegionalDB returns the database of a region.
type RegionalDB func(reg region.Region) (DB, error)

// ErrOnboardingIncomplete is returned for an account still in the pre-onboarding holding area (region global).
var ErrOnboardingIncomplete = apperror.New(apperror.CodeOnboardingIncomplete, apperror.MsgOnboardingStepIncomplete)

// The user's company: the one they own, else one they are a member of. Roles come from the member's role rows;
// an owner is always a founder, so a company that has not assigned roles yet still works.
const orgForUserSQL = `
SELECT o.id::text, o.legal_name, coalesce(o.business_registered_address, ''), (o.owner_user_id = $1::uuid) AS is_owner,
       coalesce(array_agg(r.role) FILTER (WHERE r.role IS NOT NULL), '{}')::text[]
FROM accounts.organizations o
LEFT JOIN accounts.organization_members m ON m.organization_id = o.id AND m.user_id = $1::uuid
LEFT JOIN accounts.organization_member_roles r ON r.member_id = m.id
WHERE o.owner_user_id = $1::uuid OR m.user_id = $1::uuid
GROUP BY o.id, o.legal_name, o.business_registered_address, o.owner_user_id, o.created_at
ORDER BY (o.owner_user_id = $1::uuid) DESC, o.created_at
LIMIT 1`

// Membership is a user's company as the accounts tables describe it.
type Membership struct {
	OrgID, CompanyName, CompanyAddress string
	Owner                              bool
	// Roles are the member's roles; an owner always has founder among them.
	Roles []string
	Perms rbac.Set
}

// Resolver reads memberships from the regional databases.
type Resolver struct{ Regional RegionalDB }

// Lookup finds the user's company. It returns apphttp.ErrNoOrganization when there is none.
func (r *Resolver) Lookup(ctx context.Context, userID string, reg region.Region) (*Membership, error) {
	if reg == region.RegionGlobal {
		return nil, ErrOnboardingIncomplete
	}
	pool, err := r.Regional(reg)
	if err != nil {
		return nil, err
	}
	var m Membership
	err = pool.QueryRow(ctx, orgForUserSQL, userID).Scan(&m.OrgID, &m.CompanyName, &m.CompanyAddress, &m.Owner, &m.Roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apphttp.ErrNoOrganization
	}
	if err != nil {
		return nil, err
	}
	if m.Owner && !hasRole(m.Roles, string(rbac.RoleFounder)) {
		m.Roles = append(m.Roles, string(rbac.RoleFounder))
	}
	m.Perms = PermissionsFor(m.Roles, m.Owner)
	return &m, nil
}

// ResolveCaller implements apphttp.CallerResolver.
func (r *Resolver) ResolveCaller(ctx context.Context, userID string, reg region.Region) (applications.Caller, error) {
	m, err := r.Lookup(ctx, userID, reg)
	if err != nil {
		return applications.Caller{}, err
	}
	return applications.Caller{UserID: userID, OrgID: m.OrgID, Region: string(reg), Perms: m.Perms, Roles: m.Roles}, nil
}

var _ apphttp.CallerResolver = (*Resolver)(nil)

// PermissionsFor merges the default permissions of every role a person holds.
func PermissionsFor(roles []string, owner bool) rbac.Set {
	var perms []rbac.Permission
	seen := map[rbac.Role]bool{}
	add := func(r rbac.Role) {
		if !seen[r] {
			seen[r] = true
			perms = append(perms, rbac.DefaultMatrix[r]...)
		}
	}
	if owner {
		add(rbac.RoleFounder)
	}
	for _, r := range roles {
		add(rbac.Role(r))
	}
	return rbac.NewSet(perms...)
}

func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}
