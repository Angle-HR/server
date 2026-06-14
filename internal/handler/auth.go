package handler

import (
	"github.com/go-chi/chi/v5"
)

// RegisterRoutes mounts auth routes on r.
func (h *AuthHandler) RegisterRoutes(r chi.Router) {
	r.Post("/signup", h.signup)
	r.Patch("/signup", h.patchSignup)
	r.Post("/verify-email", h.verifyEmail)
	r.Post("/resend-verification", h.resendVerification)
	r.Post("/login", h.login)
	r.Post("/refresh", h.refresh)
}
