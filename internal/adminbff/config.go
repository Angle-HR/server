// Package adminbff is the admin console's edge: internal/bff configured for the /api/v1/admin path space, with
// the admin session cookie, its own rate-limit classes (login, TOTP, accept-invite) and a path allow-list so the
// product API is never reachable through it.
package adminbff

import (
	"os"
	"strconv"
	"time"

	"github.com/Angle-HR/server/internal/bff"
)

const (
	// AdminPrefix is the only path space the admin BFF forwards.
	AdminPrefix = "/api/v1/admin"

	cookieSecure = "__Host-openhr_admin_session"
	cookieLocal  = "openhr_admin_session"
)

// Config is bff.Config plus what is specific to the admin edge.
type Config struct {
	bff.Config
	// CookieName is the admin session cookie (SESSION_COOKIE_NAME overrides the COOKIE_SECURE-derived name).
	CookieName string
	// AdminClasses are the admin rate-limit classes (RATE_LIMIT_ADMIN_<CLASS>[_EMAIL]=max/window).
	AdminClasses map[string]bff.ClassConfig
}

// DefaultAdminClasses are stricter than the product ones: the admin console has a handful of users and
// accounts with real power.
func DefaultAdminClasses() map[string]bff.ClassConfig {
	m := time.Minute
	return map[string]bff.ClassConfig{
		"ADMIN_LOGIN":          {IP: bff.Rule{Max: 10, Window: 5 * m}, Email: bff.Rule{Max: 5, Window: 15 * m}},
		"ADMIN_LOGIN_TOTP":     {IP: bff.Rule{Max: 10, Window: 5 * m}},
		"ADMIN_ACCEPT_INVITE":  {IP: bff.Rule{Max: 10, Window: 15 * m}},
		"ADMIN_INVITE_PREVIEW": {IP: bff.Rule{Max: 30, Window: 15 * m}},
	}
}

// LoadConfig reads the environment with the admin defaults: listen on :8080 (the compose convention), API
// prefix /api/v1, admin cookie names. getenv is injectable for tests (nil means os.Getenv).
func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	base, err := bff.LoadConfig(bff.Defaults{Name: "admin-bff", ListenAddr: ":8080"}, getenv)
	if err != nil {
		return Config{Config: base}, err
	}
	c := Config{Config: base, AdminClasses: DefaultAdminClasses()}
	// The product classes do not apply to the admin path space (the paths differ), and the admin BFF never
	// forwards to the applications service.
	c.Classes = map[string]bff.ClassConfig{}
	c.ApplicationsURL = ""

	secure := true
	if v := getenv("COOKIE_SECURE"); v != "" {
		if b, perr := strconv.ParseBool(v); perr == nil {
			secure = b
		}
	}
	c.CookieName = CookieName(secure)
	if v := getenv("SESSION_COOKIE_NAME"); v != "" {
		c.CookieName = v
	}

	var errs []error
	for name, cc := range c.AdminClasses {
		var e1, e2 error
		cc.IP, e1 = ruleEnv(getenv, "RATE_LIMIT_"+name, cc.IP)
		cc.Email, e2 = ruleEnv(getenv, "RATE_LIMIT_"+name+"_EMAIL", cc.Email)
		errs = append(errs, e1, e2)
		c.AdminClasses[name] = cc
	}
	for _, e := range errs {
		if e != nil {
			return c, e
		}
	}
	return c, nil
}

// CookieName is the admin session cookie for the given COOKIE_SECURE mode (see docs/microservices/sessions.md).
func CookieName(secure bool) string {
	if secure {
		return cookieSecure
	}
	return cookieLocal
}

func ruleEnv(getenv func(string) string, key string, def bff.Rule) (bff.Rule, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	r, err := bff.ParseRule(v)
	if err != nil {
		return def, &envError{key, err}
	}
	return r, nil
}

type envError struct {
	key string
	err error
}

func (e *envError) Error() string { return "invalid config: " + e.key + ": " + e.err.Error() }
