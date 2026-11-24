package bff

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP resolves the caller's address. X-Forwarded-For is honoured only when the direct peer is inside a
// trusted proxy prefix; the chain is then walked right to left and the first address that is not itself a
// trusted proxy is the client. Otherwise the socket peer is used and any XFF header is ignored.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := peerAddr(r)
	if !peer.IsValid() {
		return r.RemoteAddr
	}
	if !isTrusted(peer, trusted) {
		return peer.String()
	}
	hops := splitList(strings.Join(r.Header.Values("X-Forwarded-For"), ","))
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			return peer.String() // garbage in the chain: trust nothing to its left
		}
		a = a.Unmap()
		if !isTrusted(a, trusted) {
			return a.String()
		}
	}
	return peer.String()
}

// PeerTrusted reports whether the direct peer is a trusted proxy.
func PeerTrusted(r *http.Request, trusted []netip.Prefix) bool {
	p := peerAddr(r)
	return p.IsValid() && isTrusted(p, trusted)
}

func peerAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
