package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

type catalogDef struct {
	schema     string
	table      string
	hasActive  bool
	hasEmoji   bool
	hasIconURL bool
	hasIconKey bool
	hasRegion  bool
	hasDesc    bool
	hasLabel   bool
	hasMinMax  bool
}

var catalogDefs = map[string]catalogDef{
	"countries":             {schema: "waitlist", table: "countries", hasActive: true, hasIconKey: true, hasRegion: true},
	"industries":            {schema: "waitlist", table: "industries", hasActive: true, hasEmoji: true},
	"hiring_tools":          {schema: "waitlist", table: "hiring_tools", hasActive: true, hasIconURL: true},
	"hiring_frustrations":   {schema: "waitlist", table: "hiring_frustrations", hasActive: true, hasEmoji: true, hasDesc: true},
	"roles":                 {schema: "waitlist", table: "roles", hasActive: true, hasEmoji: true},
	"team_sizes":            {schema: "waitlist", table: "team_sizes", hasLabel: true, hasMinMax: true},
	"business_types":        {schema: "accounts", table: "business_types", hasActive: true},
	"onboarding_industries": {schema: "accounts", table: "onboarding_industries", hasActive: true, hasEmoji: true},
	"company_roles":         {schema: "accounts", table: "company_roles", hasActive: true, hasIconKey: true},
}

// listCatalog godoc
//
//	@Summary		List catalog entries
//	@Tags			admin/catalogs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			type	path		string	true	"Catalog type"
//	@Success		200		{object}	handler.AdminCatalogListEnvelope
//	@Router			/admin/catalogs/{type} [get]
func (h *AdminHandler) listCatalog(w http.ResponseWriter, r *http.Request) {
	def, ok := catalogDefs[chi.URLParam(r, "type")]
	if !ok {
		response.Error(w, r, apperror.New(apperror.CodeNotFound, "unknown catalog type"))
		return
	}

	sql := fmt.Sprintf(`SELECT row_to_json(t) FROM %s.%s t ORDER BY sort_order, created_at`, def.schema, def.table)
	rows, err := h.GlobalDB.Query(r.Context(), sql)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	defer rows.Close()

	var items []json.RawMessage
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			response.Error(w, r, err)
			return
		}
		items = append(items, raw)
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	response.Success(w, r, http.StatusOK, items)
}

type catalogCreateBody struct {
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

// createCatalog godoc
//
//	@Summary		Create catalog entry
//	@Tags			admin/catalogs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			type	path		string							true	"Catalog type"
//	@Param			body	body		handler.AdminCatalogUpsertRequest	true	"Create payload"
//	@Success		201		{object}	handler.AdminCatalogItemEnvelope
//	@Router			/admin/catalogs/{type} [post]
func (h *AdminHandler) createCatalog(w http.ResponseWriter, r *http.Request) {
	typeName := chi.URLParam(r, "type")
	def, ok := catalogDefs[typeName]
	if !ok {
		response.Error(w, r, apperror.New(apperror.CodeNotFound, "unknown catalog type"))
		return
	}

	var body catalogCreateBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	id := uuid.New()
	sortOrder := 0
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	}
	active := true
	if body.IsActive != nil {
		active = *body.IsActive
	}

	var err error
	switch {
	case def.hasLabel:
		if body.Label == nil || strings.TrimSpace(*body.Label) == "" {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "label is required"))
			return
		}
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			INSERT INTO %s.%s (id, label, min_size, max_size, sort_order)
			VALUES ($1, $2, $3, $4, $5)
		`, def.schema, def.table), id, strings.TrimSpace(*body.Label), body.MinSize, body.MaxSize, sortOrder)
	case def.hasDesc:
		if body.Description == nil || body.Slug == nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "description and slug are required"))
			return
		}
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			INSERT INTO %s.%s (id, description, slug, emoji, sort_order, is_active)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, def.schema, def.table), id, strings.TrimSpace(*body.Description), strings.TrimSpace(*body.Slug), body.Emoji, sortOrder, active)
	case def.hasRegion:
		if body.Name == nil || body.Slug == nil || body.Region == nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "name, slug, and region are required"))
			return
		}
		if _, err := region.ParseRegion(*body.Region); err != nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRegion))
			return
		}
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			INSERT INTO %s.%s (id, name, slug, region, icon_key, sort_order, is_active)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, def.schema, def.table), id, strings.TrimSpace(*body.Name), strings.TrimSpace(*body.Slug), *body.Region, body.IconKey, sortOrder, active)
	case def.hasIconURL:
		if body.Name == nil || body.Slug == nil || body.IconURL == nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "name, slug, and icon_url are required"))
			return
		}
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			INSERT INTO %s.%s (id, name, slug, icon_url, sort_order, is_active)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, def.schema, def.table), id, strings.TrimSpace(*body.Name), strings.TrimSpace(*body.Slug), strings.TrimSpace(*body.IconURL), sortOrder, active)
	default:
		if body.Name == nil || body.Slug == nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "name and slug are required"))
			return
		}
		switch {
		case def.hasEmoji:
			_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
				INSERT INTO %s.%s (id, name, slug, emoji, sort_order, is_active)
				VALUES ($1, $2, $3, $4, $5, $6)
			`, def.schema, def.table), id, strings.TrimSpace(*body.Name), strings.TrimSpace(*body.Slug), body.Emoji, sortOrder, active)
		case def.hasIconKey:
			_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
				INSERT INTO %s.%s (id, name, slug, icon_key, sort_order, is_active)
				VALUES ($1, $2, $3, $4, $5, $6)
			`, def.schema, def.table), id, strings.TrimSpace(*body.Name), strings.TrimSpace(*body.Slug), body.IconKey, sortOrder, active)
		default:
			_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
				INSERT INTO %s.%s (id, name, slug, sort_order, is_active)
				VALUES ($1, $2, $3, $4, $5)
			`, def.schema, def.table), id, strings.TrimSpace(*body.Name), strings.TrimSpace(*body.Slug), sortOrder, active)
		}
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}

	item, err := h.loadCatalogItem(r.Context(), def, id)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	h.audit(r, "catalogs.create", typeName, id.String(), nil)
	response.Success(w, r, http.StatusCreated, item)
}

// patchCatalog godoc
//
//	@Summary		Update catalog entry
//	@Tags			admin/catalogs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			type	path		string							true	"Catalog type"
//	@Param			id		path		string							true	"Entry UUID"
//	@Param			body	body		handler.AdminCatalogUpsertRequest	true	"Patch payload"
//	@Success		200		{object}	handler.AdminCatalogItemEnvelope
//	@Router			/admin/catalogs/{type}/{id} [patch]
func (h *AdminHandler) patchCatalog(w http.ResponseWriter, r *http.Request) {
	typeName := chi.URLParam(r, "type")
	def, ok := catalogDefs[typeName]
	if !ok {
		response.Error(w, r, apperror.New(apperror.CodeNotFound, "unknown catalog type"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid id"))
		return
	}

	var body catalogCreateBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	exists := false
	err = h.GlobalDB.QueryRow(r.Context(),
		fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s.%s WHERE id = $1)`, def.schema, def.table), id,
	).Scan(&exists)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	if !exists {
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	switch {
	case def.hasLabel:
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			UPDATE %s.%s SET
				label = COALESCE($2, label),
				min_size = COALESCE($3, min_size),
				max_size = COALESCE($4, max_size),
				sort_order = COALESCE($5, sort_order),
				updated_at = now()
			WHERE id = $1
		`, def.schema, def.table), id, body.Label, body.MinSize, body.MaxSize, body.SortOrder)
	case def.hasDesc:
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			UPDATE %s.%s SET
				description = COALESCE($2, description),
				slug = COALESCE($3, slug),
				emoji = COALESCE($4, emoji),
				sort_order = COALESCE($5, sort_order),
				is_active = COALESCE($6, is_active),
				updated_at = now()
			WHERE id = $1
		`, def.schema, def.table), id, body.Description, body.Slug, body.Emoji, body.SortOrder, body.IsActive)
	case def.hasRegion:
		if body.Region != nil {
			if _, err := region.ParseRegion(*body.Region); err != nil {
				response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRegion))
				return
			}
		}
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			UPDATE %s.%s SET
				name = COALESCE($2, name),
				slug = COALESCE($3, slug),
				region = COALESCE($4, region),
				icon_key = COALESCE($5, icon_key),
				sort_order = COALESCE($6, sort_order),
				is_active = COALESCE($7, is_active),
				updated_at = now()
			WHERE id = $1
		`, def.schema, def.table), id, body.Name, body.Slug, body.Region, body.IconKey, body.SortOrder, body.IsActive)
	case def.hasIconURL:
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			UPDATE %s.%s SET
				name = COALESCE($2, name),
				slug = COALESCE($3, slug),
				icon_url = COALESCE($4, icon_url),
				sort_order = COALESCE($5, sort_order),
				is_active = COALESCE($6, is_active),
				updated_at = now()
			WHERE id = $1
		`, def.schema, def.table), id, body.Name, body.Slug, body.IconURL, body.SortOrder, body.IsActive)
	case def.hasEmoji:
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			UPDATE %s.%s SET
				name = COALESCE($2, name),
				slug = COALESCE($3, slug),
				emoji = COALESCE($4, emoji),
				sort_order = COALESCE($5, sort_order),
				is_active = COALESCE($6, is_active),
				updated_at = now()
			WHERE id = $1
		`, def.schema, def.table), id, body.Name, body.Slug, body.Emoji, body.SortOrder, body.IsActive)
	case def.hasIconKey:
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			UPDATE %s.%s SET
				name = COALESCE($2, name),
				slug = COALESCE($3, slug),
				icon_key = COALESCE($4, icon_key),
				sort_order = COALESCE($5, sort_order),
				is_active = COALESCE($6, is_active),
				updated_at = now()
			WHERE id = $1
		`, def.schema, def.table), id, body.Name, body.Slug, body.IconKey, body.SortOrder, body.IsActive)
	default:
		_, err = h.GlobalDB.Exec(r.Context(), fmt.Sprintf(`
			UPDATE %s.%s SET
				name = COALESCE($2, name),
				slug = COALESCE($3, slug),
				sort_order = COALESCE($4, sort_order),
				is_active = COALESCE($5, is_active),
				updated_at = now()
			WHERE id = $1
		`, def.schema, def.table), id, body.Name, body.Slug, body.SortOrder, body.IsActive)
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}

	item, err := h.loadCatalogItem(r.Context(), def, id)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	h.audit(r, "catalogs.patch", typeName, id.String(), nil)
	response.Success(w, r, http.StatusOK, item)
}

func (h *AdminHandler) loadCatalogItem(ctx context.Context, def catalogDef, id uuid.UUID) (json.RawMessage, error) {
	var raw json.RawMessage
	err := h.GlobalDB.QueryRow(ctx,
		fmt.Sprintf(`SELECT row_to_json(t) FROM %s.%s t WHERE id = $1`, def.schema, def.table), id,
	).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperror.ErrNotFound
	}
	return raw, err
}
