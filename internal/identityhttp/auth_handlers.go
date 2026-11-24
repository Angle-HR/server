package identityhttp

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
	"github.com/Angle-HR/server/internal/identityevents"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/response"
)

// otpModulus keeps generated one-time codes to six digits.
const otpModulus = 1_000_000

var _ = apidoc.ErrorEnvelope{}

// AuthHandler handles product auth endpoints.
type AuthHandler struct {
	Router          *dbrouter.DBRouter
	GlobalDB        globalDB
	Redis           *goredis.Client
	Sessions        *session.Manager
	Verifier        *auth.VerificationStore
	Resetter        *auth.ResetStore
	TOTPCrypto      *auth.TOTPCrypto
	PasswordLockout *auth.LoginLockout
	TOTPLockout     *auth.LoginLockout
	OTPLockout      *auth.LoginLockout
	Enqueuer        jobEnqueuer
	// MeAccess adds the caller's company roles and permissions to /auth/me. Optional: nil leaves both empty.
	MeAccess func(ctx context.Context, userID string, reg region.Region) (roles, permissions []string)
	// OrgReads reads company data from org over gRPC (ORG_MEMBERSHIP, ORG_ONBOARDING). Nil or an unset field keeps
	// MeAccess and the accounts tables.
	OrgReads *OrgReads
	validate *validator.Validate
}

// Lockout tuning: password guessing gets a longer window since a slow drip
// of attempts is still a brute force; TOTP gets a shorter window matching
// the MFA pre-auth session lifetime, since codes only need protecting while
// a challenge is live.
const (
	passwordLockoutMaxAttempts = 5
	passwordLockoutWindow      = 15 * time.Minute
	passwordLockoutDuration    = 15 * time.Minute

	// Login-OTP verification is locked per email, across every session issued for it.
	otpLockoutMaxAttempts = 5
	otpLockoutWindow      = 15 * time.Minute
	otpLockoutDuration    = 15 * time.Minute

	totpLockoutMaxAttempts = 5
	totpLockoutWindow      = 5 * time.Minute
	totpLockoutDuration    = 15 * time.Minute
)

// NewAuthHandler returns an auth handler. New signups always start in the
// global holding region (region.RegionGlobal) — there is no configurable
// default deployment region for the product auth flow anymore.
func NewAuthHandler(
	router *dbrouter.DBRouter,
	globalDB globalDB,
	redisClient *goredis.Client,
	sessions *session.Manager,
	enqueuer jobEnqueuer,
	totpCrypto *auth.TOTPCrypto,
) *AuthHandler {
	return &AuthHandler{
		Router:     router,
		GlobalDB:   globalDB,
		Redis:      redisClient,
		Sessions:   sessions,
		Verifier:   auth.NewVerificationStore(redisClient),
		Resetter:   auth.NewResetStore(redisClient),
		TOTPCrypto: totpCrypto,
		PasswordLockout: auth.NewLoginLockout(redisClient, "pwd",
			passwordLockoutMaxAttempts, passwordLockoutWindow, passwordLockoutDuration),
		TOTPLockout: auth.NewLoginLockout(redisClient, "totp",
			totpLockoutMaxAttempts, totpLockoutWindow, totpLockoutDuration),
		OTPLockout: auth.NewLoginLockout(redisClient, "otp",
			otpLockoutMaxAttempts, otpLockoutWindow, otpLockoutDuration),
		Enqueuer: enqueuer,
		validate: validator.New(),
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
	Code string `json:"code" validate:"required,len=6,numeric"`
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
//	@Description	Creates an unverified account in the global holding area (no regional database yet) and enqueues a 6-digit verification email. The account is assigned a real region once onboarding is given a country. OTP expires in 300 seconds.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		identityhttp.AuthSignupRequest	true	"Signup payload"
//	@Success		201		{object}	identityhttp.AuthSignupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/signup [post]
func (h *AuthHandler) signup(w http.ResponseWriter, r *http.Request) {
	var req authSignupBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	if err := auth.ValidatePassword(req.Password); err != nil {
		response.Error(w, r, passwordPolicyError("password"))
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	ctx := r.Context()

	// Verified accounts may have moved out of pending_users into a regional
	// database. The global registry retains their email across that move.
	registrySQL, registryArgs, err := query.LookupVerifiedRegistryEmail(email)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	var registeredUserID uuid.UUID
	var registeredRegion string
	if err := h.GlobalDB.QueryRow(ctx, registrySQL, registryArgs...).Scan(&registeredUserID, &registeredRegion); err == nil {
		response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	pending, err := h.loadUserByEmail(ctx, region.RegionGlobal, email)
	if err == nil {
		if pending.EmailVerifiedAt != nil {
			response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
			return
		}
		h.restartPendingSignup(w, r, pending.ID, email, req.Password)
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

	sql, args, err := query.InsertPendingUser(email, passwordHash)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	var userID uuid.UUID
	if scanErr := h.insertPendingUser(ctx, sql, args, &userID); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	data, err := h.beginVerification(ctx, userID, email, string(region.RegionGlobal))
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
//	@Param			body	body		identityhttp.AuthSignupPatchRequest	true	"Email update payload"
//	@Success		200		{object}	identityhttp.AuthSignupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/signup [patch]
func (h *AuthHandler) patchSignup(w http.ResponseWriter, r *http.Request) {
	var req authSignupPatchBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
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

	if session.Purpose != "" && session.Purpose != auth.PurposeEmailVerify {
		// Only signup verification sessions may change the signup email.
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Same cooldowns as resend: per session, and per target email.
	retryAfter, err := h.Verifier.RetryAfterBySession(ctx, req.VerificationSessionID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if retryAfter == 0 && email != session.Email {
		retryAfter, err = h.Verifier.RetryAfterByEmail(ctx, email)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
	}
	if retryAfter > 0 {
		response.Error(w, r, verificationRateLimited(retryAfter, nil))
		return
	}

	if email != session.Email {
		registrySQL, registryArgs, registryErr := query.LookupVerifiedRegistryEmail(email)
		if registryErr != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		var registeredUserID uuid.UUID
		var registeredRegion string
		if scanErr := h.GlobalDB.QueryRow(ctx, registrySQL, registryArgs...).
			Scan(&registeredUserID, &registeredRegion); scanErr == nil {
			response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
			return
		} else if !errors.Is(scanErr, pgx.ErrNoRows) {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		if _, loadUserByEmailErr := h.loadUserByEmail(
			ctx,
			region.Region(session.Region),
			email,
		); loadUserByEmailErr == nil {
			response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
			return
		} else if !errors.Is(
			loadUserByEmailErr,
			pgx.ErrNoRows,
		) {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
	}

	pool, err := resolvePool(h.Router, h.GlobalDB, region.Region(session.Region))
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	sql, args, err := updateUserEmailSQL(region.Region(session.Region), session.UserID, email)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if scanErr := pool.QueryRow(ctx, sql, args...).Scan(&session.UserID); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			response.Error(w, r, apperror.ErrNotFound)
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	besteffort.Log(ctx, "h.Verifier.DeleteSession", h.Verifier.DeleteSession(ctx, req.VerificationSessionID))

	data, err := h.beginVerification(ctx, session.UserID, email, session.Region)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := h.markVerificationSent(ctx, data.VerificationSessionID, email); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, data)
}

// verifyEmail godoc
//
//	@Summary		Verify email
//	@Description	Validates the 6-digit OTP and starts a cookie session and returns onboarding status plus the CSRF token.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		identityhttp.AuthVerifyEmailRequest	true	"Verification payload"
//	@Success		200		{object}	identityhttp.AuthSessionEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/verify-email [post]
func (h *AuthHandler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var req authVerifyEmailBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		var fieldErrors validator.ValidationErrors
		if errors.As(err, &fieldErrors) && len(fieldErrors) == 1 && fieldErrors[0].Field() == "Code" {
			response.Error(w, r, apperror.New(apperror.CodeInvalidVerificationCode, apperror.MsgIncorrectOTP))
			return
		}
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
			response.Error(w, r, wrongCodeError(err, apperror.MsgIncorrectOTP))
			return
		}
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	reg := region.Region(session.Region)
	currentStep, completed, err := h.markEmailVerified(ctx, reg, &session)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	ident, err := h.Sessions.Start(w, r, session.UserID, string(reg), false)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	summary := onboardingSummary(nil, currentStep, completed, nil)
	response.Success(w, r, http.StatusOK, AuthSessionData{
		Onboarding: &summary,
		User:       &AuthSessionUser{ID: session.UserID.String(), Email: session.Email, EmailVerified: true, Region: string(reg)},
		CSRFToken:  ident.CSRFToken,
	})
}

// markEmailVerified marks the user verified, seeds onboarding progress in the
// user's database and records the user in the global registry, committing both
// transactions. Any failure is reported as an internal error.
func (h *AuthHandler) markEmailVerified(
	ctx context.Context,
	reg region.Region,
	session *auth.VerificationSession,
) (currentStep string, completed []string, err error) {
	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		return "", nil, apperror.ErrInternal
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", nil, apperror.ErrInternal
	}
	defer rollbackOnError(ctx, tx)

	verifySQL, verifyArgs, err := setUserVerifiedSQL(reg, session.UserID)
	if err != nil {
		return "", nil, apperror.ErrInternal
	}
	if scanErr := tx.QueryRow(ctx, verifySQL, verifyArgs...).Scan(&session.UserID); scanErr != nil {
		return "", nil, apperror.ErrInternal
	}

	currentStep, completed = onboarding.InitialProgress()
	progressSQL, progressArgs, err := upsertOnboardingProgressSQL(reg, session.UserID, currentStep, completed)
	if err != nil {
		return "", nil, apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, progressSQL, progressArgs...); execErr != nil {
		return "", nil, apperror.ErrInternal
	}
	// identity.user.email_verified.v1 goes to the outbox of the database holding the user, in this transaction.
	if evErr := identityevents.EnqueueEmailVerified(ctx, tx, identityevents.UserEmailVerified{
		UserID: session.UserID.String(), Region: string(reg), VerifiedAt: time.Now(),
	}); evErr != nil {
		return "", nil, apperror.ErrInternal
	}

	globalTx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return "", nil, apperror.ErrInternal
	}
	defer rollbackOnError(ctx, globalTx)

	registrySource := regionSourceExplicit
	if reg == region.RegionGlobal {
		registrySource = regionSourcePending
	}
	registrySQL, registryArgs, err := query.UpsertUsersRegistryProductUser(
		session.Email, session.Region, registrySource, session.UserID,
	)
	if err != nil {
		return "", nil, apperror.ErrInternal
	}
	if _, execErr := globalTx.Exec(ctx, registrySQL, registryArgs...); execErr != nil {
		return "", nil, apperror.ErrInternal
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return "", nil, apperror.ErrInternal
	}
	if commitErr := globalTx.Commit(ctx); commitErr != nil {
		return "", nil, apperror.ErrInternal
	}
	return currentStep, completed, nil
}

// resendVerification godoc
//
//	@Summary		Resend verification code
//	@Description	Sends a new OTP for an existing unverified signup. Rate-limited to once every 30 seconds per session.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		identityhttp.AuthResendVerificationRequest	true	"Resend payload"
//	@Success		200		{object}	identityhttp.AuthSignupEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/resend-verification [post]
func (h *AuthHandler) resendVerification(w http.ResponseWriter, r *http.Request) {
	var req authResendBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()
	retryAfter, err := h.Verifier.RetryAfterBySession(ctx, req.VerificationSessionID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if retryAfter > 0 {
		response.Error(w, r, verificationRateLimited(retryAfter, nil))
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
	// Share the cooldown with sign-in so a resend cannot be sidestepped (or
	// blocked) by requesting a code through another entry point.
	if err := h.Verifier.MarkResentByEmail(ctx, session.Email); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	switch {
	case session.Decoy:
		// Decoy sessions never send mail; the response stays identical.
	case session.Purpose == auth.PurposeLoginOTP:
		h.enqueueLoginOTPEmail(ctx, session.Email, code)
	default:
		h.enqueueVerificationEmail(ctx, session.Email, code)
	}

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
//	@Description	Authenticates a verified user and starts a cookie session (or an MFA pre-auth session when TOTP is enabled). When credentials are valid but email is unverified, returns 403 with a new verification session in error.details.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		identityhttp.AuthLoginRequest	true	"Login payload"
//	@Success		200		{object}	identityhttp.AuthSessionEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/login [post]
func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	var req authLoginBody
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

	if err := h.PasswordLockout.Check(ctx, email); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeTooManyAttempts, apperror.MsgTooManyAttempts))
		return
	}

	reg, userID, err := h.resolveUserRegion(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.failUnknownLogin(w, r, email, req.Password)
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.failUnknownLogin(w, r, email, req.Password)
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if user.PasswordHash == "" {
		// Invited accounts have no password yet; keep timing uniform.
		auth.DummyCheckPassword(req.Password)
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		_, _ = h.PasswordLockout.RecordFailure(ctx, email)
		response.Error(w, r, apperror.New(apperror.CodeUnauthorized, apperror.MsgInvalidCredentials))
		return
	}
	besteffort.Log(ctx, "h.PasswordLockout.Reset", h.PasswordLockout.Reset(ctx, email))

	if user.EmailVerifiedAt == nil {
		response.Error(w, r, h.unverifiedLoginError(ctx, user.ID, email, reg))
		return
	}

	if user.TOTPEnabledAt != nil && user.TOTPSecret != nil {
		h.respondMFARequired(w, r, user.ID, reg)
		return
	}

	h.respondAuthTokens(w, r, ctx, reg, user)
}

// failUnknownLogin answers a login for an email with no account exactly like a
// wrong password: it spends a dummy bcrypt comparison and records the failure
// against the email, so lockout applies whether or not the account exists.
func (h *AuthHandler) failUnknownLogin(w http.ResponseWriter, r *http.Request, email, password string) {
	auth.DummyCheckPassword(password)
	besteffort.Log(r.Context(), "h.PasswordLockout.RecordFailure", recordLockoutFailure(h.PasswordLockout, r.Context(), email))
	response.Error(w, r, apperror.New(apperror.CodeUnauthorized, apperror.MsgInvalidCredentials))
}

func recordLockoutFailure(l *auth.LoginLockout, ctx context.Context, key string) error {
	_, err := l.RecordFailure(ctx, key)
	return err
}

// restartPendingSignup handles a signup for an email whose unverified pending
// account already exists (an abandoned verification): the latest signup wins,
// so the password is replaced and a fresh verification session is started,
// subject to the per-email cooldown.
func (h *AuthHandler) restartPendingSignup(
	w http.ResponseWriter, r *http.Request, userID uuid.UUID, email, password string,
) {
	ctx := r.Context()
	retryAfter, err := h.Verifier.RetryAfterByEmail(ctx, email)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if retryAfter > 0 {
		response.Error(w, r, verificationRateLimited(retryAfter, nil))
		return
	}

	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	sql, args, err := query.UpdatePendingUserPassword(userID, passwordHash)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	var updatedID uuid.UUID
	if scanErr := h.GlobalDB.QueryRow(ctx, sql, args...).Scan(&updatedID); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			// Verified (or removed) between lookup and update: not ours to touch.
			response.Error(w, r, apperror.New(apperror.CodeEmailAlreadyRegistered, apperror.MsgEmailAlreadyRegistered))
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	data, err := h.beginVerification(ctx, userID, email, string(region.RegionGlobal))
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := h.Verifier.MarkResentByEmail(ctx, email); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusCreated, data)
}

// markVerificationSent starts the resend cooldowns for a freshly issued session.
func (h *AuthHandler) markVerificationSent(ctx context.Context, sessionID, email string) error {
	if err := h.Verifier.MarkResent(ctx, sessionID); err != nil {
		return err
	}
	return h.Verifier.MarkResentByEmail(ctx, email)
}

// unverifiedLoginError starts a fresh verification session for a user who logged in
// with valid credentials but has not verified their email, and returns the error
// the login endpoint should respond with.
func (h *AuthHandler) unverifiedLoginError(
	ctx context.Context,
	userID uuid.UUID,
	email string,
	reg region.Region,
) error {
	retryAfter, err := h.Verifier.RetryAfterByEmail(ctx, email)
	if err != nil {
		return apperror.ErrInternal
	}
	if retryAfter > 0 {
		// The caller has proven the password, so hand back the live session
		// so the client can show the code page and count down to a resend.
		extra := map[string]any{"email": email}
		if id, idErr := h.Verifier.LatestSessionID(ctx, auth.PurposeEmailVerify, email); idErr == nil && id != "" {
			extra["verification_session_id"] = id
		}
		return verificationRateLimited(retryAfter, extra)
	}

	data, err := h.beginVerification(ctx, userID, email, string(reg))
	if err != nil {
		return apperror.ErrInternal
	}
	if err := h.Verifier.MarkResentByEmail(ctx, email); err != nil {
		return apperror.ErrInternal
	}

	return apperror.NewWithDetails(
		apperror.CodeEmailNotVerified,
		apperror.MsgEmailNotVerified,
		verificationDetails(data),
	)
}

// refresh godoc
//
//	@Summary		Refresh (removed)
//	@Description	Removed: sessions are server-side with a sliding idle timeout, so there is nothing to refresh. Always returns 410 SESSION_REFRESH_REMOVED. Call GET /auth/me to check the session.
//	@Tags			auth
//	@Produce		json
//	@Failure		410	{object}	apidoc.ErrorEnvelope
//	@Router			/auth/refresh [post]
func (h *AuthHandler) refresh(w http.ResponseWriter, r *http.Request) {
	response.Error(w, r, apperror.New(apperror.CodeSessionRefreshRemoved,
		"token refresh was removed; sessions are cookie based with a sliding idle timeout"))
}

func (h *AuthHandler) beginVerification(
	ctx context.Context,
	userID uuid.UUID,
	email, reg string,
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
		Purpose:   auth.PurposeEmailVerify,
	}

	if err := h.Verifier.CreateSession(ctx, session); err != nil {
		return AuthSignupData{}, err
	}
	if err := h.Verifier.ReplaceLatestSession(ctx, auth.PurposeEmailVerify, email, sessionID); err != nil {
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

// verificationRateLimited builds the 429 returned when a code was requested too
// recently. details.retry_after_seconds (also sent as the Retry-After header)
// is the time left until another code may be requested.
// wrongCodeError maps a wrong-code result of the verification store to the API error: 400 invalid_verification_code
// with details.attempts_remaining, or, once the attempt budget is spent, 429 too_many_attempts with
// details.retry_after_seconds (and the Retry-After header). A fresh code (resend) resets the budget.
func wrongCodeError(err error, message string) error {
	var ae *auth.VerifyAttemptsError
	if !errors.As(err, &ae) {
		return apperror.New(apperror.CodeInvalidVerificationCode, message)
	}
	if ae.Locked {
		return apperror.RateLimited(apperror.CodeTooManyAttempts, apperror.MsgTooManyAttempts, ae.RetryAfter,
			map[string]any{"attempts_remaining": 0})
	}
	return apperror.NewWithDetails(apperror.CodeInvalidVerificationCode, message,
		map[string]any{"attempts_remaining": ae.Remaining})
}

func verificationRateLimited(retryAfter int, extra map[string]any) error {
	return apperror.RateLimited(
		apperror.CodeVerificationRateLimited, apperror.MsgVerificationRateLimited, retryAfter, extra,
	)
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

	_, enqueueErr := h.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type:             mailer.TypeEmailVerification,
		Recipient:        email,
		Code:             code,
		ExpiresInSeconds: auth.CodeExpiresInSeconds(),
	}, queue.EmailEnqueueOptions()...)
	besteffort.Log(ctx, "Enqueuer.EnqueueTx", enqueueErr)
	besteffort.Log(ctx, "tx.Commit", tx.Commit(ctx))
}

func (h *AuthHandler) loadUserByEmail(ctx context.Context, reg region.Region, email string) (accountUser, error) {
	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		return accountUser{}, err
	}

	sql, args, err := lookupUserByEmailSQL(reg, email)
	if err != nil {
		return accountUser{}, err
	}

	return scanAccountUser(pool.QueryRow(ctx, sql, args...))
}

func (h *AuthHandler) loadUserByID(ctx context.Context, reg region.Region, userID uuid.UUID) (accountUser, error) {
	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		return accountUser{}, err
	}

	sql, args, err := lookupUserByIDSQL(reg, userID)
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
	if scanErr := h.GlobalDB.QueryRow(ctx, sql, args...).
		Scan(&registryID, &registryEmail, &reg, &userID); scanErr != nil {
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return region.RegionUnknown, uuid.Nil, scanErr
		}
		// No registry row at all: this can only be an unverified signup
		// (verifyEmail is what creates the registry row), so it's still
		// sitting in the global holding area.
		user, loadUserByEmailErr := h.loadUserByEmail(ctx, region.RegionGlobal, email)
		if loadUserByEmailErr != nil {
			return region.RegionUnknown, uuid.Nil, loadUserByEmailErr
		}
		return region.RegionGlobal, user.ID, nil
	}

	if userID == nil {
		return region.RegionUnknown, uuid.Nil, pgx.ErrNoRows
	}

	homeReg, err := h.reconcileUserRegion(ctx, registryEmail, *userID, region.Region(reg))
	if err != nil {
		return region.RegionUnknown, uuid.Nil, err
	}

	return homeReg, *userID, nil
}

func (h *AuthHandler) reconcileUserRegion(
	ctx context.Context,
	email string,
	userID uuid.UUID,
	registryRegion region.Region,
) (region.Region, error) {
	if _, err := h.loadUserByID(ctx, registryRegion, userID); err == nil {
		return registryRegion, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return region.RegionUnknown, err
	}

	homeReg, err := h.findUserHomeRegion(ctx, userID)
	if err != nil {
		return region.RegionUnknown, err
	}

	if homeReg != registryRegion {
		if err := h.correctRegistryRegion(ctx, email, homeReg); err != nil {
			return region.RegionUnknown, err
		}
	}

	return homeReg, nil
}

// findUserHomeRegion looks for userID's row in every real regional database,
// and finally in the global holding area, returning whichever one actually
// has it. Used to self-heal a users_registry row that points at the wrong
// place.
func (h *AuthHandler) findUserHomeRegion(ctx context.Context, userID uuid.UUID) (region.Region, error) {
	sql, args, err := query.LookupAccountUserByID(userID)
	if err != nil {
		return region.RegionUnknown, err
	}

	var lastErr error
	for _, reg := range region.All() {
		pool, dBErr := h.Router.DB(reg)
		if dBErr != nil {
			lastErr = dBErr
			continue
		}

		var id uuid.UUID
		dBErr = pool.QueryRow(ctx, sql, args...).Scan(
			&id, new(string), new(string), new(*time.Time), new(*time.Time),
			new(*string), new(*string), new(*string), new(*string), new(*uuid.UUID),
			new(*string), new(*time.Time),
		)
		if errors.Is(dBErr, pgx.ErrNoRows) {
			lastErr = dBErr
			continue
		}
		if dBErr != nil {
			return region.RegionUnknown, dBErr
		}

		return reg, nil
	}

	pendingSQL, pendingArgs, err := query.LookupPendingUserByID(userID)
	if err != nil {
		return region.RegionUnknown, err
	}
	var id uuid.UUID
	err = h.GlobalDB.QueryRow(ctx, pendingSQL, pendingArgs...).Scan(
		&id, new(string), new(string), new(*time.Time), new(*time.Time),
		new(*string), new(*string), new(*string), new(*string), new(*uuid.UUID),
		new(*string), new(*time.Time),
	)
	if err == nil {
		return region.RegionGlobal, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return region.RegionUnknown, err
	}

	if lastErr != nil {
		return region.RegionUnknown, lastErr
	}

	return region.RegionUnknown, pgx.ErrNoRows
}

func (h *AuthHandler) correctRegistryRegion(ctx context.Context, email string, homeReg region.Region) error {
	source := regionSourceExplicit
	if homeReg == region.RegionGlobal {
		source = regionSourcePending
	}
	sql, args, err := query.UpdateUsersRegistryRegion(email, string(homeReg), source)
	if err != nil {
		return err
	}

	_, err = h.GlobalDB.Exec(ctx, sql, args...)
	return err
}

func (h *AuthHandler) lookupRegistryByUserID(ctx context.Context, userID uuid.UUID) (region.Region, string, error) {
	const sql = `SELECT email, region FROM users_registry WHERE user_id = $1`
	var email, reg string
	if err := h.GlobalDB.QueryRow(ctx, sql, userID).Scan(&email, &reg); err != nil {
		return region.RegionUnknown, "", err
	}

	return region.Region(reg), email, nil
}

func (h *AuthHandler) loadOnboardingSummary(
	ctx context.Context,
	reg region.Region,
	user accountUser,
) (OnboardingProgressSummary, error) {
	if user.OnboardingCompletedAt != nil {
		status := onboarding.StatusCompleted
		return OnboardingProgressSummary{
			Status:         status,
			CompletedSteps: onboarding.RequiredSteps(stringValue(user.AccountType)),
		}, nil
	}

	if rd := h.OrgReads.onboarding(); rd != nil && reg != region.RegionGlobal {
		step, completed, found, rerr := rd.Progress(ctx, user.ID.String(), reg)
		if rerr != nil {
			besteffort.Log(ctx, "org onboarding progress", rerr)
			return OnboardingProgressSummary{}, errUnavailable
		}
		if !found {
			step, completed = onboarding.InitialProgress()
		}
		return onboardingSummary(user.OnboardingCompletedAt, step, completed, user.AccountType), nil
	}

	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		return OnboardingProgressSummary{}, err
	}

	progressSQL, progressArgs, err := lookupOnboardingProgressSQL(reg, user.ID)
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

// respondAuthTokens starts a session for a user who has passed password/OTP login and has no TOTP.
// (The name is historical; no tokens are returned, the session rides in a cookie.)
func (h *AuthHandler) respondAuthTokens(
	w http.ResponseWriter,
	r *http.Request,
	ctx context.Context,
	reg region.Region,
	user accountUser,
) {
	h.respondSession(w, r, ctx, reg, user, false)
}

// respondSession starts a fresh session (rotating any presented one) and writes the session response.
func (h *AuthHandler) respondSession(
	w http.ResponseWriter,
	r *http.Request,
	ctx context.Context,
	reg region.Region,
	user accountUser,
	mfa bool,
) {
	summary, err := h.loadOnboardingSummary(ctx, reg, user)
	if err != nil {
		response.Error(w, r, summaryError(err))
		return
	}

	ident, err := h.Sessions.Start(w, r, user.ID, string(reg), mfa)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, AuthSessionData{
		Onboarding: &summary,
		User: &AuthSessionUser{
			ID: user.ID.String(), Email: user.Email, EmailVerified: user.EmailVerifiedAt != nil,
			AccountType: user.AccountType, Region: string(reg),
		},
		CSRFToken: ident.CSRFToken,
	})
}

func (h *AuthHandler) respondMFARequired(w http.ResponseWriter, r *http.Request, userID uuid.UUID, reg region.Region) {
	if err := h.Sessions.StartPreAuth(w, r, userID, string(reg)); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	response.Success(w, r, http.StatusOK, AuthMFARequiredData{
		TOTPRequired: true,
		ExpiresIn:    int(session.PreAuthTTL.Seconds()),
	})
}

func (h *AuthHandler) userTOTPEnabled(user accountUser) bool {
	return user.TOTPEnabledAt != nil && user.TOTPSecret != nil && *user.TOTPSecret != ""
}

func onboardingSummary(
	completedAt *time.Time,
	currentStep string,
	completed []string,
	accountType *string,
) OnboardingProgressSummary {
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
	n := binary.BigEndian.Uint32(b[:]) % otpModulus
	return fmt.Sprintf("%06d", n), nil
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func rollbackOnError(ctx context.Context, tx pgx.Tx) {
	besteffort.Log(ctx, "tx.Rollback", tx.Rollback(ctx))
}

// insertPendingUser stores the new unverified account. With IDENTITY_EVENTS=on the insert and the
// identity.user.registered.v1 event share one global transaction; otherwise it is the single statement it always was.
func (h *AuthHandler) insertPendingUser(ctx context.Context, sql string, args []any, userID *uuid.UUID) error {
	if !identityevents.Enabled() {
		return h.GlobalDB.QueryRow(ctx, sql, args...).Scan(userID)
	}
	tx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnError(ctx, tx)
	if err = tx.QueryRow(ctx, sql, args...).Scan(userID); err != nil {
		return err
	}
	if err = identityevents.EnqueueRegistered(ctx, tx, identityevents.UserRegistered{
		UserID: userID.String(), Region: string(region.RegionGlobal), RegisteredAt: time.Now(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// summaryError keeps org's unavailability visible as 503; anything else is an internal error.
func summaryError(err error) error {
	if errors.Is(err, errUnavailable) {
		return errUnavailable
	}
	return apperror.ErrInternal
}
