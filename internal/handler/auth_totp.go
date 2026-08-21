package handler

import (
	"errors"
	"net/http"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var _ = apidoc.ErrorEnvelope{}

// totpEnroll godoc
//
//	@Summary		Enroll TOTP authenticator
//	@Description	Generates a new TOTP secret and otpauth URI. Call confirm to enable.
//	@Tags			auth
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	handler.AuthTOTPEnrollEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/auth/totp/enroll [post]
func (h *AuthHandler) totpEnroll(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if h.TOTPCrypto == nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	ctx := r.Context()
	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if h.userTOTPEnabled(user) {
		response.Error(w, r, apperror.New(apperror.CodeConflict, "totp already enabled"))
		return
	}

	key, err := auth.GenerateTOTP(user.Email)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	encrypted, err := h.TOTPCrypto.Encrypt(key.Secret())
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	sql, args, err := query.SetAccountUserTOTPSecret(userID, encrypted)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := pool.QueryRow(ctx, sql, args...).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.New(apperror.CodeConflict, "totp already enabled"))
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, AuthTOTPEnrollData{
		Secret:     key.Secret(),
		OTPAuthURL: key.URL(),
	})
}

// totpConfirm godoc
//
//	@Summary		Confirm TOTP enrollment
//	@Description	Validates a code from the authenticator app and enables TOTP.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.AuthTOTPConfirmRequest	true	"Confirm payload"
//	@Success		200		{object}	handler.AuthMessageEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/totp/confirm [post]
func (h *AuthHandler) totpConfirm(w http.ResponseWriter, r *http.Request) {
	var req authTOTPConfirmBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if h.TOTPCrypto == nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	ctx := r.Context()
	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil || user.TOTPSecret == nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "enroll totp before confirming"))
		return
	}
	if h.userTOTPEnabled(user) {
		response.Error(w, r, apperror.New(apperror.CodeConflict, "totp already enabled"))
		return
	}

	secret, err := h.TOTPCrypto.Decrypt(*user.TOTPSecret)
	if err != nil || !auth.ValidateTOTP(secret, req.Code) {
		response.Error(w, r, apperror.New(apperror.CodeInvalidVerificationCode, "invalid authenticator code"))
		return
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	sql, args, err := query.EnableAccountUserTOTP(userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := pool.QueryRow(ctx, sql, args...).Scan(&userID); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, AuthMessageData{Message: "totp enabled"})
}

// totpDisable godoc
//
//	@Summary		Disable TOTP
//	@Description	Disables TOTP after validating password and current authenticator code.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.AuthTOTPDisableRequest	true	"Disable payload"
//	@Success		200		{object}	handler.AuthMessageEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/totp/disable [post]
func (h *AuthHandler) totpDisable(w http.ResponseWriter, r *http.Request) {
	var req authTOTPDisableBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if h.TOTPCrypto == nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	ctx := r.Context()
	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil || !h.userTOTPEnabled(user) {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "totp is not enabled"))
		return
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	secret, err := h.TOTPCrypto.Decrypt(*user.TOTPSecret)
	if err != nil || !auth.ValidateTOTP(secret, req.Code) {
		response.Error(w, r, apperror.New(apperror.CodeInvalidVerificationCode, "invalid authenticator code"))
		return
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	sql, args, err := query.DisableAccountUserTOTP(userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := pool.QueryRow(ctx, sql, args...).Scan(&userID); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, AuthMessageData{Message: "totp disabled"})
}

// loginTOTP godoc
//
//	@Summary		Complete login with TOTP
//	@Description	Exchanges an MFA challenge token and authenticator code for JWTs.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthLoginTOTPRequest	true	"Login TOTP payload"
//	@Success		200		{object}	handler.AuthTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/login/totp [post]
func (h *AuthHandler) loginTOTP(w http.ResponseWriter, r *http.Request) {
	var req authLoginTOTPBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}
	if h.TOTPCrypto == nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	claims, err := h.Tokens.ParseMFA(req.MFAToken)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	reg := region.Region(claims.Region)
	if !region.Valid(reg) {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	ctx := r.Context()
	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil || !h.userTOTPEnabled(user) {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	secret, err := h.TOTPCrypto.Decrypt(*user.TOTPSecret)
	if err != nil || !auth.ValidateTOTP(secret, req.Code) {
		response.Error(w, r, apperror.New(apperror.CodeInvalidVerificationCode, "invalid authenticator code"))
		return
	}

	h.respondAuthTokens(w, r, ctx, reg, user)
}
