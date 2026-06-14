package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
)

var _ = apidoc.ErrorEnvelope{}

// AuthHandler handles product auth endpoints (design-only stubs).
type AuthHandler struct{}

// NewAuthHandler returns an auth handler.
func NewAuthHandler() *AuthHandler {
	return &AuthHandler{}
}

// RegisterRoutes mounts auth routes on r.
func (h *AuthHandler) RegisterRoutes(r chi.Router) {
	r.Post("/signup", h.signup)
	r.Patch("/signup", h.patchSignup)
	r.Post("/verify-email", h.verifyEmail)
	r.Post("/resend-verification", h.resendVerification)
	r.Post("/login", h.login)
	r.Post("/refresh", h.refresh)
}

// signup godoc
//
//	@Summary		Product signup
//	@Description	Creates an unverified user and sends a 6-digit verification code by email. Not yet implemented.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthSignupRequest	true	"Signup payload"
//	@Success		201		{object}	handler.AuthSignupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/signup [post]
func (h *AuthHandler) signup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// patchSignup godoc
//
//	@Summary		Update signup email
//	@Description	Changes the email on an unverified signup and invalidates the prior OTP. Not yet implemented.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthSignupPatchRequest	true	"Email update payload"
//	@Success		200		{object}	handler.AuthSignupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/signup [patch]
func (h *AuthHandler) patchSignup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// verifyEmail godoc
//
//	@Summary		Verify email
//	@Description	Validates the 6-digit OTP and returns JWT tokens with onboarding status. Not yet implemented.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthVerifyEmailRequest	true	"Verification payload"
//	@Success		200		{object}	handler.AuthTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/verify-email [post]
func (h *AuthHandler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// resendVerification godoc
//
//	@Summary		Resend verification code
//	@Description	Sends a new OTP for an existing unverified signup. Rate-limited. Not yet implemented.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthResendVerificationRequest	true	"Resend payload"
//	@Success		200		{object}	handler.AuthSignupEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/resend-verification [post]
func (h *AuthHandler) resendVerification(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// login godoc
//
//	@Summary		Login
//	@Description	Authenticates a verified user and returns JWT tokens. Not yet implemented.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthLoginRequest	true	"Login payload"
//	@Success		200		{object}	handler.AuthTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/login [post]
func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// refresh godoc
//
//	@Summary		Refresh access token
//	@Description	Exchanges a valid refresh token for a new access token. Not yet implemented.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		handler.AuthRefreshRequest	true	"Refresh payload"
//	@Success		200		{object}	handler.AuthRefreshEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Router			/auth/refresh [post]
func (h *AuthHandler) refresh(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
