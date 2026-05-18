package catalog

import "github.com/Angle-HR/server/internal/apidoc"

// IndustryListEnvelope is a catalog industries response.
type IndustryListEnvelope struct {
	Data []Industry   `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// HiringToolListEnvelope is a catalog hiring tools response.
type HiringToolListEnvelope struct {
	Data []HiringTool `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// HiringFrustrationListEnvelope is a catalog hiring frustrations response.
type HiringFrustrationListEnvelope struct {
	Data []HiringFrustration `json:"data"`
	Meta *apidoc.Meta        `json:"meta,omitempty"`
}

// RoleListEnvelope is a catalog roles response.
type RoleListEnvelope struct {
	Data []Role       `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// TeamSizeListEnvelope is a catalog team sizes response.
type TeamSizeListEnvelope struct {
	Data []TeamSize   `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}
