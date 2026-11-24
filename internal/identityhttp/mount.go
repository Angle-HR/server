package identityhttp

import (
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/auth"
)

// EnvRemote, set to "on", stops the monolith serving the identity routes (/auth/*, /organizations/invites, and the
// identity part of /admin). They are then served by cmd/identity, and the edge (web-bff, UPSTREAM_IDENTITY_URL)
// sends them there. Off by default: the monolith serves everything.
const EnvRemote = "IDENTITY_REMOTE"

// Remote reports whether IDENTITY_REMOTE=on.
func Remote() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvRemote)), "on") }

// Deps are the handlers Mount serves. A nil Auth or Admin leaves those routes out; the monolith passes nil for both
// when IDENTITY_REMOTE=on.
type Deps struct {
	Auth  *AuthHandler
	Admin *AdminHandler
	// RequireAuth authenticates the product routes (session cookie and CSRF).
	RequireAuth func(http.Handler) http.Handler
	// AdminMW authenticates the admin routes and checks permissions.
	AdminMW *auth.AdminMiddleware
	// AdminExtra mounts more authenticated admin routes (the monolith still serves catalogs and jobs). It runs inside
	// the RequireAdmin group.
	AdminExtra func(r chi.Router)
}

// Mount registers the identity routes on r, which must be the /api/v1 router: /auth/* (public), the product
// protected routes (/auth/me, TOTP, /organizations/invites) and /admin.
func Mount(r chi.Router, d Deps) {
	if d.Auth != nil {
		r.Route("/auth", d.Auth.RegisterRoutes)
		r.Group(func(r chi.Router) {
			r.Use(d.RequireAuth)
			d.Auth.RegisterProtectedRoutes(r)
		})
	}
	if d.Admin != nil || d.AdminExtra != nil {
		r.Route("/admin", func(r chi.Router) {
			if d.Admin != nil {
				d.Admin.RegisterPublicRoutes(r)
			}
			r.Group(func(r chi.Router) {
				r.Use(d.AdminMW.RequireAdmin)
				if d.Admin != nil {
					d.Admin.RegisterProtectedRoutes(r, d.AdminMW)
				}
				if d.AdminExtra != nil {
					d.AdminExtra(r)
				}
			})
		})
	}
}
