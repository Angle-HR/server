package handler

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// AuthHandler handles product auth endpoints.
type AuthHandler struct {
	Router        *dbrouter.DBRouter
	GlobalDB      globalDB
	Redis         *goredis.Client
	Tokens        *auth.TokenService
	Verifier      *auth.VerificationStore
	Revoker       *auth.RevocationStore
	Resetter      *auth.ResetStore
	TOTPCrypto    *auth.TOTPCrypto
	Enqueuer      jobEnqueuer
	DefaultRegion region.Region
	validate      *validator.Validate
}

// NewAuthHandler returns an auth handler.
func NewAuthHandler(
	router *dbrouter.DBRouter,
	globalDB globalDB,
	redisClient *goredis.Client,
	tokens *auth.TokenService,
	enqueuer jobEnqueuer,
	defaultRegion region.Region,
	totpCrypto *auth.TOTPCrypto,
) *AuthHandler {
	return &AuthHandler{
		Router:        router,
		GlobalDB:      globalDB,
		Redis:         redisClient,
		Tokens:        tokens,
		Verifier:      auth.NewVerificationStore(redisClient),
		Revoker:       auth.NewRevocationStore(redisClient),
		Resetter:      auth.NewResetStore(redisClient),
		TOTPCrypto:    totpCrypto,
		Enqueuer:      enqueuer,
		DefaultRegion: defaultRegion,
		validate:      validator.New(),
	}
}

type authSignupBody struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

type authSignupPatchBody struct {
	VerificationSessionID string `json:"verification_session_id" validate:"required,uuid"`
	Email                 string `json:"email" validate:"required,email,max=254"`
}

type authVerifyEmailBody struct {
	VerificationSessionID string `json:"verification_session_id" validate:"required,uuid"`
	Code                  string `json:"code" validate:"required,len=6,numeric"`
}

type authResendBody struct {
	VerificationSessionID string `json:"verification_session_id" validate:"required,uuid"`
}

type authLoginBody struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

type authRefreshBody struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

type authLogoutBody struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

type authForgotPasswordBody struct {
	Email string `json:"email" validate:"required,email,max=254"`
}

type authResetPasswordBody struct {
	Token    string `json:"token" validate:"required,min=16,max=128"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

type authLoginOTPRequestBody struct {
	Email string `json:"email" validate:"required,email,max=254"`
}

type authLoginOTPVerifyBody struct {
	VerificationSessionID string `json:"verification_session_id" validate:"required,uuid"`
	Code                  string `json:"code" validate:"required,len=6,numeric"`
}

type authLoginTOTPBody struct {
	MFAToken string `json:"mfa_token" validate:"required"`
	Code     string `json:"code" validate:"required,len=6,numeric"`
}

type authTOTPConfirmBody struct {
	Code string `json:"code" validate:"required,len=6,numeric"`
}

type authTOTPDisableBody struct {
	Code     string `json:"code" validate:"required,len=6,numeric"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

type authAcceptInviteBody struct {
	Token     string  `json:"token" validate:"required"`
	Password  string  `json:"password" validate:"required,min=8,max=128"`
	FirstName *string `json:"first_name,omitempty" validate:"omitempty,min=1,max=120"`
	LastName  *string `json:"last_name,omitempty" validate:"omitempty,min=1,max=120"`
}

type authCreateOrgInviteBody struct {
	Email string `json:"email" validate:"required,email,max=254"`
}

type accountUser struct {
	ID                    uuid.UUID
	Email                 string
	PasswordHash          string
	EmailVerifiedAt       *time.Time
	OnboardingCompletedAt *time.Time
	AccountType           *string
	FirstName             *string
	LastName              *string
	LegalFullName         *string
	CountryID             *uuid.UUID
	TOTPSecret            *string
	TOTPEnabledAt         *time.Time
}

// signup godoc
//
//	@Summary		Product signup
//	@Description	Creates an unverified user in AUTH_DEFAULT_REGION (default uk) and enqueues a 6-digit verification email. OTP expires in 300 seconds.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthSignupRequest	true	"Signup payload"
//	@Success		201		{object}	handler.AuthSignupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/signup [post]
func (h *AuthHandler) signup(w http.ResponseWriter, r *http.Request) {
	var req authSignupBody
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

	if existing, err := h.loadUserByEmail(ctx, h.DefaultRegion, email); err == nil {
		if existing.EmailVerifiedAt != nil {
			response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
			return
		}
		response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	pool, err := h.Router.DB(h.DefaultRegion)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	sql, args, err := query.InsertAccountUser(email, passwordHash)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	var userID uuid.UUID
	if err := pool.QueryRow(ctx, sql, args...).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	data, err := h.beginVerification(ctx, userID, email, string(h.DefaultRegion))
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusCreated, data)
}

// patchSignup godoc
//
//	@Summary		Update signup email
//	@Description	Changes the email on an unverified signup and invalidates the prior OTP.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthSignupPatchRequest	true	"Email update payload"
//	@Success		200		{object}	handler.AuthSignupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/signup [patch]
func (h *AuthHandler) patchSignup(w http.ResponseWriter, r *http.Request) {
	var req authSignupPatchBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()
	session, err := h.Verifier.GetSession(ctx, req.VerificationSessionID)
	if err != nil {
		if errors.Is(err, auth.ErrVerificationExpired) {
			response.Error(w, r, apperror.New(apperror.CodeVerificationExpired, apperror.MsgVerificationExpired))
			return
		}
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email != session.Email {
		if _, err := h.loadUserByEmail(ctx, region.Region(session.Region), email); err == nil {
			response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
			return
		} else if !errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
	}

	pool, err := h.Router.DB(region.Region(session.Region))
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	sql, args, err := query.UpdateAccountUserEmail(session.UserID, email)
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

	_ = h.Verifier.DeleteSession(ctx, req.VerificationSessionID)

	data, err := h.beginVerification(ctx, session.UserID, email, session.Region)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, data)
}

// verifyEmail godoc
//
//	@Summary		Verify email
//	@Description	Validates the 6-digit OTP and returns JWT tokens with onboarding status.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthVerifyEmailRequest	true	"Verification payload"
//	@Success		200		{object}	handler.AuthTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/verify-email [post]
func (h *AuthHandler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var req authVerifyEmailBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()
	session, err := h.Verifier.ValidateCode(ctx, req.VerificationSessionID, req.Code)
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
	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	defer rollbackOnError(ctx, tx)

	verifySQL, verifyArgs, err := query.SetAccountUserVerified(session.UserID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := tx.QueryRow(ctx, verifySQL, verifyArgs...).Scan(&session.UserID); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	currentStep, completed := onboarding.InitialProgress()
	progressSQL, progressArgs, err := query.UpsertOnboardingProgress(session.UserID, currentStep, completed)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if _, err := tx.Exec(ctx, progressSQL, progressArgs...); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	globalTx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	defer rollbackOnError(ctx, globalTx)

	registrySQL, registryArgs, err := query.UpsertUsersRegistryProductUser(session.Email, session.Region, regionSourceExplicit, session.UserID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if _, err := globalTx.Exec(ctx, registrySQL, registryArgs...); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := globalTx.Commit(ctx); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	tokens, err := h.Tokens.IssuePair(session.UserID, reg, true)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	summary := onboardingSummary(nil, currentStep, completed, nil)
	response.Success(w, r, http.StatusOK, AuthTokenData{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
		Onboarding:   summary,
	})
}

// resendVerification godoc
//
//	@Summary		Resend verification code
//	@Description	Sends a new OTP for an existing unverified signup. Rate-limited to once every 30 seconds per session.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthResendVerificationRequest	true	"Resend payload"
//	@Success		200		{object}	handler.AuthSignupEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/resend-verification [post]
func (h *AuthHandler) resendVerification(w http.ResponseWriter, r *http.Request) {
	var req authResendBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()
	ok, err := h.Verifier.CanResend(ctx, req.VerificationSessionID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if !ok {
		response.Error(w, r, apperror.New(apperror.CodeVerificationRateLimited, apperror.MsgVerificationRateLimited))
		return
	}

	session, err := h.Verifier.GetSession(ctx, req.VerificationSessionID)
	if err != nil {
		if errors.Is(err, auth.ErrVerificationExpired) {
			response.Error(w, r, apperror.New(apperror.CodeVerificationExpired, apperror.MsgVerificationExpired))
			return
		}
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	code, err := generateOTP()
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	session.Code = code
	session.Attempts = 0

	if err := h.Verifier.ReplaceSession(ctx, session); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := h.Verifier.MarkResent(ctx, req.VerificationSessionID); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	h.enqueueVerificationEmail(ctx, session.Email, code)

	response.Success(w, r, http.StatusOK, AuthSignupData{
		VerificationSessionID:    session.SessionID,
		Email:                    session.Email,
		CodeExpiresInSeconds:     auth.CodeExpiresInSeconds(),
		ResendAvailableInSeconds: auth.ResendAvailableInSeconds(),
	})
}

// login godoc
//
//	@Summary		Login
//	@Description	Authenticates a verified user and returns JWT tokens. When credentials are valid but email is unverified, returns 403 with a new verification session in error.details.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthLoginRequest	true	"Login payload"
//	@Success		200		{object}	handler.AuthTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/login [post]
func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	var req authLoginBody
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
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	if user.EmailVerifiedAt == nil {
		ok, err := h.Verifier.CanResendByEmail(ctx, email)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		if !ok {
			response.Error(w, r, apperror.New(apperror.CodeVerificationRateLimited, apperror.MsgVerificationRateLimited))
			return
		}

		data, err := h.beginVerification(ctx, user.ID, email, string(reg))
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		if err := h.Verifier.MarkResentByEmail(ctx, email); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		response.Error(w, r, apperror.NewWithDetails(
			apperror.CodeEmailNotVerified,
			apperror.MsgEmailNotVerified,
			verificationDetails(data),
		))
		return
	}

	if user.TOTPEnabledAt != nil && user.TOTPSecret != nil {
		h.respondMFARequired(w, r, user.ID, reg)
		return
	}

	h.respondAuthTokens(w, r, ctx, reg, user)
}

// refresh godoc
//
//	@Summary		Refresh access token
//	@Description	Exchanges a valid refresh token for a new access token.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthRefreshRequest	true	"Refresh payload"
//	@Success		200		{object}	handler.AuthRefreshEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/refresh [post]
func (h *AuthHandler) refresh(w http.ResponseWriter, r *http.Request) {
	var req authRefreshBody
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
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	ctx := r.Context()
	if h.Revoker != nil {
		ok, err := h.Revoker.IsRefreshValid(ctx, claims)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		if !ok {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	reg, _, err := h.lookupRegistryByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil || user.EmailVerifiedAt == nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	accessToken, expiresIn, err := h.Tokens.IssueAccess(req.RefreshToken, reg, true)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	response.Success(w, r, http.StatusOK, AuthRefreshData{
		AccessToken: accessToken,
		ExpiresIn:   expiresIn,
	})
}

func (h *AuthHandler) beginVerification(ctx context.Context, userID uuid.UUID, email, reg string) (AuthSignupData, error) {
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
		Purpose:   auth.PurposeEmailVerify,
	}

	if err := h.Verifier.CreateSession(ctx, session); err != nil {
		return AuthSignupData{}, err
	}

	h.enqueueVerificationEmail(ctx, email, code)

	return AuthSignupData{
		VerificationSessionID:    sessionID,
		Email:                    email,
		CodeExpiresInSeconds:     auth.CodeExpiresInSeconds(),
		ResendAvailableInSeconds: auth.ResendAvailableInSeconds(),
	}, nil
}

func verificationDetails(data AuthSignupData) map[string]any {
	return map[string]any{
		"verification_session_id":     data.VerificationSessionID,
		"email":                       data.Email,
		"code_expires_in_seconds":     data.CodeExpiresInSeconds,
		"resend_available_in_seconds": data.ResendAvailableInSeconds,
	}
}

func (h *AuthHandler) enqueueVerificationEmail(ctx context.Context, email, code string) {
	if h.Enqueuer == nil {
		return
	}

	tx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return
	}
	defer rollbackOnError(ctx, tx)

	_, _ = h.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type:             mailer.TypeEmailVerification,
		Recipient:        email,
		Code:             code,
		ExpiresInSeconds: auth.CodeExpiresInSeconds(),
	}, queue.EmailEnqueueOptions()...)
	_ = tx.Commit(ctx)
}

func (h *AuthHandler) loadUserByEmail(ctx context.Context, reg region.Region, email string) (accountUser, error) {
	pool, err := h.Router.DB(reg)
	if err != nil {
		return accountUser{}, err
	}

	sql, args, err := query.LookupAccountUserByEmail(email)
	if err != nil {
		return accountUser{}, err
	}

	return scanAccountUser(pool.QueryRow(ctx, sql, args...))
}

func (h *AuthHandler) loadUserByID(ctx context.Context, reg region.Region, userID uuid.UUID) (accountUser, error) {
	pool, err := h.Router.DB(reg)
	if err != nil {
		return accountUser{}, err
	}

	sql, args, err := query.LookupAccountUserByID(userID)
	if err != nil {
		return accountUser{}, err
	}

	return scanAccountUser(pool.QueryRow(ctx, sql, args...))
}

func (h *AuthHandler) resolveUserRegion(ctx context.Context, email string) (region.Region, uuid.UUID, error) {
	sql, args, err := query.LookupUsersRegistryByEmail(email)
	if err != nil {
		return region.RegionUnknown, uuid.Nil, err
	}

	var registryID uuid.UUID
	var registryEmail string
	var reg string
	var userID *uuid.UUID
	if err := h.GlobalDB.QueryRow(ctx, sql, args...).Scan(&registryID, &registryEmail, &reg, &userID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return region.RegionUnknown, uuid.Nil, err
		}
		user, err := h.loadUserByEmail(ctx, h.DefaultRegion, email)
		if err != nil {
			return region.RegionUnknown, uuid.Nil, err
		}
		return h.DefaultRegion, user.ID, nil
	}

	if userID == nil {
		return region.RegionUnknown, uuid.Nil, pgx.ErrNoRows
	}

	return region.Region(reg), *userID, nil
}

func (h *AuthHandler) lookupRegistryByUserID(ctx context.Context, userID uuid.UUID) (region.Region, string, error) {
	const sql = `SELECT email, region FROM users_registry WHERE user_id = $1`
	var email, reg string
	if err := h.GlobalDB.QueryRow(ctx, sql, userID).Scan(&email, &reg); err != nil {
		return region.RegionUnknown, "", err
	}

	return region.Region(reg), email, nil
}

func (h *AuthHandler) loadOnboardingSummary(ctx context.Context, reg region.Region, user accountUser) (OnboardingProgressSummary, error) {
	if user.OnboardingCompletedAt != nil {
		status := onboarding.StatusCompleted
		return OnboardingProgressSummary{
			Status:         status,
			CompletedSteps: onboarding.RequiredSteps(stringValue(user.AccountType)),
		}, nil
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		return OnboardingProgressSummary{}, err
	}

	progressSQL, progressArgs, err := query.LookupOnboardingProgress(user.ID)
	if err != nil {
		return OnboardingProgressSummary{}, err
	}

	var currentStep string
	var completed []string
	if err := pool.QueryRow(ctx, progressSQL, progressArgs...).Scan(&user.ID, &currentStep, &completed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			currentStep, completed = onboarding.InitialProgress()
		} else {
			return OnboardingProgressSummary{}, err
		}
	}

	return onboardingSummary(user.OnboardingCompletedAt, currentStep, completed, user.AccountType), nil
}

func scanAccountUser(row pgx.Row) (accountUser, error) {
	var user accountUser
	err := row.Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.EmailVerifiedAt,
		&user.OnboardingCompletedAt,
		&user.AccountType,
		&user.FirstName,
		&user.LastName,
		&user.LegalFullName,
		&user.CountryID,
		&user.TOTPSecret,
		&user.TOTPEnabledAt,
	)
	return user, err
}

func (h *AuthHandler) respondAuthTokens(w http.ResponseWriter, r *http.Request, ctx context.Context, reg region.Region, user accountUser) {
	tokens, err := h.Tokens.IssuePair(user.ID, reg, true)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	summary, err := h.loadOnboardingSummary(ctx, reg, user)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, AuthTokenData{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
		Onboarding:   summary,
	})
}

func (h *AuthHandler) respondMFARequired(w http.ResponseWriter, r *http.Request, userID uuid.UUID, reg region.Region) {
	mfaToken, expiresIn, err := h.Tokens.IssueMFAToken(userID, reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	response.Success(w, r, http.StatusOK, AuthMFARequiredData{
		TOTPRequired: true,
		MFAToken:     mfaToken,
		ExpiresIn:    expiresIn,
	})
}

func (h *AuthHandler) userTOTPEnabled(user accountUser) bool {
	return user.TOTPEnabledAt != nil && user.TOTPSecret != nil && *user.TOTPSecret != ""
}

func onboardingSummary(completedAt *time.Time, currentStep string, completed []string, accountType *string) OnboardingProgressSummary {
	if completedAt != nil {
		return OnboardingProgressSummary{
			Status:         onboarding.StatusCompleted,
			CompletedSteps: completed,
		}
	}

	next := onboarding.NextStep(stringValue(accountType), onboarding.NormalizeCompletedSteps(completed))
	return OnboardingProgressSummary{
		Status:         onboarding.StatusInProgress,
		CurrentStep:    &currentStep,
		CompletedSteps: completed,
		NextStep:       &next,
	}
}

func generateOTP() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(b[:]) % 1000000
	return fmt.Sprintf("%06d", n), nil
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func rollbackOnError(ctx context.Context, tx pgx.Tx) {
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		_ = err
	}
}
