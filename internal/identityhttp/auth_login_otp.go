package identityhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// loginOTPRequest godoc
//
//	@Summary		Request passwordless login OTP
//	@Description	Sends a 6-digit sign-in code. Separate from signup email verification. Always answers 200 with a session, even for unknown or unverified emails (no code is sent for those and verification fails like a wrong code).
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		identityhttp.AuthLoginOTPRequest	true	"Login OTP request"
//	@Success		200		{object}	identityhttp.AuthSignupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/login/otp/request [post]
func (h *AuthHandler) loginOTPRequest(w http.ResponseWriter, r *http.Request) {
	var req authLoginOTPRequestBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	ctx := r.Context()

	// Unknown and unverified emails get the same 200 as real ones, with a decoy
	// session that can never verify, so the endpoint does not reveal which
	// emails have accounts.
	decoy := true
	var reg region.Region
	var user accountUser
	resolvedReg, userID, err := h.resolveUserRegion(ctx, email)
	switch {
	case err == nil:
		loaded, loadErr := h.loadUserByID(ctx, resolvedReg, userID)
		switch {
		case loadErr == nil:
			if loaded.EmailVerifiedAt != nil {
				decoy, reg, user = false, resolvedReg, loaded
			}
		case !errors.Is(loadErr, pgx.ErrNoRows):
			response.Error(w, r, apperror.ErrInternal)
			return
		}
	case !errors.Is(err, pgx.ErrNoRows):
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	retryAfter, err := h.Verifier.RetryAfterByEmail(ctx, email)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if retryAfter > 0 {
		response.Error(w, r, verificationRateLimited(retryAfter, nil))
		return
	}

	var data AuthSignupData
	if decoy {
		data, err = h.beginLoginOTP(ctx, uuid.New(), email, string(region.RegionGlobal), true)
	} else {
		data, err = h.beginLoginOTP(ctx, user.ID, email, string(reg), false)
	}
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
//	@Description	Exchanges a login OTP for a cookie session and csrf_token (or an MFA pre-auth cookie when TOTP is enabled).
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		identityhttp.AuthLoginOTPVerifyRequest	true	"Login OTP verify"
//	@Success		200		{object}	identityhttp.AuthSessionEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/login/otp/verify [post]
func (h *AuthHandler) loginOTPVerify(w http.ResponseWriter, r *http.Request) {
	var req authLoginOTPVerifyBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()

	// Per-email lockout across all sessions: requesting fresh sessions does not
	// reset the budget. Decoy sessions are tracked the same way.
	pending, err := h.Verifier.GetSession(ctx, req.VerificationSessionID)
	if err != nil {
		if errors.Is(err, auth.ErrVerificationExpired) {
			response.Error(w, r, apperror.New(apperror.CodeVerificationExpired, apperror.MsgVerificationExpired))
			return
		}
		response.Error(w, r, apperror.ErrNotFound)
		return
	}
	if lockErr := h.OTPLockout.Check(ctx, pending.Email); lockErr != nil {
		response.Error(w, r, apperror.New(apperror.CodeTooManyAttempts, apperror.MsgTooManyAttempts))
		return
	}

	session, err := h.Verifier.ValidateCodeForPurpose(ctx, req.VerificationSessionID, req.Code, auth.PurposeLoginOTP)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidVerificationCode) {
			besteffort.Log(ctx, "OTPLockout.RecordFailure", recordLockoutFailure(h.OTPLockout, ctx, pending.Email))
		}
		if errors.Is(err, auth.ErrVerificationExpired) {
			response.Error(w, r, apperror.New(apperror.CodeVerificationExpired, apperror.MsgVerificationExpired))
			return
		}
		if errors.Is(err, auth.ErrInvalidVerificationCode) {
			response.Error(w, r, wrongCodeError(err, apperror.MsgInvalidVerificationCode))
			return
		}
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	besteffort.Log(ctx, "OTPLockout.Reset", h.OTPLockout.Reset(ctx, session.Email))

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

func (h *AuthHandler) beginLoginOTP(
	ctx context.Context,
	userID uuid.UUID,
	email, reg string,
	decoy bool,
) (AuthSignupData, error) {
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
		Decoy:     decoy,
	}

	if err := h.Verifier.CreateSession(ctx, session); err != nil {
		return AuthSignupData{}, err
	}
	if err := h.Verifier.ReplaceLatestSession(ctx, auth.PurposeLoginOTP, email, sessionID); err != nil {
		return AuthSignupData{}, err
	}

	if !decoy {
		h.enqueueLoginOTPEmail(ctx, email, code)
	}

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

	_, enqueueErr := h.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type:             mailer.TypeLoginOTP,
		Recipient:        email,
		Code:             code,
		ExpiresInSeconds: auth.CodeExpiresInSeconds(),
	}, queue.EmailEnqueueOptions()...)
	besteffort.Log(ctx, "Enqueuer.EnqueueTx", enqueueErr)
	besteffort.Log(ctx, "tx.Commit", tx.Commit(ctx))
}
