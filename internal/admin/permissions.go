// Package admin provides admin identity, RBAC, and ops store helpers.
package admin

// Permission slugs enforced by middleware and seeded in migrations.
const (
	PermWaitlistRead  = "waitlist:read"
	PermWaitlistWrite = "waitlist:write"
	PermUsersRead     = "users:read"
	PermUsersWrite    = "users:write"
	PermCatalogsRead  = "catalogs:read"
	PermCatalogsWrite = "catalogs:write"
	PermJobsRead      = "jobs:read"
	PermJobsWrite     = "jobs:write"
	PermAdminsRead    = "admins:read"
	PermAdminsWrite   = "admins:write"
	PermAuditRead     = "audit:read"
)

// RoleSuperadmin is the bootstrap role slug.
const RoleSuperadmin = "superadmin"
