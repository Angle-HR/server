package apphttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// Envelopes for the API documentation.
type (
	// OffersEnvelope is a list of offers.
	OffersEnvelope struct {
		Data []applications.Offer `json:"data"`
	}
	// OfferEnvelope is one offer.
	OfferEnvelope struct {
		Data applications.Offer `json:"data"`
	}
	// OfferTemplatesEnvelope is a list of offer templates.
	OfferTemplatesEnvelope struct {
		Data []applications.OfferTemplate `json:"data"`
	}
	// OfferTemplateEnvelope is one offer template.
	OfferTemplateEnvelope struct {
		Data applications.OfferTemplate `json:"data"`
	}
	// OfferEventsEnvelope is the audit trail of an offer.
	OfferEventsEnvelope struct {
		Data []applications.OfferEvent `json:"data"`
	}
	// PublicOfferEnvelope is an offer as the candidate sees it.
	PublicOfferEnvelope struct {
		Data applications.PublicOffer `json:"data"`
	}
)

func (h *Handler) registerOfferJobRoutes(r chi.Router) {
	r.Get("/applications/{app_id}/offers", h.listOffers)
	r.Post("/applications/{app_id}/offers", h.createOffer)
	r.Patch("/applications/{app_id}/offers/{offer_id}", h.updateOffer)
	r.Post("/applications/{app_id}/offers/{offer_id}/send", h.sendOffer)
	r.Post("/applications/{app_id}/offers/{offer_id}/withdraw", h.withdrawOffer)
	r.Get("/applications/{app_id}/offers/{offer_id}/audit", h.offerAudit)
	r.Get("/applications/{app_id}/offers/{offer_id}/document", h.offerDocument)
}

func (h *Handler) registerOfferCompanyRoutes(r chi.Router) {
	r.Get("/offer-templates", h.listOfferTemplates)
	r.Post("/offer-templates", h.createOfferTemplate)
	r.Put("/offer-templates/{template_id}", h.updateOfferTemplate)
	r.Delete("/offer-templates/{template_id}", h.deleteOfferTemplate)
}

// RegisterOfferPublicRoutes mounts the routes a candidate reaches from the link in their email. r is the router of
// /public/offers. They need no sign-in: the token in the path is the credential.
func (h *Handler) RegisterOfferPublicRoutes(r chi.Router) {
	r.Get("/{token}", h.publicOffer)
	r.Post("/{token}/sign", h.signOffer)
	r.Post("/{token}/decline", h.declineOffer)
	r.Get("/{token}/document", h.publicOfferDocument)
}

func offerID(r *http.Request) string { return chi.URLParam(r, "offer_id") }

// OfferRequest makes an offer.
type OfferRequest struct {
	// TemplateID fills the letter from an offer template when body is left out.
	TemplateID string `json:"template_id,omitempty"`
	// Title defaults to "Offer of employment: {{job_title}}".
	Title string `json:"title,omitempty"`
	// Body is the letter. Placeholders: {{candidate_name}}, {{job_title}}, {{company}}, {{sender_name}}, {{start_date}}, {{compensation}}.
	Body         string `json:"body,omitempty"`
	Compensation string `json:"compensation,omitempty"`
	// StartDate is YYYY-MM-DD.
	StartDate string `json:"start_date,omitempty"`
	// Send sends the offer to the candidate straight away.
	Send bool `json:"send,omitempty"`
	// ExpiresInDays is how long the link works: 7 by default, 30 at most.
	ExpiresInDays int `json:"expires_in_days,omitempty"`
}

// OfferPatchRequest changes a draft; a field left out stays.
type OfferPatchRequest struct {
	Title        *string `json:"title,omitempty"`
	Body         *string `json:"body,omitempty"`
	Compensation *string `json:"compensation,omitempty"`
	StartDate    *string `json:"start_date,omitempty"`
}

// OfferSendRequest sends an offer.
type OfferSendRequest struct {
	ExpiresInDays int `json:"expires_in_days,omitempty"`
}

// OfferTemplateRequest saves an offer template.
type OfferTemplateRequest struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

// SignRequest is the candidate's signature.
type SignRequest struct {
	// FullName is typed by the candidate: it is their signature.
	FullName string `json:"full_name"`
	// Consent must be true: the candidate agrees to sign electronically (consent_text of the offer).
	Consent bool `json:"consent"`
}

// DeclineRequest turns an offer down.
type DeclineRequest struct {
	Reason string `json:"reason,omitempty"`
}

// listOffers godoc
//
//	@Summary		An application's offers
//	@Description	Newest first. An offer nobody answered by its expiry shows as expired. Seen by people who can see the job.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string	true	"Job id"
//	@Param			app_id	path		string	true	"Application id"
//	@Success		200		{object}	apphttp.OffersEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/offers [get]
func (h *Handler) listOffers(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.Offers(r.Context(), c, jobID(r), appID(r))
	finish(w, r, http.StatusOK, out, err)
}

// createOffer godoc
//
//	@Summary		Make an offer
//	@Description	Saves a draft, or sends it with `send`. The candidate must be in review. One live offer per application. The letter is filled in from placeholders when it is saved and is fixed once sent. The candidate gets an email with a single-use link that works until the offer expires; signing it hires the candidate.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string					true	"Job id"
//	@Param			app_id	path		string					true	"Application id"
//	@Param			body	body		apphttp.OfferRequest	true	"Offer"
//	@Success		201		{object}	apphttp.OfferEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/offers [post]
func (h *Handler) createOffer(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req OfferRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.CreateOffer(r.Context(), c, jobID(r), appID(r), applications.OfferInput{TemplateID: req.TemplateID, Title: req.Title,
		Body: req.Body, Compensation: req.Compensation, StartDate: req.StartDate, Send: req.Send, ExpiresInDays: req.ExpiresInDays})
	finish(w, r, http.StatusCreated, out, err)
}

// updateOffer godoc
//
//	@Summary		Change a draft offer
//	@Description	Only a draft: a sent offer is fixed, so withdraw it and make a new one.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id			path		string						true	"Job id"
//	@Param			app_id		path		string						true	"Application id"
//	@Param			offer_id	path		string						true	"Offer id"
//	@Param			body		body		apphttp.OfferPatchRequest	true	"Changes"
//	@Success		200			{object}	apphttp.OfferEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/offers/{offer_id} [patch]
func (h *Handler) updateOffer(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req OfferPatchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.UpdateOffer(r.Context(), c, jobID(r), appID(r), offerID(r), applications.OfferPatch{Title: req.Title, Body: req.Body,
		Compensation: req.Compensation, StartDate: req.StartDate})
	finish(w, r, http.StatusOK, out, err)
}

// sendOffer godoc
//
//	@Summary		Send an offer
//	@Description	Sends a draft, or sends an unanswered offer again: the new email carries a new link and the earlier link stops working.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id			path		string						true	"Job id"
//	@Param			app_id		path		string						true	"Application id"
//	@Param			offer_id	path		string						true	"Offer id"
//	@Param			body		body		apphttp.OfferSendRequest	false	"Options"
//	@Success		200			{object}	apphttp.OfferEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/offers/{offer_id}/send [post]
func (h *Handler) sendOffer(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req OfferSendRequest
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.SendOffer(r.Context(), c, jobID(r), appID(r), offerID(r), req.ExpiresInDays)
	finish(w, r, http.StatusOK, out, err)
}

// withdrawOffer godoc
//
//	@Summary		Withdraw an offer
//	@Description	Takes back an offer that is not signed, declined or expired. Its link stops working at once.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id			path	string	true	"Job id"
//	@Param			app_id		path	string	true	"Application id"
//	@Param			offer_id	path	string	true	"Offer id"
//	@Success		204
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/offers/{offer_id}/withdraw [post]
func (h *Handler) withdrawOffer(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	if err := svc.WithdrawOffer(r.Context(), c, jobID(r), appID(r), offerID(r)); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// offerAudit godoc
//
//	@Summary		An offer's audit trail
//	@Description	Created, sent, viewed, signed, declined, expired and withdrawn, with the time, IP address and browser of whoever acted. Oldest first.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id			path		string	true	"Job id"
//	@Param			app_id		path		string	true	"Application id"
//	@Param			offer_id	path		string	true	"Offer id"
//	@Success		200			{object}	apphttp.OfferEventsEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/offers/{offer_id}/audit [get]
func (h *Handler) offerAudit(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.OfferAudit(r.Context(), c, jobID(r), appID(r), offerID(r))
	finish(w, r, http.StatusOK, out, err)
}

// offerDocument godoc
//
//	@Summary		The signed offer
//	@Description	A short-lived link to the signed PDF: the letter, the signature and the audit trail. Only for a signed offer.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id			path		string	true	"Job id"
//	@Param			app_id		path		string	true	"Application id"
//	@Param			offer_id	path		string	true	"Offer id"
//	@Success		200			{object}	apphttp.FileLinkEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/offers/{offer_id}/document [get]
func (h *Handler) offerDocument(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.OfferDocument(r.Context(), c, jobID(r), appID(r), offerID(r))
	finish(w, r, http.StatusOK, out, err)
}

// listOfferTemplates godoc
//
//	@Summary		Offer templates
//	@Description	The company's reusable offer letters.
//	@Tags			hiring
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	apphttp.OfferTemplatesEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/offer-templates [get]
func (h *Handler) listOfferTemplates(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.OfferTemplates(r.Context(), c)
	finish(w, r, http.StatusOK, out, err)
}

// createOfferTemplate godoc
//
//	@Summary		Add an offer template
//	@Description	Needs job.manage_any. Placeholders: {{candidate_name}}, {{job_title}}, {{company}}, {{sender_name}}, {{start_date}}, {{compensation}}. At most 30.
//	@Tags			hiring
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		apphttp.OfferTemplateRequest	true	"Template"
//	@Success		201		{object}	apphttp.OfferTemplateEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/offer-templates [post]
func (h *Handler) createOfferTemplate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req OfferTemplateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.CreateOfferTemplate(r.Context(), c, req.Name, req.Body)
	finish(w, r, http.StatusCreated, out, err)
}

// updateOfferTemplate godoc
//
//	@Summary		Change an offer template
//	@Description	Needs job.manage_any. Offers already made keep their text.
//	@Tags			hiring
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			template_id	path		string							true	"Template id"
//	@Param			body		body		apphttp.OfferTemplateRequest	true	"Template"
//	@Success		200			{object}	apphttp.OfferTemplateEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/offer-templates/{template_id} [put]
func (h *Handler) updateOfferTemplate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req OfferTemplateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.UpdateOfferTemplate(r.Context(), c, chi.URLParam(r, "template_id"), req.Name, req.Body)
	finish(w, r, http.StatusOK, out, err)
}

// deleteOfferTemplate godoc
//
//	@Summary		Delete an offer template
//	@Description	Needs job.manage_any. Offers already made keep their text.
//	@Tags			hiring
//	@Security		SessionCookie
//	@Param			template_id	path	string	true	"Template id"
//	@Success		204
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/offer-templates/{template_id} [delete]
func (h *Handler) deleteOfferTemplate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteOfferTemplate(r.Context(), c, chi.URLParam(r, "template_id")); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- the candidate's side ----

// offerScope resolves the token of a public offer link into the regional service, the company and the hash of the
// secret. Anything wrong with the token answers 404, so a link cannot be probed.
func (h *Handler) offerScope(w http.ResponseWriter, r *http.Request) (svc *applications.Service, tenantID, hash string, ok bool) {
	noStore(w)
	w.Header().Set("Referrer-Policy", "no-referrer")
	reg, tenant, hash, valid := applications.ParseOfferToken(chi.URLParam(r, "token"))
	if !valid || h.Applications == nil {
		response.Error(w, r, apperror.New(apperror.CodeNotFound, "this link is not valid"))
		return nil, "", "", false
	}
	svc, err := h.Applications(region.Region(reg))
	if err != nil || svc == nil {
		response.Error(w, r, apperror.New(apperror.CodeNotFound, "this link is not valid"))
		return nil, "", "", false
	}
	return svc, tenant, hash, true
}

func requestMeta(r *http.Request) applications.Meta {
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	return applications.Meta{IP: clientIP(r), UserAgent: ua}
}

// publicOffer godoc
//
//	@Summary		Read an offer
//	@Description	The offer behind the link in the candidate's email. No sign-in: the token in the link is the credential. Opening it is recorded as a view. `can_respond` says whether it can still be signed or declined.
//	@Tags			offers
//	@Produce		json
//	@Param			token	path		string	true	"Token from the link"
//	@Success		200		{object}	apphttp.PublicOfferEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/public/offers/{token} [get]
func (h *Handler) publicOffer(w http.ResponseWriter, r *http.Request) {
	svc, tenant, hash, ok := h.offerScope(w, r)
	if !ok {
		return
	}
	out, err := svc.ViewOffer(r.Context(), tenant, hash, requestMeta(r))
	finish(w, r, http.StatusOK, out, err)
}

// signOffer godoc
//
//	@Summary		Sign an offer
//	@Description	The candidate types their full name and agrees to sign electronically. The time is the server's. The offer's terms are fixed; the signature, the hash of the letter, the consent, the IP address and the browser go into the audit trail, a signed PDF is made, and the candidate is hired. The link works once.
//	@Tags			offers
//	@Accept			json
//	@Produce		json
//	@Param			token	path		string				true	"Token from the link"
//	@Param			body	body		apphttp.SignRequest	true	"Signature"
//	@Success		200		{object}	apphttp.PublicOfferEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/public/offers/{token}/sign [post]
func (h *Handler) signOffer(w http.ResponseWriter, r *http.Request) {
	svc, tenant, hash, ok := h.offerScope(w, r)
	if !ok {
		return
	}
	var req SignRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.SignOffer(r.Context(), tenant, hash, req.FullName, req.Consent, requestMeta(r))
	finish(w, r, http.StatusOK, out, err)
}

// declineOffer godoc
//
//	@Summary		Decline an offer
//	@Description	The candidate says no, with an optional reason the company can read. The link works once.
//	@Tags			offers
//	@Accept			json
//	@Produce		json
//	@Param			token	path	string					true	"Token from the link"
//	@Param			body	body	apphttp.DeclineRequest	false	"Reason"
//	@Success		204
//	@Failure		400	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Router			/public/offers/{token}/decline [post]
func (h *Handler) declineOffer(w http.ResponseWriter, r *http.Request) {
	svc, tenant, hash, ok := h.offerScope(w, r)
	if !ok {
		return
	}
	var req DeclineRequest
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	if err := svc.DeclineOffer(r.Context(), tenant, hash, req.Reason, requestMeta(r)); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publicOfferDocument godoc
//
//	@Summary		The signed offer
//	@Description	A short-lived link to the signed PDF, for the candidate who signed. The link of the email stops working once signed, so this is for the page shown right after signing.
//	@Tags			offers
//	@Produce		json
//	@Param			token	path		string	true	"Token from the link"
//	@Success		200		{object}	apphttp.FileLinkEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/public/offers/{token}/document [get]
func (h *Handler) publicOfferDocument(w http.ResponseWriter, r *http.Request) {
	svc, tenant, hash, ok := h.offerScope(w, r)
	if !ok {
		return
	}
	out, err := svc.PublicOfferDocument(r.Context(), tenant, hash)
	finish(w, r, http.StatusOK, out, err)
}
