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

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/docs"
	"github.com/Angle-HR/server/internal/handler"
	"github.com/Angle-HR/server/pkg/config"
	"github.com/Angle-HR/server/pkg/db"
	"github.com/Angle-HR/server/pkg/logger"
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

	countriesHandler := handler.NewCountriesHandler(globalPool)
	catalogHandler := handler.NewCatalogHandler(globalPool)
	waitlistHandler := handler.NewWaitlistHandler(dbRouter, globalPool)
	onboardingHandler := handler.NewOnboardingHandler(dbRouter, globalPool)

	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(chimiddleware.RealIP)
	router.Use(chimiddleware.Recoverer)

	if docs.IsEnabled(cfg.AppEnv) {
		docs.RegisterRoutes(router, docs.Config{PublicAPIURL: cfg.PublicAPIURL})
	}

	router.Route("/api/v1", func(r chi.Router) {
		countriesHandler.RegisterRoutes(r)
		catalogHandler.RegisterRoutes(r)
		waitlistHandler.RegisterRoutes(r)
		onboardingHandler.RegisterRoutes(r)
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
