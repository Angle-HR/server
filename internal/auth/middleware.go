package auth

import (
	"errors"
	"net/http"

	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// Middleware authenticates product requests from the server-side session cookie.
type Middleware struct {
	Sessions *session.Manager
}

// NewMiddleware returns session auth middleware.
func NewMiddleware(sessions *session.Manager) *Middleware {
	return &Middleware{Sessions: sessions}
}

// RequireAuth rejects requests without a valid session. Unsafe methods must also carry a matching X-CSRF-Token.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m == nil || m.Sessions == nil {
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

		reg := region.Region(ident.Region)
		if !region.Valid(reg) {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}

		if session.IsUnsafeMethod(r.Method) && !session.CheckCSRF(r, ident) {
			response.Error(w, r, apperror.New(apperror.CodeCSRFInvalid, "missing or invalid CSRF token"))
			return
		}

		ctx := WithSession(WithUser(r.Context(), ident.UserID, reg), ident)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
