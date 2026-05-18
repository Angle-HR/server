package session

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// Handler exposes onboarding session endpoints.
type Handler struct {
	service *Service
}

// NewHandler returns a session HTTP handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes mounts session routes on r.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/waitlist/session", h.createSession)
	r.Get("/waitlist/session/{token}", h.getSession)
	r.Patch("/waitlist/session/{token}/step/1", h.saveStep1)
	r.Patch("/waitlist/session/{token}/step/2", h.saveStep2)
	r.Patch("/waitlist/session/{token}/step/3", h.saveStep3)
	r.Patch("/waitlist/session/{token}/step/4", h.saveStep4)
	r.Post("/waitlist/session/{token}/submit", h.submit)
}

// createSession godoc
//
//	@Summary		Create onboarding session
//	@Description	Starts a new waitlist onboarding session and returns a session token.
//	@Tags			session
//	@Produce		json
//	@Success		201	{object}	session.CreateSessionEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/waitlist/session [post]
func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	token, currentStep, expiresAt, err := h.service.CreateSession(r.Context())
	if err != nil {
		response.Error(w, r, fmt.Errorf("create session: %w", err))
		return
	}

	response.Success(w, r, http.StatusCreated, map[string]any{
		"session_token": token,
		"current_step":  currentStep,
		"expires_at":    expiresAt,
	})
}

// getSession godoc
//
//	@Summary		Get onboarding session
//	@Description	Returns the current step and saved partial progress for a session token.
//	@Tags			session
//	@Produce		json
//	@Param			token	path		string	true	"Session token"
//	@Success		200		{object}	session.GetSessionEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/waitlist/session/{token} [get]
func (h *Handler) getSession(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	currentStep, partial, err := h.service.GetSession(r.Context(), token)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"current_step": currentStep,
		"partial":      partial,
	})
}

// saveStep1 godoc
//
//	@Summary		Save step 1
//	@Description	Saves industry selections for the onboarding session.
//	@Tags			session
//	@Accept			json
//	@Produce		json
//	@Param			token	path		string				true	"Session token"
//	@Param			body	body		session.Step1Request	true	"Step 1 payload"
//	@Success		200		{object}	session.StepProgressEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/waitlist/session/{token}/step/1 [patch]
func (h *Handler) saveStep1(w http.ResponseWriter, r *http.Request) {
	var payload Step1Request
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	ids, err := ParseUUIDs(payload.IndustryIDs)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	nextStep, err := h.service.SaveStep1(r.Context(), chi.URLParam(r, "token"), ids, payload.OtherIndustry)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]int{"current_step": nextStep})
}

// saveStep2 godoc
//
//	@Summary		Save step 2
//	@Description	Saves hiring tool selections for the onboarding session.
//	@Tags			session
//	@Accept			json
//	@Produce		json
//	@Param			token	path		string				true	"Session token"
//	@Param			body	body		session.Step2Request	true	"Step 2 payload"
//	@Success		200		{object}	session.StepProgressEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/waitlist/session/{token}/step/2 [patch]
func (h *Handler) saveStep2(w http.ResponseWriter, r *http.Request) {
	var payload Step2Request
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	ids, err := ParseUUIDs(payload.ToolIDs)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	nextStep, err := h.service.SaveStep2(r.Context(), chi.URLParam(r, "token"), ids, payload.OtherTool)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]int{"current_step": nextStep})
}

// saveStep3 godoc
//
//	@Summary		Save step 3
//	@Description	Saves hiring frustration selections for the onboarding session.
//	@Tags			session
//	@Accept			json
//	@Produce		json
//	@Param			token	path		string				true	"Session token"
//	@Param			body	body		session.Step3Request	true	"Step 3 payload"
//	@Success		200		{object}	session.StepProgressEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/waitlist/session/{token}/step/3 [patch]
func (h *Handler) saveStep3(w http.ResponseWriter, r *http.Request) {
	var payload Step3Request
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	ids, err := ParseUUIDs(payload.FrustrationIDs)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	nextStep, err := h.service.SaveStep3(r.Context(), chi.URLParam(r, "token"), ids, payload.OtherFrustration)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]int{"current_step": nextStep})
}

// saveStep4 godoc
//
//	@Summary		Save step 4
//	@Description	Saves role and team size selections for the onboarding session.
//	@Tags			session
//	@Accept			json
//	@Produce		json
//	@Param			token	path		string				true	"Session token"
//	@Param			body	body		session.Step4Request	true	"Step 4 payload"
//	@Success		200		{object}	session.StepProgressEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/waitlist/session/{token}/step/4 [patch]
func (h *Handler) saveStep4(w http.ResponseWriter, r *http.Request) {
	var payload Step4Request
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	roleIDs, err := ParseUUIDs([]string{payload.RoleID})
	if err != nil {
		response.Error(w, r, err)
		return
	}

	teamSizeIDs, err := ParseUUIDs([]string{payload.TeamSizeID})
	if err != nil {
		response.Error(w, r, err)
		return
	}

	nextStep, err := h.service.SaveStep4(r.Context(), chi.URLParam(r, "token"), roleIDs[0], teamSizeIDs[0])
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]int{"current_step": nextStep})
}

// submit godoc
//
//	@Summary		Submit onboarding session
//	@Description	Finalizes the onboarding flow and creates a waitlist submission.
//	@Tags			session
//	@Accept			json
//	@Produce		json
//	@Param			token	path		string				true	"Session token"
//	@Param			body	body		session.SubmitRequest	true	"Submit payload"
//	@Success		201		{object}	session.SubmitEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/waitlist/session/{token}/submit [post]
func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	var payload SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	result, err := h.service.Submit(r.Context(), chi.URLParam(r, "token"), SubmitInput(payload))
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusCreated, result)
}
