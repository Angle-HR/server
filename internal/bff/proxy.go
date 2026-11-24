package bff

import (
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Angle-HR/server/pkg/apperror"
)

// NewProxy builds the reverse proxy to the upstream. Paths are preserved, bodies and responses stream
// (FlushInterval -1, so SSE works), WebSocket upgrades are handled by httputil, hop-by-hop headers are removed
// by the stdlib, and Cookie / Set-Cookie / Authorization / X-CSRF-Token pass through untouched.
func NewProxy(cfg Config, upstream *url.URL, m *Metrics) http.Handler {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: cfg.UpstreamTimeout,
		ForceAttemptHTTP2:     true,
	}
	rp := &httputil.ReverseProxy{
		Transport:     tr,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			in, out := pr.In, pr.Out
			pr.SetURL(upstream) // joins upstream base path, rewrites Host to the upstream
			out.Host = upstream.Host

			// Rewrite starts with X-Forwarded-*/Forwarded stripped from Out; rebuild them.
			peer := peerAddr(in)
			peerStr := in.RemoteAddr
			if peer.IsValid() {
				peerStr = peer.String()
			}
			trusted := PeerTrusted(in, cfg.TrustedProxies)
			chain := peerStr
			if prior := strings.Join(in.Header.Values("X-Forwarded-For"), ", "); trusted && prior != "" {
				chain = prior + ", " + peerStr
			}
			out.Header.Set("X-Forwarded-For", chain)
			proto := "http"
			if in.TLS != nil {
				proto = "https"
			}
			if trusted {
				if p := in.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
					proto = p
				}
			}
			out.Header.Set("X-Forwarded-Proto", proto)
			host := in.Host
			if trusted {
				if h := in.Header.Get("X-Forwarded-Host"); h != "" {
					host = h
				}
			}
			out.Header.Set("X-Forwarded-Host", host)
			out.Header.Set(RequestIDHeader, middleware.GetReqID(in.Context()))
		},
		ModifyResponse: func(resp *http.Response) error {
			// The BFF owns CORS and hardening headers; drop whatever the monolith added so none are doubled.
			for k := range resp.Header {
				if strings.HasPrefix(k, "Access-Control-") {
					delete(resp.Header, k)
				}
			}
			for _, k := range securityHeaders {
				resp.Header.Del(k)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var mbe *http.MaxBytesError
			switch {
			case errors.As(err, &mbe):
				writeError(w, r, http.StatusRequestEntityTooLarge, apperror.CodePayloadTooLarge, "request body too large", 0)
			case errors.Is(err, r.Context().Err()) && r.Context().Err() != nil:
				// client went away; nothing useful to send
				w.WriteHeader(499)
			default:
				m.Inc("bff_upstream_errors_total")
				code := http.StatusBadGateway
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					code = http.StatusGatewayTimeout
				}
				writeError(w, r, code, apperror.CodeServiceUnavailable, "upstream unavailable", 0)
			}
		},
	}
	return rp
}

// LimitBody caps request bodies. Oversized bodies fail the read, which the proxy ErrorHandler maps to 413;
// a declared Content-Length over the cap is rejected immediately.
func LimitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				writeError(w, r, http.StatusRequestEntityTooLarge, apperror.CodePayloadTooLarge, "request body too large", 0)
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}
