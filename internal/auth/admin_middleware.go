package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// AdminPrincipal is the subset of admin user fields needed by middleware.
type AdminPrincipal struct {
	ID       uuid.UUID
	Email    string
	IsActive bool
}

// AdminUserStore loads admin principals and permissions for middleware.
type AdminUserStore interface {
	GetAdminPrincipal(ctx context.Context, id uuid.UUID) (AdminPrincipal, error)
	ListAdminPermissions(ctx context.Context, userID uuid.UUID) ([]string, error)
}

// AdminMiddleware validates admin sessions and loads permissions from the store.
type AdminMiddleware struct {
	Sessions *session.Manager
	Store    AdminUserStore
	// RequireMFA (ADMIN_TOTP_REQUIRED) refuses every admin route except the TOTP enrolment ones to a session that
	// has not passed a second factor (session MFA flag), answering 403 TOTP_ENROLLMENT_REQUIRED.
	RequireMFA bool
}

// mfaExemptSuffixes are the admin routes a session without a second factor may still call: read the profile and
// run the enrolment (logout is public).
var mfaExemptSuffixes = []string{"/admin/auth/me", "/admin/auth/totp/enroll", "/admin/auth/totp/confirm"}

func mfaExempt(path string) bool {
	for _, s := range mfaExemptSuffixes {
		if strings.HasSuffix(strings.TrimSuffix(path, "/"), s) {
			return true
		}
	}
	return false
}

// NewAdminMiddleware returns admin auth middleware.
func NewAdminMiddleware(sessions *session.Manager, store AdminUserStore) *AdminMiddleware {
	return &AdminMiddleware{Sessions: sessions, Store: store}
}

// RequireAdmin rejects requests without a valid admin session and active user.
func (m *AdminMiddleware) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m == nil || m.Sessions == nil || m.Store == nil {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		ident, err := m.Sessions.Authenticate(r)
		if err != nil {
			if errors.Is(err, session.ErrUnavailable) {
				response.Error(w, r, apperror.ErrInternal)
				return
			}
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}
		if session.IsUnsafeMethod(r.Method) && !session.CheckCSRF(r, ident) {
			response.Error(w, r, apperror.New(apperror.CodeCSRFInvalid, "missing or invalid CSRF token"))
			return
		}
		if m.RequireMFA && !ident.MFA && !mfaExempt(r.URL.Path) {
			response.Error(w, r, apperror.New(apperror.CodeTOTPEnrollmentRequired,
				"two-factor authentication is required: enrol an authenticator first"))
			return
		}
		userID := ident.UserID

		user, err := m.Store.GetAdminPrincipal(r.Context(), userID)
		if err != nil || !user.IsActive {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		perms, err := m.Store.ListAdminPermissions(r.Context(), userID)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		ctx := WithSession(WithAdmin(r.Context(), user.ID, user.Email, perms), ident)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequirePermission rejects requests missing the given permission.
func (m *AdminMiddleware) RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !AdminHasPermission(r.Context(), perm) {
				response.Error(w, r, apperror.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
