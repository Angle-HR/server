package adminbff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Angle-HR/server/internal/bff"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// Class returns the admin rate-limit class of a request, or "".
func Class(method, path string) string {
	p := strings.TrimSuffix(strings.TrimPrefix(path, AdminPrefix), "/")
	switch {
	case method == http.MethodPost && p == "/auth/login":
		return "ADMIN_LOGIN"
	case method == http.MethodPost && p == "/auth/login/totp":
		return "ADMIN_LOGIN_TOTP"
	case method == http.MethodPost && p == "/auth/accept-invite":
		return "ADMIN_ACCEPT_INVITE"
	case method == http.MethodGet && strings.HasPrefix(p, "/auth/invite/"):
		return "ADMIN_INVITE_PREVIEW"
	}
	return ""
}

// IsPublic reports whether an admin path is served without a session (login, TOTP step, invite, logout).
func IsPublic(method, path string) bool {
	p := strings.TrimSuffix(strings.TrimPrefix(path, AdminPrefix), "/")
	switch {
	case method == http.MethodPost:
		switch p {
		case "/auth/login", "/auth/login/totp", "/auth/accept-invite", "/auth/logout", "/auth/refresh":
			return true
		}
	case method == http.MethodGet:
		return strings.HasPrefix(p, "/auth/invite/")
	}
	return false
}

func inAdminSpace(path string) bool {
	return path == AdminPrefix || strings.HasPrefix(path, AdminPrefix+"/")
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string, retry int) {
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response.Envelope{
		Error: &response.ErrorBody{Code: code, Message: msg},
		Meta:  &response.Meta{RequestID: middleware.GetReqID(r.Context())},
	})
}

// PathGuard answers 404 for everything outside the admin path space except the health endpoints, so the
// product API cannot be reached through the admin edge.
func PathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || inAdminSpace(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, r, http.StatusNotFound, apperror.CodeNotFound, "not found", 0)
	})
}

// RateLimit applies the admin classes. Auth classes fail closed on a Redis outage (cfg.FailClosedAuth).
func RateLimit(cfg Config, lim bff.Limiter, m *bff.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			class := Class(r.Method, r.URL.Path)
			cc, ok := cfg.AdminClasses[class]
			if class == "" || !ok {
				next.ServeHTTP(w, r)
				return
			}
			ip := bff.ClientIP(r, cfg.TrustedProxies)
			if cc.IP.Max > 0 && !allow(w, r, lim, m, class, "c:"+class+":ip:"+ip, cc.IP, cfg.FailClosedAuth) {
				return
			}
			if cc.Email.Max > 0 {
				if email := bff.PeekEmail(r); email != "" {
					sum := sha256.Sum256([]byte(email))
					if !allow(w, r, lim, m, class+"_email", "c:"+class+":em:"+hex.EncodeToString(sum[:16]), cc.Email, cfg.FailClosedAuth) {
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func allow(w http.ResponseWriter, r *http.Request, lim bff.Limiter, m *bff.Metrics, name, key string, rule bff.Rule, failClosed bool) bool {
	ok, retry, err := lim.Allow(r.Context(), key, rule)
	if err != nil {
		m.Inc("bff_ratelimit_errors_total", "class", name)
		if failClosed {
			writeError(w, r, http.StatusServiceUnavailable, apperror.CodeServiceUnavailable, "service temporarily unavailable, try again", 5)
			return false
		}
		return true
	}
	if !ok {
		m.Inc("bff_ratelimit_rejected_total", "class", name)
		secs := int((retry + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		writeError(w, r, http.StatusTooManyRequests, apperror.CodeTooManyRequests, "too many requests, try again later", secs)
		return false
	}
	return true
}

// CookieValidator is the edge's stateless session check: protected admin routes need the admin session cookie
// (the monolith stays the authority on whether it is valid). A missing cookie is a 401 without a hop upstream,
// which is also what keeps an expired-cookie browser flow cheap. A Redis-backed validator can replace it.
type CookieValidator struct{ Name string }

// Validate implements bff.SessionValidator.
func (v CookieValidator) Validate(r *http.Request) (*bff.Identity, error) {
	if r.Method == http.MethodOptions || IsPublic(r.Method, r.URL.Path) || !inAdminSpace(r.URL.Path) {
		return nil, nil
	}
	if c, err := r.Cookie(v.Name); err != nil || c.Value == "" {
		return nil, bff.ErrUnauthenticated
	}
	return nil, nil
}
