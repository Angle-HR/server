package bff

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Angle-HR/server/pkg/apperror"
)

// Identity is what the edge learned about the caller from a validated session. The next phase forwards it to
// services as gRPC metadata; until then it is forwarded to the monolith as X-Openhr-* headers (only when a
// validator produced one).
type Identity struct {
	Subject  string
	TenantID string
	Region   string
	Actor    string // "user" or "admin"
	Roles    []string
}

// ErrUnauthenticated makes the edge answer 401. Any other error is a 503 (validator backend unavailable).
var ErrUnauthenticated = errors.New("unauthenticated")

// SessionValidator inspects cookies/bearer credentials at the edge. Return (nil, nil) for "no opinion":
// the request continues untouched and the upstream stays the authority.
type SessionValidator interface {
	Validate(r *http.Request) (*Identity, error)
}

// NoopValidator passes everything through. It is the Phase 1 implementation.
type NoopValidator struct{}

// Validate implements SessionValidator.
func (NoopValidator) Validate(*http.Request) (*Identity, error) { return nil, nil }

type identityKey struct{}

// IdentityFrom returns the identity attached by the session middleware, if any.
func IdentityFrom(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey{}).(*Identity)
	return id
}

const identityHeaderPrefix = "X-Openhr-"

// Session runs v and attaches the identity. It always strips inbound X-Openhr-* headers so a client can never
// spoof an identity to the upstream.
func Session(v SessionValidator) func(http.Handler) http.Handler {
	if v == nil {
		v = NoopValidator{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k := range r.Header {
				if strings.HasPrefix(k, identityHeaderPrefix) {
					delete(r.Header, k)
				}
			}
			id, err := v.Validate(r)
			switch {
			case errors.Is(err, ErrUnauthenticated):
				writeError(w, r, http.StatusUnauthorized, apperror.CodeUnauthorized, "authentication required", 0)
				return
			case err != nil:
				writeError(w, r, http.StatusServiceUnavailable, apperror.CodeServiceUnavailable, "service temporarily unavailable, try again", 5)
				return
			case id != nil:
				r.Header.Set("X-Openhr-Subject", id.Subject)
				r.Header.Set("X-Openhr-Tenant", id.TenantID)
				r.Header.Set("X-Openhr-Region", id.Region)
				r.Header.Set("X-Openhr-Actor", id.Actor)
				r.Header.Set("X-Openhr-Roles", strings.Join(id.Roles, ","))
				r = r.WithContext(context.WithValue(r.Context(), identityKey{}, id))
			}
			next.ServeHTTP(w, r)
		})
	}
}
