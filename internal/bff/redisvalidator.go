package bff

import (
	"errors"
	"net/http"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/session"
)

// RedisValidator is the real SessionValidator: it checks the session cookie against the Redis session store the
// identity service writes (same namespaces and cookie names, internal/session), so the edge can refuse protected
// requests and forward the caller as X-Openhr-* headers. Product and admin sessions are separate: /admin paths use
// the admin cookie and namespace, everything else the product one.
//
// Policy (default deny): a path is public only if listed in publicPath; every other path needs a full session.
// The upstream still authenticates and checks CSRF itself, so this is an additional layer, not a replacement.
type RedisValidator struct {
	Product *session.Manager
	Admin   *session.Manager
	Prefix  string // API prefix, stripped before classifying
}

// NewRedisValidator builds the validator over the same Redis as the identity service.
func NewRedisValidator(rdb goredis.Cmdable, cfg Config) *RedisValidator {
	mc := session.ManagerConfig{IdleTTL: cfg.SessionIdleTTL, AbsoluteTTL: cfg.SessionAbsoluteTTL, Secure: cfg.CookieSecure}
	mc.Kind = session.KindProduct
	p := session.NewManager(rdb, mc)
	mc.Kind = session.KindAdmin
	return &RedisValidator{Product: p, Admin: session.NewManager(rdb, mc), Prefix: cfg.APIPrefix}
}

// publicPath reports whether the path (API prefix removed) needs no session.
func publicPath(p string, admin bool) bool {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if admin {
		// /admin/auth/{login,refresh,logout,accept-invite} and /admin/auth/invite/{token}
		if len(parts) >= 3 && parts[1] == "auth" {
			switch parts[2] {
			case "login", "refresh", "logout", "accept-invite", "invite":
				return true
			}
		}
		return false
	}
	// The interactive API docs (the API host root and /api/docs) and their spec, served by the monolith outside
	// production (see internal/docs).
	if p == "/" || p == "/api/docs" || p == "/api/openapi.json" {
		return true
	}
	switch parts[0] {
	case "auth":
		if len(parts) >= 2 {
			return parts[1] != "me" && parts[1] != "totp"
		}
	case "countries", "public", "j": // "j" is the public careers pages (/j/{public_id}, /j/{public_id}/privacy)
		return true
	case "onboarding":
		if len(parts) == 2 {
			switch parts[1] {
			case "business-types", "industries", "company-roles", "identification-requirements":
				return true
			}
		}
	}
	return false
}

// Validate implements SessionValidator.
func (v *RedisValidator) Validate(r *http.Request) (*Identity, error) {
	p := strings.TrimPrefix(r.URL.Path, v.Prefix)
	if r.Method == http.MethodOptions {
		return nil, nil
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	admin := parts[0] == "admin"
	mgr := v.Product
	if admin {
		mgr = v.Admin
	}
	if publicPath(p, admin) {
		return nil, nil
	}
	ident, err := mgr.Authenticate(r)
	if err != nil {
		if errors.Is(err, session.ErrUnavailable) {
			return nil, err
		}
		return nil, ErrUnauthenticated
	}
	id := &Identity{Subject: ident.UserID.String(), Region: ident.Region, Actor: "user"}
	if admin {
		id.Actor, id.Roles = "admin", []string{"admin"}
	}
	return id, nil
}
