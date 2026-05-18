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

	"github.com/Angle-HR/server/internal/docs"
	"github.com/Angle-HR/server/internal/jobs"
	appmiddleware "github.com/Angle-HR/server/internal/middleware"
	waitlistadmin "github.com/Angle-HR/server/internal/waitlist/admin"
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

	store, err := db.NewStore(ctx, cfg.DBUrl)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer store.Pool.Close()

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

	catalogRepo := catalog.NewRepository(store.Queries)
	sessionRepo := session.NewRepository(store.Pool, store.Queries)
	adminRepo := waitlistadmin.NewRepository(store.Queries)

	catalogHandler := catalog.NewHandler(catalogRepo)
	sessionHandler := session.NewHandler(session.NewService(sessionRepo, catalogRepo))
	adminHandler := waitlistadmin.NewHandler(adminRepo)

	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(chimiddleware.RealIP)
	router.Use(chimiddleware.Recoverer)

	if docs.IsEnabled(cfg.AppEnv) {
		docs.RegisterRoutes(router)
	}

	router.Route("/api/v1", func(r chi.Router) {
		catalogHandler.RegisterRoutes(r)
		sessionHandler.RegisterRoutes(r)

		r.Route("/admin", func(r chi.Router) {
			r.Use(appmiddleware.RequireAdmin)
			adminHandler.RegisterRoutes(r)
		})
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
