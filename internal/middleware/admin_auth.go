package middleware

import (
	"net/http"
	"strings"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

const adminScope = "admin"

// RequireAdmin is a placeholder admin auth middleware.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			response.Error(w, r, apperror.New(apperror.CodeUnauthorized, apperror.MsgMissingAuthorizationHeader))
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			response.Error(w, r, apperror.New(apperror.CodeUnauthorized, apperror.MsgInvalidAuthorizationHeader))
			return
		}

		token := strings.TrimSpace(parts[1])
		if !strings.Contains(token, adminScope) {
			response.Error(w, r, apperror.New(apperror.CodeForbidden, apperror.MsgAdminScopeRequired))
			return
		}

		next.ServeHTTP(w, r)
	})
}
