package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
	"github.com/go-chi/chi/v5"
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

type acceptInviteBody struct {
	Token    string `json:"token" validate:"required"`
	Name     string `json:"name" validate:"required,min=1,max=120"`
	Password string `json:"password" validate:"required,min=8,max=128"`
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

	ctx := r.Context()
	if err := h.PasswordLockout.Check(ctx, req.Email); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeTooManyAttempts, apperror.MsgTooManyAttempts))
		return
	}

	user, err := h.Store.GetUserByEmail(ctx, req.Email)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if !user.IsActive || user.PasswordHash == nil || !auth.CheckPassword(*user.PasswordHash, req.Password) {
		_, _ = h.PasswordLockout.RecordFailure(ctx, req.Email)
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	_ = h.PasswordLockout.Reset(ctx, req.Email)

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
	if err != nil || !user.IsActive || user.PasswordHash == nil {
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

// getInvite godoc
//
//	@Summary		Preview admin invite
//	@Description	Validates an invite token and returns the invited email and expiry.
//	@Tags			admin/auth
//	@Produce		json
//	@Param			token	path		string	true	"Invite token"
//	@Success		200		{object}	handler.AdminInvitePreviewEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/invite/{token} [get]
func (h *AdminHandler) getInvite(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	preview, err := h.Store.GetInviteByToken(r.Context(), token)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, preview)
}

// acceptInvite godoc
//
//	@Summary		Accept admin invite
//	@Description	Sets name and password for an invited admin and returns JWT tokens.
//	@Tags			admin/auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AdminAcceptInviteRequest	true	"Accept payload"
//	@Success		200		{object}	handler.AdminTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/accept-invite [post]
func (h *AdminHandler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var body acceptInviteBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	body.Token = strings.TrimSpace(body.Token)
	body.Name = strings.TrimSpace(body.Name)
	if err := h.validate.Struct(body); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	result, err := h.Store.AcceptInvite(r.Context(), body.Token, body.Name, hash)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	_ = h.Store.WriteAudit(r.Context(), result.User.ID, "staff.invite_accept", "staff", result.User.ID.String(), nil, r.RemoteAddr)

	pair, err := h.Tokens.IssueAdminPair(result.User.ID)
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
