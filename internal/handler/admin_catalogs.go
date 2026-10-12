package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
	"countries": {
		schema:     "waitlist",
		table:      "countries",
		hasActive:  true,
		hasIconKey: true,
		hasRegion:  true,
	},
	"industries":   {schema: "waitlist", table: "industries", hasActive: true, hasEmoji: true},
	"hiring_tools": {schema: "waitlist", table: "hiring_tools", hasActive: true, hasIconURL: true},
	"hiring_frustrations": {
		schema:    "waitlist",
		table:     "hiring_frustrations",
		hasActive: true,
		hasEmoji:  true,
		hasDesc:   true,
	},
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
	err := h.insertCatalogRow(r.Context(), def, &body, id)
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
	if decodeJSONErr := decodeJSON(r, &body); decodeJSONErr != nil {
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

	err = h.updateCatalogRow(r.Context(), def, &body, id)
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

// catalogColumn is one column and its value in a generated catalog INSERT or UPDATE.
type catalogColumn struct {
	name  string
	value any
}

func trimmed(p *string) string { return strings.TrimSpace(*p) }

func catalogValidation(msg string) error {
	return apperror.New(apperror.CodeValidationError, msg)
}

// insertCatalogColumns validates body for the catalog shape and returns the columns to insert.
func insertCatalogColumns(def catalogDef, body *catalogCreateBody) ([]catalogColumn, error) {
	sortOrder := 0
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	}
	active := true
	if body.IsActive != nil {
		active = *body.IsActive
	}
	tail := []catalogColumn{{"sort_order", sortOrder}, {"is_active", active}}

	switch {
	case def.hasLabel:
		return insertLabelColumns(body, tail[0])
	case def.hasDesc:
		return insertDescriptionColumns(body, tail)
	case def.hasRegion:
		return insertRegionColumns(body, tail)
	case def.hasIconURL:
		return insertIconURLColumns(body, tail)
	default:
		return insertNamedColumns(def, body, tail)
	}
}

func insertLabelColumns(body *catalogCreateBody, order catalogColumn) ([]catalogColumn, error) {
	if body.Label == nil || strings.TrimSpace(*body.Label) == "" {
		return nil, catalogValidation("label is required")
	}
	return []catalogColumn{
		{"label", trimmed(body.Label)}, {"min_size", body.MinSize}, {"max_size", body.MaxSize}, order,
	}, nil
}

func insertDescriptionColumns(body *catalogCreateBody, tail []catalogColumn) ([]catalogColumn, error) {
	if body.Description == nil || body.Slug == nil {
		return nil, catalogValidation("description and slug are required")
	}
	return append([]catalogColumn{
		{"description", trimmed(body.Description)}, {"slug", trimmed(body.Slug)}, {"emoji", body.Emoji},
	}, tail...), nil
}

func insertRegionColumns(body *catalogCreateBody, tail []catalogColumn) ([]catalogColumn, error) {
	if body.Name == nil || body.Slug == nil || body.Region == nil {
		return nil, catalogValidation("name, slug, and region are required")
	}
	if _, err := region.ParseRegion(*body.Region); err != nil {
		return nil, catalogValidation(apperror.MsgInvalidRegion)
	}
	return append([]catalogColumn{
		{"name", trimmed(body.Name)}, {"slug", trimmed(body.Slug)},
		{"region", *body.Region}, {"icon_key", body.IconKey},
	}, tail...), nil
}

func insertIconURLColumns(body *catalogCreateBody, tail []catalogColumn) ([]catalogColumn, error) {
	if body.Name == nil || body.Slug == nil || body.IconURL == nil {
		return nil, catalogValidation("name, slug, and icon_url are required")
	}
	return append([]catalogColumn{
		{"name", trimmed(body.Name)}, {"slug", trimmed(body.Slug)}, {"icon_url", trimmed(body.IconURL)},
	}, tail...), nil
}

func insertNamedColumns(def catalogDef, body *catalogCreateBody, tail []catalogColumn) ([]catalogColumn, error) {
	if body.Name == nil || body.Slug == nil {
		return nil, catalogValidation("name and slug are required")
	}
	cols := []catalogColumn{{"name", trimmed(body.Name)}, {"slug", trimmed(body.Slug)}}
	switch {
	case def.hasEmoji:
		cols = append(cols, catalogColumn{"emoji", body.Emoji})
	case def.hasIconKey:
		cols = append(cols, catalogColumn{"icon_key", body.IconKey})
	}
	return append(cols, tail...), nil
}

// updateCatalogColumns returns the columns a PATCH may change for the catalog shape.
func updateCatalogColumns(def catalogDef, body *catalogCreateBody) ([]catalogColumn, error) {
	order := catalogColumn{"sort_order", body.SortOrder}
	isActive := catalogColumn{"is_active", body.IsActive}
	nameSlug := []catalogColumn{{"name", body.Name}, {"slug", body.Slug}}

	switch {
	case def.hasLabel:
		return []catalogColumn{
			{"label", body.Label}, {"min_size", body.MinSize}, {"max_size", body.MaxSize}, order,
		}, nil
	case def.hasDesc:
		return []catalogColumn{
			{"description", body.Description}, {"slug", body.Slug}, {"emoji", body.Emoji}, order, isActive,
		}, nil
	case def.hasRegion:
		if body.Region != nil {
			if _, err := region.ParseRegion(*body.Region); err != nil {
				return nil, catalogValidation(apperror.MsgInvalidRegion)
			}
		}
		return append(nameSlug,
			catalogColumn{"region", body.Region}, catalogColumn{"icon_key", body.IconKey}, order, isActive), nil
	case def.hasIconURL:
		return append(nameSlug, catalogColumn{"icon_url", body.IconURL}, order, isActive), nil
	case def.hasEmoji:
		return append(nameSlug, catalogColumn{"emoji", body.Emoji}, order, isActive), nil
	case def.hasIconKey:
		return append(nameSlug, catalogColumn{"icon_key", body.IconKey}, order, isActive), nil
	default:
		return append(nameSlug, order, isActive), nil
	}
}

// insertCatalogRow validates body and inserts a new catalog entry.
func (h *AdminHandler) insertCatalogRow(
	ctx context.Context,
	def catalogDef,
	body *catalogCreateBody,
	id uuid.UUID,
) error {
	cols, err := insertCatalogColumns(def, body)
	if err != nil {
		return err
	}
	names := []string{"id"}
	placeholders := []string{"$1"}
	args := []any{id}
	for i, c := range cols {
		names = append(names, c.name)
		placeholders = append(placeholders, "$"+strconv.Itoa(i+2))
		args = append(args, c.value)
	}
	sql := fmt.Sprintf("INSERT INTO %s.%s (%s) VALUES (%s)",
		def.schema, def.table, strings.Join(names, ", "), strings.Join(placeholders, ", "))
	_, err = h.GlobalDB.Exec(ctx, sql, args...)
	return err
}

// updateCatalogRow applies the non-nil fields of body to an existing catalog entry.
func (h *AdminHandler) updateCatalogRow(
	ctx context.Context,
	def catalogDef,
	body *catalogCreateBody,
	id uuid.UUID,
) error {
	cols, err := updateCatalogColumns(def, body)
	if err != nil {
		return err
	}
	sets := make([]string, 0, len(cols)+1)
	args := []any{id}
	for i, c := range cols {
		n := strconv.Itoa(i + 2)
		sets = append(sets, c.name+" = COALESCE($"+n+", "+c.name+")")
		args = append(args, c.value)
	}
	sets = append(sets, "updated_at = now()")
	sql := fmt.Sprintf("UPDATE %s.%s SET %s WHERE id = $1", def.schema, def.table, strings.Join(sets, ", "))
	_, err = h.GlobalDB.Exec(ctx, sql, args...)
	return err
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
