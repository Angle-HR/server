// Package app wires configuration, persistence, and HTTP routes for the server.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/oschwald/geoip2-golang"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/docs"
	"github.com/Angle-HR/server/internal/jobs"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/waitlist/catalog"
	"github.com/Angle-HR/server/internal/waitlist/session"
	"github.com/Angle-HR/server/pkg/config"
	"github.com/Angle-HR/server/pkg/db"
	"github.com/Angle-HR/server/pkg/logger"
	"github.com/Angle-HR/server/pkg/queue"
)

const readHeaderTimeout = 5 * time.Second

// Run starts the HTTP server and blocks until shutdown.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.AppEnv)
	ctx := context.Background()

	regionConfigs, err := dbrouter.LoadConfigsFromEnv()
	if err != nil {
		return fmt.Errorf("load regional database config: %w", err)
	}

	dbRouter, err := dbrouter.New(ctx, regionConfigs)
	if err != nil {
		return fmt.Errorf("connect regional databases: %w", err)
	}
	defer dbRouter.Close()

	globalPool, err := db.NewGlobalPool(ctx, cfg.DBUrlGlobal)
	if err != nil {
		return fmt.Errorf("connect global database: %w", err)
	}
	defer globalPool.Close()

	var geoDB *geoip2.Reader
	if cfg.GeoLite2Path != "" {
		geoDB, err = geoip2.Open(cfg.GeoLite2Path)
		if err != nil {
			return fmt.Errorf("open geolite2 database: %w", err)
		}
		defer geoDB.Close()
	}

	regionResolver := region.NewRegionResolver(globalPool, geoDB, []byte(cfg.JWTSecret))
	regionResolver.BaseDomain = cfg.RegionBaseDomain

	redisClient, err := db.NewRedis(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			log.Warn("close redis client", "error", closeErr)
		}
	}()

	taskClient, err := queue.NewClient(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("create task client: %w", err)
	}
	defer func() {
		if closeErr := taskClient.Close(); closeErr != nil {
			log.Warn("close task client", "error", closeErr)
		}
	}()

	_ = jobs.NewPublisher(taskClient)

	catalogRepo := catalog.NewRepository(dbRouter)
	sessionRepo := session.NewRepository(dbRouter)

	catalogHandler := catalog.NewHandler(catalogRepo)
	sessionHandler := session.NewHandler(session.NewService(sessionRepo, catalogRepo))

	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(chimiddleware.RealIP)
	router.Use(chimiddleware.Recoverer)

	if docs.IsEnabled(cfg.AppEnv) {
		docs.RegisterRoutes(router)
	}

	router.Route("/api/v1", func(r chi.Router) {
		r.Use(regionResolver.Middleware())
		catalogHandler.RegisterRoutes(r)
		sessionHandler.RegisterRoutes(r)
	})

	server := &http.Server{
		Addr:              ":" + cfg.ServerPort,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("server listening", "addr", server.Addr, "env", cfg.AppEnv)
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen and serve: %w", serveErr)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Info("shutdown signal received", "signal", sig.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown server: %w", err)
	}

	log.Info("server stopped")
	return nil
}
