package orgstore

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/rbac"
)

// ErrNoCompany means the user has no company (an individual, or onboarding is not finished).
var ErrNoCompany = errors.New("orgstore: user has no company")

// Membership is a user's company, owner flag, roles and permissions.
type Membership struct {
	CompanyID, LegalName, Address string
	IsOwner                       bool
	// Roles are the member's roles; an owner always has founder among them.
	Roles []string
	Perms rbac.Set
}

// The user's company: the one they own, else one they are a member of. Roles come from the member's role rows; an
// owner is always a founder, so a company that has not assigned roles yet still works.
const membershipSQL = `
SELECT o.id::text, o.legal_name, coalesce(o.business_registered_address, ''), (o.owner_user_id = $1::uuid) AS is_owner,
       coalesce(array_agg(r.role) FILTER (WHERE r.role IS NOT NULL), '{}')::text[]
FROM accounts.organizations o
LEFT JOIN accounts.organization_members m ON m.organization_id = o.id AND m.user_id = $1::uuid
LEFT JOIN accounts.organization_member_roles r ON r.member_id = m.id
WHERE o.deleted_at IS NULL AND (o.owner_user_id = $1::uuid OR m.user_id = $1::uuid)
GROUP BY o.id, o.legal_name, o.business_registered_address, o.owner_user_id, o.created_at
ORDER BY (o.owner_user_id = $1::uuid) DESC, o.created_at
LIMIT 1`

// Resolve finds the user's company in region reg.
func (s *Store) Resolve(ctx context.Context, reg region.Region, userID string) (*Membership, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	var m Membership
	err = d.QueryRow(ctx, membershipSQL, userID).Scan(&m.CompanyID, &m.LegalName, &m.Address, &m.IsOwner, &m.Roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoCompany
	}
	if err != nil {
		return nil, fmt.Errorf("orgstore: resolve membership: %w", err)
	}
	if m.IsOwner && !contains(m.Roles, string(rbac.RoleFounder)) {
		m.Roles = append(m.Roles, string(rbac.RoleFounder))
	}
	m.Perms = PermissionsFor(m.Roles, m.IsOwner)
	return &m, nil
}

// PermissionsFor merges the default permissions of every role a person holds. Owners always include founder.
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

// PermissionList returns the permissions as sorted strings.
func PermissionList(s rbac.Set) []string {
	out := make([]string, 0, len(s))
	for p := range s {
		out = append(out, string(p))
	}
	sort.Strings(out)
	return out
}
