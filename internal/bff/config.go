// Package bff is the shared edge (backend-for-frontend) toolkit: reverse proxy, Redis rate limiting, CORS,
// security headers, request ids, health and metrics. cmd/web-bff and a future cmd/admin-bff differ only in the
// Defaults passed to LoadConfig.
package bff

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults are per-BFF default values (web-bff vs admin-bff).
type Defaults struct {
	Name       string // SERVICE_NAME
	ListenAddr string
	Upstream   string
}

// Config is the full BFF configuration. Every field comes from env (or NAME_FILE) via LoadConfig.
type Config struct {
	Name          string
	ListenAddr    string // LISTEN_ADDR
	OpsListenAddr string // OPS_LISTEN_ADDR: serves /metrics; empty disables
	// UpstreamURL (UPSTREAM_API_URL) is the optional catch-all upstream. Empty (the monolith is retired): a path no
	// service route claims gets a 404.
	UpstreamURL  string
	PublicAPIURL string // PUBLIC_API_URL: the origin the API docs advertise (web BFF only)
	// ApplicationsURL, when set (UPSTREAM_APPLICATIONS_URL), sends the applications routes (ApplicationsPath) to the
	// applications service instead of the monolith.
	ApplicationsURL string
	// HiringURL, when set (UPSTREAM_HIRING_URL), sends the hiring routes (HiringPath) to the hiring service.
	HiringURL string
	// OrgURL, when set (UPSTREAM_ORG_URL), sends the org routes (OrgPath) to the org service instead of the monolith.
	OrgURL string
	// IdentityURL, when set (UPSTREAM_IDENTITY_URL), sends the identity routes (IdentityPath) to the identity
	// service instead of the monolith.
	IdentityURL string
	// SessionValidation (SESSION_VALIDATION=on) makes the edge validate the session cookie against the same Redis
	// store the identity service writes, refuse protected paths without one and forward X-Openhr-* headers.
	SessionValidation  bool
	CookieSecure       bool          // COOKIE_SECURE (default true): selects the __Host- cookie names
	SessionIdleTTL     time.Duration // SESSION_IDLE_TTL seconds (default 7200)
	SessionAbsoluteTTL time.Duration // SESSION_ABSOLUTE_TTL seconds (default 43200)
	APIPrefix          string        // API_PREFIX, stripped before matching rate-limit classes (default /api/v1)
	AppEnv             string
	LogLevel           string
	RedisURL           string // REDIS_URL (redis://...)
	TrustedProxies     []netip.Prefix
	AllowedOrigins     []string
	HSTS               bool // HSTS_ENABLED: always send HSTS (otherwise only when the request arrived over TLS)
	MaxBodyBytes       int64
	UpstreamTimeout    time.Duration // upstream response-header timeout
	ReadHeaderTO       time.Duration
	IdleTimeout        time.Duration
	ShutdownTimeout    time.Duration
	GlobalLimit        Rule
	Classes            map[string]ClassConfig
	FailClosedAuth     bool // RATE_LIMIT_FAIL_CLOSED_AUTH (default true)
	FailClosedOthers   bool // RATE_LIMIT_FAIL_CLOSED_OTHER (default false)
	RequireOrigin      bool // ORIGIN_REQUIRED: reject unsafe browser-less requests with no Origin header
}

// Rule is "max requests per window". Max 0 disables the rule.
type Rule struct {
	Max    int
	Window time.Duration
}

// ClassConfig is the limit for one abuse-path class: per IP, and (for credential endpoints) per email.
type ClassConfig struct {
	IP    Rule
	Email Rule // zero Max disables email keying
}

// DefaultClasses returns the built-in route classes. Override with RATE_LIMIT_<CLASS>=max/window and
// RATE_LIMIT_<CLASS>_EMAIL=max/window, e.g. RATE_LIMIT_LOGIN=20/5m.
func DefaultClasses() map[string]ClassConfig {
	m := time.Minute
	return map[string]ClassConfig{
		"LOGIN":               {IP: Rule{20, 5 * m}, Email: Rule{10, 15 * m}},
		"LOGIN_OTP_REQUEST":   {IP: Rule{10, 15 * m}, Email: Rule{5, 15 * m}},
		"LOGIN_OTP_VERIFY":    {IP: Rule{20, 15 * m}, Email: Rule{10, 15 * m}},
		"LOGIN_TOTP":          {IP: Rule{20, 5 * m}, Email: Rule{10, 15 * m}},
		"SIGNUP":              {IP: Rule{10, 60 * m}, Email: Rule{3, 60 * m}},
		"VERIFY_EMAIL":        {IP: Rule{20, 15 * m}, Email: Rule{10, 15 * m}},
		"RESEND_VERIFICATION": {IP: Rule{5, 15 * m}, Email: Rule{3, 15 * m}},
		"FORGOT_PASSWORD":     {IP: Rule{5, 15 * m}, Email: Rule{3, 15 * m}},
		"RESET_PASSWORD":      {IP: Rule{10, 15 * m}},
		"PUBLIC_APPLY":        {IP: Rule{10, 60 * m}},
		"PUBLIC_OFFER":        {IP: Rule{30, 60 * m}},
	}
}

// LoadConfig reads the environment. getenv is injectable for tests (nil means os.Getenv).
func LoadConfig(d Defaults, getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	var errs []string
	get := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		if f := getenv(k + "_FILE"); f != "" {
			b, err := os.ReadFile(f) // #nosec G304 -- operator-supplied secret file path
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s_FILE: %v", k, err))
				return def
			}
			return strings.TrimSpace(string(b))
		}
		return def
	}
	dur := func(k string, def time.Duration) time.Duration {
		v := get(k, "")
		if v == "" {
			return def
		}
		x, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, k+": "+err.Error())
			return def
		}
		return x
	}
	boolean := func(k string, def bool) bool {
		v := get(k, "")
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, k+": "+err.Error())
			return def
		}
		return b
	}
	rule := func(k string, def Rule) Rule {
		v := get(k, "")
		if v == "" {
			return def
		}
		r, err := ParseRule(v)
		if err != nil {
			errs = append(errs, k+": "+err.Error())
			return def
		}
		return r
	}

	c := Config{
		Name:               get("SERVICE_NAME", d.Name),
		ListenAddr:         get("LISTEN_ADDR", d.ListenAddr),
		OpsListenAddr:      get("OPS_LISTEN_ADDR", ""),
		UpstreamURL:        get("UPSTREAM_API_URL", d.Upstream),
		PublicAPIURL:       get("PUBLIC_API_URL", ""),
		ApplicationsURL:    get("UPSTREAM_APPLICATIONS_URL", ""),
		HiringURL:          get("UPSTREAM_HIRING_URL", ""),
		OrgURL:             get("UPSTREAM_ORG_URL", ""),
		IdentityURL:        get("UPSTREAM_IDENTITY_URL", ""),
		SessionValidation:  strings.EqualFold(get("SESSION_VALIDATION", ""), "on"),
		CookieSecure:       boolean("COOKIE_SECURE", true),
		SessionIdleTTL:     time.Duration(secs(get("SESSION_IDLE_TTL", ""), 7200)) * time.Second,
		SessionAbsoluteTTL: time.Duration(secs(get("SESSION_ABSOLUTE_TTL", ""), 43200)) * time.Second,
		APIPrefix:          strings.TrimRight(get("API_PREFIX", "/api/v1"), "/"),
		AppEnv:             get("APP_ENV", "development"),
		LogLevel:           get("LOG_LEVEL", ""),
		RedisURL:           get("REDIS_URL", ""),
		AllowedOrigins:     splitList(get("ALLOWED_ORIGINS", "")),
		HSTS:               boolean("HSTS_ENABLED", false),
		RequireOrigin:      boolean("ORIGIN_REQUIRED", false),
		UpstreamTimeout:    dur("UPSTREAM_TIMEOUT", 30*time.Second),
		ReadHeaderTO:       dur("READ_HEADER_TIMEOUT", 10*time.Second),
		IdleTimeout:        dur("IDLE_TIMEOUT", 90*time.Second),
		ShutdownTimeout:    dur("SHUTDOWN_TIMEOUT", 20*time.Second),
		GlobalLimit:        rule("RATE_LIMIT_GLOBAL", Rule{300, time.Minute}),
		FailClosedAuth:     boolean("RATE_LIMIT_FAIL_CLOSED_AUTH", true),
		FailClosedOthers:   boolean("RATE_LIMIT_FAIL_CLOSED_OTHER", false),
		Classes:            DefaultClasses(),
	}
	n, err := strconv.ParseInt(get("MAX_BODY_BYTES", "10485760"), 10, 64)
	if err != nil || n <= 0 {
		errs = append(errs, "MAX_BODY_BYTES: must be a positive integer")
		n = 10 << 20
	}
	c.MaxBodyBytes = n

	for _, cidr := range splitList(get("TRUSTED_PROXY_CIDRS", "")) {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			a, aerr := netip.ParseAddr(cidr)
			if aerr != nil {
				errs = append(errs, "TRUSTED_PROXY_CIDRS: "+err.Error())
				continue
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		c.TrustedProxies = append(c.TrustedProxies, p)
	}
	for name, cc := range c.Classes {
		cc.IP = rule("RATE_LIMIT_"+name, cc.IP)
		cc.Email = rule("RATE_LIMIT_"+name+"_EMAIL", cc.Email)
		c.Classes[name] = cc
	}
	if c.UpstreamURL == "" && c.ApplicationsURL == "" && c.HiringURL == "" && c.OrgURL == "" && c.IdentityURL == "" {
		errs = append(errs, "at least one upstream is required (UPSTREAM_IDENTITY_URL, UPSTREAM_ORG_URL, UPSTREAM_HIRING_URL, UPSTREAM_APPLICATIONS_URL or UPSTREAM_API_URL)")
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("invalid config: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

// ParseRule parses "20/5m" (max/window).
func ParseRule(s string) (Rule, error) {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return Rule{}, fmt.Errorf("rule %q must look like 20/5m", s)
	}
	n, err := strconv.Atoi(strings.TrimSpace(a))
	if err != nil || n < 0 {
		return Rule{}, fmt.Errorf("rule %q: bad max", s)
	}
	w, err := time.ParseDuration(strings.TrimSpace(b))
	if err != nil || w <= 0 {
		return Rule{}, fmt.Errorf("rule %q: bad window", s)
	}
	return Rule{Max: n, Window: w}, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func secs(v string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return n
	}
	return def
}
