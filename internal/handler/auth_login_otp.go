package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
	"github.com/google/uuid"
)

var _ = apidoc.ErrorEnvelope{}

// loginOTPRequest godoc
//
//	@Summary		Request passwordless login OTP
//	@Description	Sends a 6-digit sign-in code. Separate from signup email verification.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthLoginOTPRequest	true	"Login OTP request"
//	@Success		200		{object}	handler.AuthSignupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/login/otp/request [post]
func (h *AuthHandler) loginOTPRequest(w http.ResponseWriter, r *http.Request) {
	var req authLoginOTPRequestBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	ctx := r.Context()

	reg, userID, err := h.resolveUserRegion(ctx, email)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil || user.EmailVerifiedAt == nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	ok, err := h.Verifier.CanResendByEmail(ctx, email)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if !ok {
		response.Error(w, r, apperror.New(apperror.CodeVerificationRateLimited, apperror.MsgVerificationRateLimited))
		return
	}

	data, err := h.beginLoginOTP(ctx, user.ID, email, string(reg))
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := h.Verifier.MarkResentByEmail(ctx, email); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, data)
}

// loginOTPVerify godoc
//
//	@Summary		Verify passwordless login OTP
//	@Description	Exchanges a login OTP for JWTs (or MFA challenge when TOTP is enabled).
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthLoginOTPVerifyRequest	true	"Login OTP verify"
//	@Success		200		{object}	handler.AuthTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/login/otp/verify [post]
func (h *AuthHandler) loginOTPVerify(w http.ResponseWriter, r *http.Request) {
	var req authLoginOTPVerifyBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()
	session, err := h.Verifier.ValidateCodeForPurpose(ctx, req.VerificationSessionID, req.Code, auth.PurposeLoginOTP)
	if err != nil {
		if errors.Is(err, auth.ErrVerificationExpired) {
			response.Error(w, r, apperror.New(apperror.CodeVerificationExpired, apperror.MsgVerificationExpired))
			return
		}
		if errors.Is(err, auth.ErrInvalidVerificationCode) {
			response.Error(w, r, apperror.New(apperror.CodeInvalidVerificationCode, apperror.MsgInvalidVerificationCode))
			return
		}
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	reg := region.Region(session.Region)
	user, err := h.loadUserByID(ctx, reg, session.UserID)
	if err != nil || user.EmailVerifiedAt == nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	if h.userTOTPEnabled(user) {
		h.respondMFARequired(w, r, user.ID, reg)
		return
	}

	h.respondAuthTokens(w, r, ctx, reg, user)
}

func (h *AuthHandler) beginLoginOTP(ctx context.Context, userID uuid.UUID, email, reg string) (AuthSignupData, error) {
	sessionID := uuid.NewString()
	code, err := generateOTP()
	if err != nil {
		return AuthSignupData{}, err
	}

	session := auth.VerificationSession{
		SessionID: sessionID,
		UserID:    userID,
		Email:     email,
		Region:    reg,
		Code:      code,
		Purpose:   auth.PurposeLoginOTP,
	}

	if err := h.Verifier.CreateSession(ctx, session); err != nil {
		return AuthSignupData{}, err
	}

	h.enqueueLoginOTPEmail(ctx, email, code)

	return AuthSignupData{
		VerificationSessionID:    sessionID,
		Email:                    email,
		CodeExpiresInSeconds:     auth.CodeExpiresInSeconds(),
		ResendAvailableInSeconds: auth.ResendAvailableInSeconds(),
	}, nil
}

func (h *AuthHandler) enqueueLoginOTPEmail(ctx context.Context, email, code string) {
	if h.Enqueuer == nil {
		return
	}
	tx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return
	}
	defer rollbackOnError(ctx, tx)

	_, _ = h.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type:             mailer.TypeLoginOTP,
		Recipient:        email,
		Code:             code,
		ExpiresInSeconds: auth.CodeExpiresInSeconds(),
	}, queue.EmailEnqueueOptions()...)
	_ = tx.Commit(ctx)
}
