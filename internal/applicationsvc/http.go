package applicationsvc

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/logger"
)

const (
	corsMaxAgeSeconds   = 300
	maxRequestBodyBytes = 1 << 20 // 1 MB, as the monolith
)

// HTTPDeps is what the HTTP server is built from.
type HTTPDeps struct {
	Log *slog.Logger
	// CORSAllowedOrigins and AllowedOrigins are the monolith's CORS_ALLOWED_ORIGINS and ALLOWED_ORIGINS: the
	// first drives the CORS middleware, the second the Origin check on unsafe methods.
	CORSAllowedOrigins []string
	AllowedOrigins     []string
	// Ready returns an error while the service cannot serve (database down).
	Ready func(ctx context.Context) error
	// NATS reports the bus as "up", "down" or "off"; it never fails readiness (the bus is optional). Nil means off.
	NATS func() string
	// Stats returns the counters of the job read model's fallback use (see jobmodel.Stats); served on
	// GET /internal/jobmodel-stats, which the BFFs do not route. Nil leaves the route off.
	Stats func() map[string]int64
	// RequireAuth authenticates the company-side routes from the session cookie and CSRF token.
	RequireAuth func(http.Handler) http.Handler
	// Apps are the handler dependencies. Mount's RequireAuth is filled from RequireAuth here.
	Apps apphttp.Deps
}

// NewRouter builds the service's HTTP handler: /healthz, /readyz and the apphttp routes under /api/v1, with the
// middleware stack of the monolith (body limit, request id, real ip, logging, recovery, CORS, origin guard).
func NewRouter(d HTTPDeps) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req.Body = http.MaxBytesReader(w, req.Body, maxRequestBodyBytes)
			next.ServeHTTP(w, req)
		})
	})
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(logger.RequestLogger(d.Log))
	r.Use(chimiddleware.Recoverer)
	if len(d.CORSAllowedOrigins) > 0 {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins:   slices.Clone(d.CORSAllowedOrigins),
			AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Accept", "Content-Type", "If-Match", "Idempotency-Key", session.CSRFHeader},
			AllowCredentials: true,
			MaxAge:           corsMaxAgeSeconds,
		}))
	}

	live := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
	r.Get("/healthz", live)
	r.Head("/healthz", live)
	if d.Stats != nil {
		r.Get("/internal/jobmodel-stats", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(d.Stats())
		})
	}
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if d.Ready != nil {
			if err := d.Ready(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"unavailable","database":"down"}`))
				return
			}
		}
		nats := "off"
		if d.NATS != nil {
			nats = d.NATS()
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "database": "up", "nats": nats})
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Browsers must come from an allowed origin on unsafe methods (CSRF defence in depth).
		r.Use(session.OriginGuard(d.AllowedOrigins))
		deps := d.Apps
		deps.RequireAuth = d.RequireAuth
		apphttp.Mount(r, deps)
	})
	return r
}
