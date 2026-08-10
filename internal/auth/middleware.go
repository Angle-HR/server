package auth

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// Middleware validates Bearer JWT access tokens.
type Middleware struct {
	Tokens *TokenService
}

// NewMiddleware returns JWT auth middleware.
func NewMiddleware(tokens *TokenService) *Middleware {
	return &Middleware{Tokens: tokens}
}

// RequireAuth rejects requests without a valid access token.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("auth check", "method", r.Method, "url", r.URL.Path)
		if m == nil || m.Tokens == nil {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		claims, err := m.Tokens.ParseAccess(token)
		if err != nil {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		if !claims.EmailVerified {
			response.Error(w, r, apperror.ErrForbidden)
			return
		}

		userID, err := UserIDFromAccess(claims)
		if err != nil {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		reg, err := RegionFromAccess(claims)
		if err != nil {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		ctx := WithUser(r.Context(), userID, reg)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts a JWT from Authorization.
// Accepts "Bearer <token>" or a bare token value.
func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" || strings.EqualFold(header, "Bearer") {
		return ""
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}

	// Bare JWT (no scheme prefix).
	if !strings.Contains(header, " ") {
		return header
	}

	return ""
}
