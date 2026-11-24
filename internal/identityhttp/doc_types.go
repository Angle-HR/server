package identityhttp

import "github.com/Angle-HR/server/internal/apidoc"

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
