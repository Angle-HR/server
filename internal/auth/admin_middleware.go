package auth

import (
	"context"
	"net/http"

	"github.com/google/uuid"

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

// AdminMiddleware validates admin JWTs and loads permissions from the store.
type AdminMiddleware struct {
	Tokens *TokenService
	Store  AdminUserStore
}

// NewAdminMiddleware returns admin auth middleware.
func NewAdminMiddleware(tokens *TokenService, store AdminUserStore) *AdminMiddleware {
	return &AdminMiddleware{Tokens: tokens, Store: store}
}

// RequireAdmin rejects requests without a valid admin access token and active user.
func (m *AdminMiddleware) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m == nil || m.Tokens == nil || m.Store == nil {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		claims, err := m.Tokens.ParseAdminAccess(token)
		if err != nil {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		userID, err := AdminUserIDFromAccess(claims)
		if err != nil {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

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

		ctx := WithAdmin(r.Context(), user.ID, user.Email, perms)
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
