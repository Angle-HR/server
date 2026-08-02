package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
	"github.com/google/uuid"
)

var _ = apidoc.ErrorEnvelope{}

type adminLoginBody struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

type adminRefreshBody struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// login godoc
//
//	@Summary		Admin login
//	@Description	Authenticates an admin user and returns admin JWT tokens.
//	@Tags			admin/auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AdminLoginRequest	true	"Login payload"
//	@Success		200		{object}	handler.AdminTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/login [post]
func (h *AdminHandler) login(w http.ResponseWriter, r *http.Request) {
	var req adminLoginBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	user, err := h.Store.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if !user.IsActive || !auth.CheckPassword(user.PasswordHash, req.Password) {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	pair, err := h.Tokens.IssueAdminPair(user.ID)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"access_token":  pair.AccessToken,
		"refresh_token": pair.RefreshToken,
		"expires_in":    pair.ExpiresIn,
		"token_type":    "Bearer",
	})
}

// refresh godoc
//
//	@Summary		Admin token refresh
//	@Description	Issues a new admin access/refresh pair from a valid admin refresh token.
//	@Tags			admin/auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AdminRefreshRequest	true	"Refresh payload"
//	@Success		200		{object}	handler.AdminTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/refresh [post]
func (h *AdminHandler) refresh(w http.ResponseWriter, r *http.Request) {
	var req adminRefreshBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	claims, err := h.Tokens.ParseAdminRefresh(req.RefreshToken)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	user, err := h.Store.GetUserByID(r.Context(), userID)
	if err != nil || !user.IsActive {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	pair, err := h.Tokens.IssueAdminPair(user.ID)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"access_token":  pair.AccessToken,
		"refresh_token": pair.RefreshToken,
		"expires_in":    pair.ExpiresIn,
		"token_type":    "Bearer",
	})
}

// me godoc
//
//	@Summary		Admin profile
//	@Description	Returns the authenticated admin profile, roles, and permissions.
//	@Tags			admin/auth
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	handler.AdminMeEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/me [get]
func (h *AdminHandler) me(w http.ResponseWriter, r *http.Request) {
	userID, email, perms, ok := auth.AdminFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	user, err := h.Store.GetUserByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, apperror.ErrNotFound) {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}
		response.Error(w, r, err)
		return
	}

	roles, err := h.Store.ListRolesForUser(r.Context(), userID)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"id":          user.ID,
		"email":       email,
		"name":        user.Name,
		"is_active":   user.IsActive,
		"roles":       roles,
		"permissions": perms,
	})
}
