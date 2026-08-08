package handler

import (
	"encoding/json"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/apidoc"
)

// AdminLoginRequest is the admin login body.
type AdminLoginRequest struct {
	Email    string `json:"email" example:"admin@anglehr.local"`
	Password string `json:"password" example:"changeme123"`
}

// AdminRefreshRequest is the admin refresh body.
type AdminRefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// AdminAcceptInviteRequest is the accept-invite body.
type AdminAcceptInviteRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// AdminTokenData is the admin token response payload.
type AdminTokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type" example:"Bearer"`
}

// AdminTokenEnvelope wraps admin tokens.
type AdminTokenEnvelope struct {
	Data AdminTokenData `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
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

// AdminWaitlistListEnvelope wraps waitlist list responses.
type AdminWaitlistListEnvelope struct {
	Data []adminWaitlistItem `json:"data"`
	Meta *apidoc.Meta        `json:"meta,omitempty"`
}

// AdminWaitlistDetailEnvelope wraps waitlist detail responses.
type AdminWaitlistDetailEnvelope struct {
	Data adminWaitlistDetail `json:"data"`
	Meta *apidoc.Meta        `json:"meta,omitempty"`
}

// AdminWaitlistPatchRequest is the waitlist patch body.
type AdminWaitlistPatchRequest struct {
	WantsEarlyAccess *bool           `json:"wants_early_access"`
	WantsUserTesting *bool           `json:"wants_user_testing"`
	Metadata         json.RawMessage `json:"metadata"`
}

// AdminUserListEnvelope wraps user list responses.
type AdminUserListEnvelope struct {
	Data []adminUserListItem `json:"data"`
	Meta *apidoc.Meta        `json:"meta,omitempty"`
}

// AdminUserDetailEnvelope wraps user detail responses.
type AdminUserDetailEnvelope struct {
	Data adminUserDetail `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}

// AdminUserPatchRequest is the user patch body.
type AdminUserPatchRequest struct {
	Deleted *bool `json:"deleted"`
}

// AdminCatalogListEnvelope wraps catalog list responses.
type AdminCatalogListEnvelope struct {
	Data []json.RawMessage `json:"data"`
	Meta *apidoc.Meta      `json:"meta,omitempty"`
}

// AdminCatalogItemEnvelope wraps a single catalog item.
type AdminCatalogItemEnvelope struct {
	Data json.RawMessage `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}

// AdminCatalogUpsertRequest is create/patch catalog body.
type AdminCatalogUpsertRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Label       *string `json:"label"`
	Slug        *string `json:"slug"`
	Emoji       *string `json:"emoji"`
	IconURL     *string `json:"icon_url"`
	IconKey     *string `json:"icon_key"`
	Region      *string `json:"region"`
	SortOrder   *int    `json:"sort_order"`
	IsActive    *bool   `json:"is_active"`
	MinSize     *int    `json:"min_size"`
	MaxSize     *int    `json:"max_size"`
}

// AdminJobListEnvelope wraps job list responses.
type AdminJobListEnvelope struct {
	Data []adminJobView `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

// AdminJobEnvelope wraps a single job.
type AdminJobEnvelope struct {
	Data adminJobView `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
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
