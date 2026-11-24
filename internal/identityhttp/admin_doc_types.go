package identityhttp

import (
	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/apidoc"
)

// AdminLoginRequest is the admin login body.
type AdminLoginRequest struct {
	Email    string `json:"email" example:"admin@anglehr.local"`
	Password string `json:"password" example:"changeme123"`
}

// AdminAcceptInviteRequest is the accept-invite body.
type AdminAcceptInviteRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// AdminSessionData is the admin login response payload; the session rides in the Set-Cookie header.
type AdminSessionData struct {
	User      map[string]any `json:"user"`
	CSRFToken string         `json:"csrf_token"`
}

// AdminTokenEnvelope wraps the admin session response.
type AdminTokenEnvelope struct {
	Data AdminSessionData `json:"data"`
	Meta *apidoc.Meta     `json:"meta,omitempty"`
}

// AdminInvitePreviewEnvelope wraps invite preview.
type AdminInvitePreviewEnvelope struct {
	Data admin.InvitePreview `json:"data"`
	Meta *apidoc.Meta        `json:"meta,omitempty"`
}

// AdminMeEnvelope wraps /admin/auth/me.
type AdminMeEnvelope struct {
	Data map[string]any `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

// AdminUserPatchRequest is the user patch body.
type AdminUserPatchRequest struct {
	Deleted *bool `json:"deleted"`
}

// AdminStaffListEnvelope wraps staff list responses.
type AdminStaffListEnvelope struct {
	Data []admin.StaffMember `json:"data"`
	Meta *apidoc.Meta        `json:"meta,omitempty"`
}

// AdminStaffEnvelope wraps a staff member.
type AdminStaffEnvelope struct {
	Data admin.StaffMember `json:"data"`
	Meta *apidoc.Meta      `json:"meta,omitempty"`
}

// AdminCreateStaffRequest is the invite staff body.
type AdminCreateStaffRequest struct {
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

// AdminPatchStaffRequest is the patch staff body.
type AdminPatchStaffRequest struct {
	Name     *string   `json:"name"`
	IsActive *bool     `json:"is_active"`
	Roles    *[]string `json:"roles"`
}

// AdminRolesEnvelope wraps roles matrix.
type AdminRolesEnvelope struct {
	Data []admin.RoleWithPermissions `json:"data"`
	Meta *apidoc.Meta                `json:"meta,omitempty"`
}

// AdminRoleEnvelope wraps a single role.
type AdminRoleEnvelope struct {
	Data admin.RoleWithPermissions `json:"data"`
	Meta *apidoc.Meta              `json:"meta,omitempty"`
}

// AdminCreateRoleRequest is the create role body.
type AdminCreateRoleRequest struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// AdminPatchRoleRequest is the patch role body.
type AdminPatchRoleRequest struct {
	Name        *string   `json:"name"`
	Permissions *[]string `json:"permissions"`
}

// AdminPermissionsEnvelope wraps the permission catalog.
type AdminPermissionsEnvelope struct {
	Data []admin.Permission `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

// AdminAuditListEnvelope wraps audit log list responses.
type AdminAuditListEnvelope struct {
	Data []admin.AuditLog `json:"data"`
	Meta *apidoc.Meta     `json:"meta,omitempty"`
}
