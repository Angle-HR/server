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

func (h *Handler) saveStep1(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		IndustryIDs   []string `json:"industry_ids"`
		OtherIndustry *string  `json:"other_industry"`
	}
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

func (h *Handler) saveStep2(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		ToolIDs   []string `json:"tool_ids"`
		OtherTool *string  `json:"other_tool"`
	}
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

func (h *Handler) saveStep3(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		FrustrationIDs   []string `json:"frustration_ids"`
		OtherFrustration *string  `json:"other_frustration"`
	}
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

func (h *Handler) saveStep4(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		RoleID     string `json:"role_id"`
		TeamSizeID string `json:"team_size_id"`
	}
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

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name             string `json:"name"`
		WantsEarlyAccess bool   `json:"wants_early_access"`
		WantsUserTesting bool   `json:"wants_user_testing"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	result, err := h.service.Submit(r.Context(), chi.URLParam(r, "token"), SubmitInput{
		Name:             payload.Name,
		WantsEarlyAccess: payload.WantsEarlyAccess,
		WantsUserTesting: payload.WantsUserTesting,
	})
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusCreated, result)
}
