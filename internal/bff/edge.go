package bff

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Angle-HR/server/pkg/apperror"
)

// RequestIDHeader is propagated to the upstream and returned to the client.
const RequestIDHeader = "X-Request-Id"

// RequestID keeps a sane inbound X-Request-Id (<=128 chars of [A-Za-z0-9._-]) or generates one, stores it in the
// context the way chi's middleware does (so pkg/response and pkg/logger see it) and echoes it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			var b [12]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		r.Header.Set(RequestIDHeader, id)
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.RequestIDKey, id)))
	})
}

func validRequestID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// securityHeaders are set by the BFF and stripped from upstream responses so they never duplicate.
var securityHeaders = []string{
	"Strict-Transport-Security", "X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy",
	"Referrer-Policy", "Permissions-Policy", "Cross-Origin-Resource-Policy",
}

// SecurityHeaders sets API-appropriate hardening headers. HSTS is sent when always is set, or the request
// reached us over TLS (directly, or via a trusted proxy reporting X-Forwarded-Proto: https).
func SecurityHeaders(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
			h.Set("Cross-Origin-Resource-Policy", "same-site")
			if cfg.HSTS || r.TLS != nil ||
				(PeerTrusted(r, cfg.TrustedProxies) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")) {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" {
			continue // wildcard is never valid with credentials; ignore it
		}
		if strings.EqualFold(strings.TrimRight(a, "/"), origin) {
			return true
		}
	}
	return false
}

// If-Match carries a job's revision on every save; the browser blocks the request without it here.
const corsAllowHeaders = "Accept, Content-Type, If-Match, Authorization, X-CSRF-Token, X-Request-Id, Idempotency-Key"

// CORS implements the allow-list with credentials, answers preflights itself and rejects unsafe methods whose
// Origin header is not allow-listed (a CSRF defence in depth next to the CSRF token). Requests without an
// Origin header (curl, server-to-server) pass unless cfg.RequireOrigin.
func CORS(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			h := w.Header()
			h.Add("Vary", "Origin")
			ok := origin != "" && originAllowed(origin, cfg.AllowedOrigins)
			if ok {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Expose-Headers", "X-Request-Id, Retry-After")
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if !ok {
					writeError(w, r, http.StatusForbidden, apperror.CodeForbidden, "origin not allowed", 0)
					return
				}
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if !isSafeMethod(r.Method) {
				switch {
				case origin == "" && cfg.RequireOrigin:
					writeError(w, r, http.StatusForbidden, apperror.CodeForbidden, "origin required", 0)
					return
				case origin != "" && !ok:
					writeError(w, r, http.StatusForbidden, apperror.CodeForbidden, "origin not allowed", 0)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}
