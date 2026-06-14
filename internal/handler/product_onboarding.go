package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
)

var _ = apidoc.ErrorEnvelope{}

// ProductOnboardingHandler handles product onboarding endpoints (design-only stubs).
type ProductOnboardingHandler struct{}

// NewProductOnboardingHandler returns a product onboarding handler.
func NewProductOnboardingHandler() *ProductOnboardingHandler {
	return &ProductOnboardingHandler{}
}

// RegisterRoutes mounts product onboarding routes on r.
func (h *ProductOnboardingHandler) RegisterRoutes(r chi.Router) {
	r.Get("/onboarding/status", h.status)
	r.Put("/onboarding/profile", h.putProfile)
	r.Put("/onboarding/address", h.putAddress)
	r.Post("/onboarding/address/verify", h.verifyAddress)
	r.Put("/onboarding/business", h.putBusiness)
	r.Post("/onboarding/complete", h.complete)
	r.Get("/onboarding/business-types", h.listBusinessTypes)
	r.Get("/onboarding/industries", h.listOnboardingIndustries)
	r.Get("/onboarding/company-roles", h.listCompanyRoles)
}

// status godoc
//
//	@Summary		Get onboarding status
//	@Description	Returns current step, completed steps, and saved draft fields. Not yet implemented.
//	@Tags			onboarding/session
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	handler.ProductOnboardingStatusEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		501	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/status [get]
func (h *ProductOnboardingHandler) status(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// putProfile godoc
//
//	@Summary		Upsert profile
//	@Description	Saves account type and profile fields for individual or business accounts. Not yet implemented.
//	@Tags			onboarding/profile
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.ProductProfileRequest	true	"Profile payload"
//	@Success		200		{object}	handler.ProductProfileEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/profile [put]
func (h *ProductOnboardingHandler) putProfile(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// putAddress godoc
//
//	@Summary		Upsert address
//	@Description	Saves workspace address from search or manual entry. Not yet implemented.
//	@Tags			onboarding/address
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.ProductAddressRequest	true	"Address payload"
//	@Success		200		{object}	handler.ProductAddressEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/address [put]
func (h *ProductOnboardingHandler) putAddress(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// verifyAddress godoc
//
//	@Summary		Verify address
//	@Description	Reserved for third-party address verification. Returns 501 until a provider is integrated.
//	@Tags			onboarding/address
//	@Produce		json
//	@Security		BearerAuth
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		501	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/address/verify [post]
func (h *ProductOnboardingHandler) verifyAddress(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// putBusiness godoc
//
//	@Summary		Upsert business compliance
//	@Description	Saves business type, industry, and employee count. Business accounts only. Not yet implemented.
//	@Tags			onboarding/business
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.ProductBusinessRequest	true	"Business payload"
//	@Success		200		{object}	handler.ProductBusinessEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/business [put]
func (h *ProductOnboardingHandler) putBusiness(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// complete godoc
//
//	@Summary		Complete onboarding
//	@Description	Finalizes onboarding after all mandatory steps are done. Not yet implemented.
//	@Tags			onboarding/session
//	@Produce		json
//	@Security		BearerAuth
//	@Success		201	{object}	handler.ProductOnboardingCompleteEnvelope
//	@Failure		400	{object}	apidoc.ErrorEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Failure		501	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/complete [post]
func (h *ProductOnboardingHandler) complete(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// listBusinessTypes godoc
//
//	@Summary		List business types
//	@Description	Returns business type options for the compliance step. Not yet implemented.
//	@Tags			onboarding/reference
//	@Produce		json
//	@Success		200	{object}	handler.BusinessTypeListEnvelope
//	@Failure		501	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/business-types [get]
func (h *ProductOnboardingHandler) listBusinessTypes(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// listOnboardingIndustries godoc
//
//	@Summary		List onboarding industries
//	@Description	Returns product onboarding industry options. Not yet implemented.
//	@Tags			onboarding/reference
//	@Produce		json
//	@Success		200	{object}	handler.OnboardingIndustryListEnvelope
//	@Failure		501	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/industries [get]
func (h *ProductOnboardingHandler) listOnboardingIndustries(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// listCompanyRoles godoc
//
//	@Summary		List company roles
//	@Description	Returns company role options for business profile step. Not yet implemented.
//	@Tags			onboarding/reference
//	@Produce		json
//	@Success		200	{object}	handler.CompanyRoleListEnvelope
//	@Failure		501	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/company-roles [get]
func (h *ProductOnboardingHandler) listCompanyRoles(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
