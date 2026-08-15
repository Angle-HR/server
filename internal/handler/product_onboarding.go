package handler

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
)

var _ = apidoc.ErrorEnvelope{}

// RegisterRoutes mounts public product onboarding routes on r.
func (h *ProductOnboardingHandler) RegisterRoutes(r chi.Router) {
	r.Get("/onboarding/business-types", h.listBusinessTypes)
	r.Get("/onboarding/industries", h.listOnboardingIndustries)
	r.Get("/onboarding/company-roles", h.listCompanyRoles)
}

// RegisterProtectedRoutes mounts authenticated product onboarding routes on r.
func (h *ProductOnboardingHandler) RegisterProtectedRoutes(r chi.Router) {
	r.Get("/onboarding/status", h.status)
	r.Put("/onboarding/profile", h.putProfile)
	r.Put("/onboarding/address", h.putAddress)
	r.Post("/onboarding/address/verify", h.verifyAddress)
	r.Put("/onboarding/business", h.putBusiness)
	r.Post("/onboarding/complete", h.complete)
}

// listBusinessTypes godoc
//
//	@Summary		List business types
//	@Description	Returns business type options for the compliance step.
//	@Tags			onboarding/reference
//	@Produce		json
//	@Success		200	{object}	handler.BusinessTypeListEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/business-types [get]
func (h *ProductOnboardingHandler) listBusinessTypes(w http.ResponseWriter, r *http.Request) {
	h.writeCatalogList(w, r, func(ctx context.Context) (any, error) {
		return h.loadBusinessTypes(ctx)
	})
}

// listOnboardingIndustries godoc
//
//	@Summary		List onboarding industries
//	@Description	Returns product onboarding industry options.
//	@Tags			onboarding/reference
//	@Produce		json
//	@Success		200	{object}	handler.OnboardingIndustryListEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/industries [get]
func (h *ProductOnboardingHandler) listOnboardingIndustries(w http.ResponseWriter, r *http.Request) {
	h.writeCatalogList(w, r, func(ctx context.Context) (any, error) {
		return h.loadOnboardingIndustries(ctx)
	})
}

// listCompanyRoles godoc
//
//	@Summary		List company roles
//	@Description	Returns company role options for business profile step.
//	@Tags			onboarding/reference
//	@Produce		json
//	@Success		200	{object}	handler.CompanyRoleListEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/company-roles [get]
func (h *ProductOnboardingHandler) listCompanyRoles(w http.ResponseWriter, r *http.Request) {
	h.writeCatalogList(w, r, func(ctx context.Context) (any, error) {
		return h.loadCompanyRoles(ctx)
	})
}
