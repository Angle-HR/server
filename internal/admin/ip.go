package admin

import (
	"net"
	"net/netip"
	"strings"
)

// NormalizeIP reduces a request address to the bare IP stored in admin.audit_logs.ip: "192.168.65.1:64455" and
// "[2001:db8::1]:443" lose their port, "[::1]" its brackets, an IPv4-mapped IPv6 address becomes IPv4 and an IPv6
// zone is dropped. Which client address a request has is decided before this point (the RealIP middleware and, at the
// edge, the trusted-proxy aware bff.ClientIP set r.RemoteAddr); this only fixes the format. Input that is not an IP
// (empty, garbage) yields "" rather than a stored fragment.
func NormalizeIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	host := raw
	if h, _, err := net.SplitHostPort(raw); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	a, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	return a.WithZone("").Unmap().String()
}
