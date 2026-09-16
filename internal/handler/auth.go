package handler

import (
	"github.com/go-chi/chi/v5"
)

// RegisterRoutes mounts public auth routes on r.
func (h *AuthHandler) RegisterRoutes(r chi.Router) {
	r.Post("/signup", h.signup)
	r.Patch("/signup", h.patchSignup)
	r.Post("/verify-email", h.verifyEmail)
	r.Post("/resend-verification", h.resendVerification)
	r.Post("/login", h.login)
	r.Post("/login/otp/request", h.loginOTPRequest)
	r.Post("/login/otp/verify", h.loginOTPVerify)
	r.Post("/login/totp", h.loginTOTP)
	r.Post("/refresh", h.refresh)
	r.Post("/logout", h.logout)
	r.Post("/forgot-password", h.forgotPassword)
	r.Post("/reset-password", h.resetPassword)
	r.Get("/invite/{token}", h.getInvite)
	r.Post("/accept-invite", h.acceptInvite)
}

// RegisterProtectedRoutes mounts authenticated product auth routes. Caller must apply RequireAuth.
func (h *AuthHandler) RegisterProtectedRoutes(r chi.Router) {
	r.Get("/auth/me", h.me)
	r.Post("/auth/totp/enroll", h.totpEnroll)
	r.Post("/auth/totp/confirm", h.totpConfirm)
	r.Post("/auth/totp/disable", h.totpDisable)
	r.Post("/organizations/invites", h.createOrgInvite)
}
