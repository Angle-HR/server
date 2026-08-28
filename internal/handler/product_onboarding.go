package handler

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// RegisterRoutes mounts public product onboarding routes on r.
func (h *ProductOnboardingHandler) RegisterRoutes(r chi.Router) {
	r.Get("/onboarding/business-types", h.listBusinessTypes)
	r.Get("/onboarding/industries", h.listOnboardingIndustries)
	r.Get("/onboarding/company-roles", h.listCompanyRoles)
	r.Get("/onboarding/identification-requirements", h.identificationRequirements)
}

// RegisterProtectedRoutes mounts authenticated product onboarding routes on r.
func (h *ProductOnboardingHandler) RegisterProtectedRoutes(r chi.Router) {
	r.Get("/onboarding/status", h.status)
	r.Put("/onboarding/profile", h.putProfile)
	r.Put("/onboarding/address", h.putAddress)
	r.Post("/onboarding/address/search", h.searchAddress)
	r.Post("/onboarding/address/verify", h.verifyAddress)
	r.Put("/onboarding/compliance", h.putCompliance)
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

// identificationRequirements godoc
//
//	@Summary		Get business identification requirements
//	@Description	Returns country-specific business identification field labels, formats, and validation patterns.
//	@Tags			onboarding/reference
//	@Produce		json
//	@Param			country_id	query		string	true	"Country UUID"
//	@Success		200			{object}	handler.IdentificationRequirementsEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		500			{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/identification-requirements [get]
func (h *ProductOnboardingHandler) identificationRequirements(w http.ResponseWriter, r *http.Request) {
	countryIDRaw := r.URL.Query().Get("country_id")
	if countryIDRaw == "" {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "country_id is required"))
		return
	}
	countryID, err := uuid.Parse(countryIDRaw)
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidCountryID))
		return
	}

	ctx := r.Context()
	if err := h.ensureActiveCountry(ctx, countryID); err != nil {
		response.Error(w, r, err)
		return
	}

	slug, err := h.countrySlugByID(ctx, countryID)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	req, ok := onboarding.IdentificationRequirementsForCountry(slug)
	if !ok {
		response.Error(w, r, apperror.New(apperror.CodeNotFound, "identification requirements not configured for country"))
		return
	}

	fields := make([]IdentificationRequirementField, 0, len(req.Fields))
	for _, field := range req.Fields {
		fields = append(fields, IdentificationRequirementField{
			Key:         field.Key,
			Label:       field.Label,
			FormatHint:  field.FormatHint,
			Placeholder: field.Placeholder,
			Pattern:     field.Pattern,
			Required:    field.Required,
		})
	}

	response.Success(w, r, http.StatusOK, IdentificationRequirementsData{
		CountryID:   countryID.String(),
		CountrySlug: slug,
		Fields:      fields,
	})
}
