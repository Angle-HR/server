package handler

import (
	"encoding/json"

	"github.com/Angle-HR/server/internal/apidoc"
)

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
