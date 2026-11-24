package bff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/docs"
	"github.com/Angle-HR/server/pkg/logger"
)

// Deps are the pluggable parts of a BFF. Zero values get sane defaults (except Limiter, which is required).
type Deps struct {
	Limiter   Limiter
	Validator SessionValidator
	Metrics   *Metrics
	Log       *slog.Logger
}

// NewHandler assembles the edge. Order: request id, metrics, logging, security headers, CORS/Origin,
// rate limit, session validation, body limit, proxy. /healthz and /readyz skip everything after the headers.
func NewHandler(cfg Config, d Deps) (http.Handler, error) {
	var up *url.URL
	if cfg.UpstreamURL != "" {
		var err error
		up, err = url.Parse(cfg.UpstreamURL)
		if err != nil || up.Scheme == "" || up.Host == "" {
			return nil, fmt.Errorf("UPSTREAM_API_URL %q is not an absolute URL", cfg.UpstreamURL)
		}
	}
	if d.Metrics == nil {
		d.Metrics = NewMetrics()
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Limiter == nil {
		return nil, errors.New("a Limiter is required")
	}

	// With no catch-all upstream, a path no service route claims is a 404.
	proxy := http.Handler(http.HandlerFunc(notFound))
	if up != nil {
		proxy = NewProxy(cfg, up, d.Metrics)
	}
	var routes []Route
	var proxies []http.Handler
	for _, u := range []struct {
		name, env, raw string
		match          func(string) bool
	}{
		{"applications", "UPSTREAM_APPLICATIONS_URL", cfg.ApplicationsURL, ApplicationsPath},
		{"hiring", "UPSTREAM_HIRING_URL", cfg.HiringURL, HiringPath},
		{"org", "UPSTREAM_ORG_URL", cfg.OrgURL, OrgPath},
		{"identity", "UPSTREAM_IDENTITY_URL", cfg.IdentityURL, IdentityPath},
	} {
		if u.raw == "" {
			continue
		}
		pu, perr := url.Parse(u.raw)
		if perr != nil || pu.Scheme == "" || pu.Host == "" {
			return nil, fmt.Errorf("%s %q is not an absolute URL", u.env, u.raw)
		}
		routes = append(routes, Route{Name: u.name, Target: pu, Match: u.match})
		proxies = append(proxies, NewProxy(cfg, pu, d.Metrics))
	}
	var ready []*url.URL
	if up != nil {
		ready = append(ready, up)
	}
	for _, rt := range routes {
		ready = append(ready, rt.Target)
	}
	if len(routes) > 0 {
		proxy = routed(cfg.APIPrefix, proxy, routes, proxies)
	}
	guarded := chainHandlers(proxy,
		RateLimit(cfg, d.Limiter, d.Metrics),
		Session(d.Validator),
		LimitBody(cfg.MaxBodyBytes),
	)

	r := chi.NewRouter()
	r.Use(RequestID, d.Metrics.Instrument, logger.RequestLogger(d.Log), SecurityHeaders(cfg))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/readyz", readyHandler(d.Limiter, ready, cfg.UpstreamTimeout))
	// The interactive API docs, outside production. They need no session and no upstream.
	if cfg.Name == "web-bff" && docs.IsEnabled(cfg.AppEnv) {
		r.Group(func(r chi.Router) {
			r.Use(docsCSP)
			docs.RegisterRoutes(r, docs.Config{PublicAPIURL: cfg.PublicAPIURL})
		})
	}
	r.Group(func(r chi.Router) {
		r.Use(CORS(cfg))
		r.Handle("/*", guarded)
	})
	return r, nil
}

func chainHandlers(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// readyHandler reports Redis and upstream reachability; 503 if Redis or any upstream is down.
func readyHandler(lim Limiter, upstreams []*url.URL, timeout time.Duration) http.HandlerFunc {
	client := &http.Client{Timeout: min(timeout, 3*time.Second)}
	return func(w http.ResponseWriter, r *http.Request) {
		checks := map[string]string{"redis": "ok"}
		ok := true
		if err := lim.Ping(r.Context()); err != nil {
			checks["redis"], ok = "down", false
		}
		for _, u := range upstreams {
			hu := *u
			hu.Path, hu.RawQuery = "/healthz", ""
			req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, hu.String(), nil)
			resp, err := client.Do(req)
			state := "ok"
			if err != nil || resp.StatusCode >= 500 {
				state, ok = "down", false
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
			checks["upstream "+u.Host] = state
		}
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": ok, "checks": checks})
	}
}

// notFound answers a path no upstream owns, in the API error envelope.
func notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"route not found"}}`))
}

// Run loads nothing itself: it serves cfg until SIGINT/SIGTERM, then drains for cfg.ShutdownTimeout.
func Run(cfg Config) error {
	log := logger.New(cfg.AppEnv, cfg.LogLevel).With("service", cfg.Name)
	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("REDIS_URL: %w", err)
	}
	rc := redis.NewClient(opt)
	defer rc.Close()
	for _, w := range cfg.Warnings() {
		log.Warn(w)
	}

	m := NewMetrics()
	var validator SessionValidator
	if cfg.SessionValidation {
		validator = NewRedisValidator(rc, cfg)
		log.Info("session validation on")
	}
	h, err := NewHandler(cfg, Deps{Limiter: &RedisLimiter{Client: rc, Prefix: cfg.Name + ":rl:"}, Validator: validator, Metrics: m, Log: log})
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           h,
		ReadHeaderTimeout: cfg.ReadHeaderTO,
		IdleTimeout:       cfg.IdleTimeout,
		// No Read/WriteTimeout on purpose: uploads and SSE/WebSocket streams must not be cut mid-flight.
	}
	var ops *http.Server
	if cfg.OpsListenAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", m.Handler())
		ops = &http.Server{Addr: cfg.OpsListenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := ops.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("ops listener", "err", err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.ListenAddr, "upstream", cfg.UpstreamURL, "identity", cfg.IdentityURL)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if ops != nil {
		_ = ops.Shutdown(sctx)
	}
	return srv.Shutdown(sctx)
}

// docsCSP replaces the edge's lock-down policy (default-src 'none', right for JSON) on the docs pages: the Scalar
// viewer loads its script and styles from jsDelivr and calls the API from the page. The page still cannot be framed.
func docsCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; "+
			"script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; "+
			"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; "+
			"font-src 'self' data: https://cdn.jsdelivr.net; "+
			"img-src 'self' data: https:; connect-src 'self' https:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
