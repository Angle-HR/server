package adminbff

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/bff"
	"github.com/Angle-HR/server/pkg/logger"
)

// NewHandler is bff.NewHandler plus the admin layers: CORS (so edge rejections carry CORS headers), the path
// guard and the admin rate limits, with the cookie presence check as the session validator.
func NewHandler(cfg Config, lim bff.Limiter, m *bff.Metrics, log *slog.Logger) (http.Handler, error) {
	inner, err := bff.NewHandler(cfg.Config, bff.Deps{Limiter: lim, Validator: CookieValidator{Name: cfg.CookieName}, Metrics: m, Log: log})
	if err != nil {
		return nil, err
	}
	var h http.Handler = inner
	h = RateLimit(cfg, lim, m)(h)
	h = PathGuard(h)
	h = bff.CORS(cfg.Config)(h)
	return bff.RequestID(h), nil
}

// Run serves cfg until SIGINT/SIGTERM, then drains for cfg.ShutdownTimeout.
func Run(cfg Config) error {
	log := logger.New(cfg.AppEnv, cfg.LogLevel).With("service", cfg.Name)
	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("REDIS_URL: %w", err)
	}
	rc := redis.NewClient(opt)
	defer rc.Close()

	m := bff.NewMetrics()
	h, err := NewHandler(cfg, &bff.RedisLimiter{Client: rc, Prefix: cfg.Name + ":rl:"}, m, log)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: h, ReadHeaderTimeout: cfg.ReadHeaderTO, IdleTimeout: cfg.IdleTimeout}
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
	log.Info("listening", "addr", cfg.ListenAddr, "upstream", cfg.UpstreamURL, "cookie", cfg.CookieName)

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
