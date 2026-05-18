package catalog

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/pkg/response"
)

const cacheControl = "public, max-age=300"

type catalogRepository interface {
	ReferenceVersion(ctx context.Context, table string) (string, error)
	ListIndustries(ctx context.Context) ([]Industry, error)
	ListHiringTools(ctx context.Context) ([]HiringTool, error)
	ListHiringFrustrations(ctx context.Context) ([]HiringFrustration, error)
	ListRoles(ctx context.Context) ([]Role, error)
	ListTeamSizes(ctx context.Context) ([]TeamSize, error)
}

// Handler exposes onboarding reference endpoints.
type Handler struct {
	repo catalogRepository
}

// NewHandler returns a catalog HTTP handler.
func NewHandler(repo catalogRepository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes mounts catalog routes on r.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/industries", h.listIndustries)
	r.Get("/hiring-tools", h.listHiringTools)
	r.Get("/hiring-frustrations", h.listHiringFrustrations)
	r.Get("/roles", h.listRoles)
	r.Get("/team-sizes", h.listTeamSizes)
}

// listIndustries godoc
//
//	@Summary		List industries
//	@Description	Returns active industry options for onboarding step 1.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	catalog.IndustryListEnvelope
//	@Success		304	"Not Modified"
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/industries [get]
func (h *Handler) listIndustries(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, "industries", func(ctx context.Context) (any, error) {
		return h.repo.ListIndustries(ctx)
	})
}

// listHiringTools godoc
//
//	@Summary		List hiring tools
//	@Description	Returns active hiring tool options for onboarding step 2.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	catalog.HiringToolListEnvelope
//	@Success		304	"Not Modified"
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring-tools [get]
func (h *Handler) listHiringTools(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, "hiring_tools", func(ctx context.Context) (any, error) {
		return h.repo.ListHiringTools(ctx)
	})
}

// listHiringFrustrations godoc
//
//	@Summary		List hiring frustrations
//	@Description	Returns active hiring frustration options for onboarding step 3.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	catalog.HiringFrustrationListEnvelope
//	@Success		304	"Not Modified"
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring-frustrations [get]
func (h *Handler) listHiringFrustrations(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, "hiring_frustrations", func(ctx context.Context) (any, error) {
		return h.repo.ListHiringFrustrations(ctx)
	})
}

// listRoles godoc
//
//	@Summary		List roles
//	@Description	Returns active role options for onboarding step 4.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	catalog.RoleListEnvelope
//	@Success		304	"Not Modified"
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/roles [get]
func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, "roles", func(ctx context.Context) (any, error) {
		return h.repo.ListRoles(ctx)
	})
}

// listTeamSizes godoc
//
//	@Summary		List team sizes
//	@Description	Returns active team size options for onboarding step 4.
//	@Tags			catalog
//	@Produce		json
//	@Success		200	{object}	catalog.TeamSizeListEnvelope
//	@Success		304	"Not Modified"
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/team-sizes [get]
func (h *Handler) listTeamSizes(w http.ResponseWriter, r *http.Request) {
	h.writeList(w, r, "team_sizes", func(ctx context.Context) (any, error) {
		return h.repo.ListTeamSizes(ctx)
	})
}

type listLoader func(context.Context) (any, error)

func (h *Handler) writeList(
	w http.ResponseWriter,
	r *http.Request,
	table string,
	load listLoader,
) {
	version, err := h.repo.ReferenceVersion(r.Context(), table)
	if err != nil {
		response.Error(w, r, fmt.Errorf("load reference version: %w", err))
		return
	}

	etag := `W/"` + version + `"`
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("ETag", etag)

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	data, err := load(r.Context())
	if err != nil {
		response.Error(w, r, fmt.Errorf("load reference data: %w", err))
		return
	}

	response.Success(w, r, http.StatusOK, data)
}
