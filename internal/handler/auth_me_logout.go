package handler

import (
	"net/http"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// me godoc
//
//	@Summary		Current product user
//	@Description	Returns the authenticated product user and onboarding progress.
//	@Tags			auth
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	handler.AuthMeEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/auth/me [get]
func (h *AuthHandler) me(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	ctx := r.Context()
	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	summary, err := h.loadOnboardingSummary(ctx, reg, user)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	var countryID *string
	if user.CountryID != nil {
		s := user.CountryID.String()
		countryID = &s
	}

	response.Success(w, r, http.StatusOK, AuthMeData{
		ID:            user.ID.String(),
		Email:         user.Email,
		EmailVerified: user.EmailVerifiedAt != nil,
		AccountType:   user.AccountType,
		FirstName:     user.FirstName,
		LastName:      user.LastName,
		LegalFullName: user.LegalFullName,
		CountryID:     countryID,
		Region:        string(reg),
		TOTPEnabled:   h.userTOTPEnabled(user),
		Onboarding:    summary,
	})
}

// logout godoc
//
//	@Summary		Logout
//	@Description	Revokes the provided refresh token. Idempotent.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthLogoutRequest	true	"Logout payload"
//	@Success		200		{object}	handler.AuthMessageEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/logout [post]
func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	var req authLogoutBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	claims, err := h.Tokens.ParseRefresh(req.RefreshToken)
	if err != nil {
		// Idempotent: treat invalid/expired refresh as already logged out.
		response.Success(w, r, http.StatusOK, AuthMessageData{Message: "logged out"})
		return
	}

	if h.Revoker != nil && claims.ExpiresAt != nil {
		_ = h.Revoker.RevokeJTI(r.Context(), claims.ID, claims.ExpiresAt.Time)
	}

	response.Success(w, r, http.StatusOK, AuthMessageData{Message: "logged out"})
}
