package bff

import "strings"

// isDevEnv reports whether env is a local development environment (the only place the defaults below are fine).
func isDevEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "", "development", "dev", "local", "test":
		return true
	default:
		return false
	}
}

// Warnings lists configuration that is safe locally but usually wrong behind a real edge. They are logged once at
// startup; none of them stops the service. See docs/microservices/hardening.md.
func (c Config) Warnings() []string {
	if isDevEnv(c.AppEnv) {
		return nil
	}
	var w []string
	if len(c.TrustedProxies) == 0 {
		w = append(w, "TRUSTED_PROXY_CIDRS is unset: the client IP is the socket peer, so behind a load balancer or "+
			"ingress every user shares one rate-limit bucket. Set it to the proxy CIDRs (never 0.0.0.0/0)")
	}
	if len(c.AllowedOrigins) == 0 {
		w = append(w, "ALLOWED_ORIGINS is unset: browser origins are not restricted")
	}
	if !c.RequireOrigin {
		w = append(w, "ORIGIN_REQUIRED=false: unsafe requests without an Origin header are accepted. Set it to true "+
			"when only browsers call this BFF (server-to-server callers must then send an allow-listed Origin)")
	}
	return w
}
