// Package admin provides admin identity, RBAC, and ops store helpers.
package admin

// Permission slugs enforced by middleware and seeded in migrations.
const (
	PermUsersRead     = "users:read"
	PermUsersWrite    = "users:write"
	PermCatalogsRead  = "catalogs:read"
	PermCatalogsWrite = "catalogs:write"
	PermJobsRead      = "jobs:read"
	PermJobsWrite     = "jobs:write"
	PermAdminsRead    = "admins:read"
	PermAdminsWrite   = "admins:write"
	PermAuditRead     = "audit:read"

	// PermVerificationReview covers the company verification (KYB) review queue.
	PermVerificationReview = "verification:review"

	// PermCompaniesDelete covers deleting a company (superadmin only).
	PermCompaniesDelete = "companies:delete"
)

// RoleSuperadmin is the bootstrap role slug.
const RoleSuperadmin = "superadmin"
