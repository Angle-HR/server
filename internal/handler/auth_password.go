package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
	"github.com/jackc/pgx/v5"
)

var _ = apidoc.ErrorEnvelope{}

// forgotPassword godoc
//
//	@Summary		Request password reset
//	@Description	Always returns 200. When the email exists, enqueues a reset link (token TTL 1 hour).
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthForgotPasswordRequest	true	"Forgot password payload"
//	@Success		200		{object}	handler.AuthMessageEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/forgot-password [post]
func (h *AuthHandler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var req authForgotPasswordBody
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
	msg := AuthMessageData{Message: "If an account exists for that email, a reset link has been sent."}

	reg, userID, err := h.resolveUserRegion(ctx, email)
	if err != nil {
		response.Success(w, r, http.StatusOK, msg)
		return
	}

	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil || user.EmailVerifiedAt == nil {
		response.Success(w, r, http.StatusOK, msg)
		return
	}

	if h.Resetter == nil {
		response.Success(w, r, http.StatusOK, msg)
		return
	}

	token, err := h.Resetter.CreateToken(ctx, auth.ResetSession{
		UserID: user.ID,
		Email:  user.Email,
		Region: string(reg),
	})
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	h.enqueuePasswordResetEmail(ctx, user.Email, token)
	response.Success(w, r, http.StatusOK, msg)
}

// resetPassword godoc
//
//	@Summary		Reset password
//	@Description	Sets a new password using a forgot-password token and revokes existing refresh sessions.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthResetPasswordRequest	true	"Reset password payload"
//	@Success		200		{object}	handler.AuthMessageEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/reset-password [post]
func (h *AuthHandler) resetPassword(w http.ResponseWriter, r *http.Request) {
	var req authResetPasswordBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()
	if h.Resetter == nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	session, err := h.Resetter.ConsumeToken(ctx, req.Token)
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeVerificationExpired, "reset token invalid or expired"))
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	reg := region.Region(session.Region)
	if !region.Valid(reg) {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	sql, args, err := query.UpdateAccountUserPassword(session.UserID, passwordHash)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := pool.QueryRow(ctx, sql, args...).Scan(&session.UserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.ErrNotFound)
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if h.Revoker != nil {
		_ = h.Revoker.RevokeUserSessions(ctx, session.UserID, h.Tokens.RefreshLifetime())
	}

	response.Success(w, r, http.StatusOK, AuthMessageData{Message: "password updated"})
}

func (h *AuthHandler) enqueuePasswordResetEmail(ctx context.Context, email, token string) {
	if h.Enqueuer == nil {
		return
	}
	tx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return
	}
	defer rollbackOnError(ctx, tx)

	_, _ = h.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type:             mailer.TypePasswordReset,
		Recipient:        email,
		Token:            token,
		ExpiresInSeconds: auth.ResetTTLSeconds(),
	}, queue.EmailEnqueueOptions()...)
	_ = tx.Commit(ctx)
}
