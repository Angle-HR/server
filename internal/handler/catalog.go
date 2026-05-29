package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

const catalogCacheControl = "public, max-age=300"

// CatalogHandler serves onboarding reference data from the global registry.
type CatalogHandler struct {
	GlobalDB globalDB
}

// NewCatalogHandler returns a catalog handler.
func NewCatalogHandler(globalDB globalDB) *CatalogHandler {
	return &CatalogHandler{GlobalDB: globalDB}
}

// RegisterRoutes mounts catalog routes on r.
func (h *CatalogHandler) RegisterRoutes(r chi.Router) {
	r.Get("/industries", h.listIndustries)
	r.Get("/hiring-tools", h.listHiringTools)
	r.Get("/hiring-frustrations", h.listHiringFrustrations)
	r.Get("/roles", h.listRoles)
	r.Get("/team-sizes", h.listTeamSizes)
}

// Industry is an active industry option.
type Industry struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Slug  string    `json:"slug"`
	Emoji *string   `json:"emoji,omitempty"`
}

// HiringTool is an active hiring tool option.
type HiringTool struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Slug    string    `json:"slug"`
	IconURL string    `json:"icon_url"`
}

// HiringFrustration is an active hiring frustration option.
type HiringFrustration struct {
	ID          uuid.UUID `json:"id"`
	Description string    `json:"description"`
	Slug        string    `json:"slug"`
	Emoji       *string   `json:"emoji,omitempty"`
}

// Role is an active role option.
type Role struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Slug  string    `json:"slug"`
	Emoji *string   `json:"emoji,omitempty"`
}

// TeamSize is a team size band option.
type TeamSize struct {
	ID      uuid.UUID `json:"id"`
	Label   string    `json:"label"`
	MinSize *int      `json:"min_size,omitempty"`
	MaxSize *int      `json:"max_size,omitempty"`
}

// listIndustries godoc
//
//	@Summary		List industries
//	@Description	Returns active industry options for onboarding.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	handler.IndustryListEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/industries [get]
func (h *CatalogHandler) listIndustries(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, func(ctx context.Context) (any, error) {
		return h.loadIndustries(ctx)
	})
}

// listHiringTools godoc
//
//	@Summary		List hiring tools
//	@Description	Returns active hiring tool options for onboarding.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	handler.HiringToolListEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring-tools [get]
func (h *CatalogHandler) listHiringTools(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, func(ctx context.Context) (any, error) {
		return h.loadHiringTools(ctx)
	})
}

// listHiringFrustrations godoc
//
//	@Summary		List hiring frustrations
//	@Description	Returns active hiring frustration options for onboarding.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	handler.HiringFrustrationListEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring-frustrations [get]
func (h *CatalogHandler) listHiringFrustrations(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, func(ctx context.Context) (any, error) {
		return h.loadHiringFrustrations(ctx)
	})
}

// listRoles godoc
//
//	@Summary		List roles
//	@Description	Returns active role options for onboarding.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	handler.RoleListEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/roles [get]
func (h *CatalogHandler) listRoles(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, func(ctx context.Context) (any, error) {
		return h.loadRoles(ctx)
	})
}

// listTeamSizes godoc
//
//	@Summary		List team sizes
//	@Description	Returns team size band options for onboarding.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	handler.TeamSizeListEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/team-sizes [get]
func (h *CatalogHandler) listTeamSizes(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, func(ctx context.Context) (any, error) {
		return h.loadTeamSizes(ctx)
	})
}

func (h *CatalogHandler) writeList(w http.ResponseWriter, r *http.Request, load func(context.Context) (any, error)) {
	items, err := load(r.Context())
	if err != nil {
		response.Error(w, r, err)
		return
	}

	w.Header().Set("Cache-Control", catalogCacheControl)
	response.Success(w, r, http.StatusOK, items)
}

func (h *CatalogHandler) loadIndustries(ctx context.Context) ([]Industry, error) {
	sql, args, err := query.ListActiveIndustries()
	if err != nil {
		return nil, fmt.Errorf("build list industries: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Industry, 0, 12)
	for rows.Next() {
		var item Industry
		if err := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.Emoji); err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	return items, rows.Err()
}

func (h *CatalogHandler) loadHiringTools(ctx context.Context) ([]HiringTool, error) {
	sql, args, err := query.ListActiveHiringTools()
	if err != nil {
		return nil, fmt.Errorf("build list hiring tools: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]HiringTool, 0, 16)
	for rows.Next() {
		var item HiringTool
		if err := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.IconURL); err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	return items, rows.Err()
}

func (h *CatalogHandler) loadHiringFrustrations(ctx context.Context) ([]HiringFrustration, error) {
	sql, args, err := query.ListActiveHiringFrustrations()
	if err != nil {
		return nil, fmt.Errorf("build list hiring frustrations: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]HiringFrustration, 0, 8)
	for rows.Next() {
		var item HiringFrustration
		if err := rows.Scan(&item.ID, &item.Description, &item.Slug, &item.Emoji); err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	return items, rows.Err()
}

func (h *CatalogHandler) loadRoles(ctx context.Context) ([]Role, error) {
	sql, args, err := query.ListActiveRoles()
	if err != nil {
		return nil, fmt.Errorf("build list roles: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Role, 0, 8)
	for rows.Next() {
		var item Role
		if err := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.Emoji); err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	return items, rows.Err()
}

func (h *CatalogHandler) loadTeamSizes(ctx context.Context) ([]TeamSize, error) {
	sql, args, err := query.ListTeamSizes()
	if err != nil {
		return nil, fmt.Errorf("build list team sizes: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]TeamSize, 0, 4)
	for rows.Next() {
		var item TeamSize
		if err := rows.Scan(&item.ID, &item.Label, &item.MinSize, &item.MaxSize); err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	return items, rows.Err()
}
