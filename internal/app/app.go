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

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/docs"
	"github.com/Angle-HR/server/internal/handler"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/pkg/config"
	"github.com/Angle-HR/server/pkg/db"
	"github.com/Angle-HR/server/pkg/logger"
	redisclient "github.com/Angle-HR/server/pkg/redis"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/software78/fluvio/fluviui"
)

const (
	readHeaderTimeout   = 5 * time.Second
	readTimeout         = 15 * time.Second
	writeTimeout        = 15 * time.Second
	idleTimeout         = 60 * time.Second
	maxRequestBodyBytes = 1 << 20 // 1 MB
)

// Run starts the HTTP server and blocks until shutdown.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.AppEnv)
	ctx := context.Background()

	redisClient, err := redisclient.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer redisClient.Close()

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

	if err := queue.Migrate(ctx, globalPool); err != nil {
		return fmt.Errorf("apply Fluvio migrations: %w", err)
	}

	fluvioClient, err := queue.NewInsertClient(globalPool)
	if err != nil {
		return fmt.Errorf("create Fluvio client: %w", err)
	}

	tokenService, err := auth.NewTokenService(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	if err != nil {
		return fmt.Errorf("create token service: %w", err)
	}
	authMiddleware := auth.NewMiddleware(tokenService)

	adminStore := admin.NewStore(globalPool)
	if cfg.AdminBootstrapEmail != "" && cfg.AdminBootstrapPassword != "" {
		hash, err := auth.HashPassword(cfg.AdminBootstrapPassword)
		if err != nil {
			return fmt.Errorf("hash admin bootstrap password: %w", err)
		}
		if err := admin.Bootstrap(ctx, adminStore, admin.BootstrapConfig{
			Email:        cfg.AdminBootstrapEmail,
			PasswordHash: hash,
			Name:         cfg.AdminBootstrapName,
		}); err != nil {
			return fmt.Errorf("bootstrap admin: %w", err)
		}
	}
	adminMiddleware := auth.NewAdminMiddleware(tokenService, adminStore)

	countriesHandler := handler.NewCountriesHandler(globalPool)
	catalogHandler := handler.NewCatalogHandler(globalPool)
	waitlistHandler := handler.NewWaitlistHandler(dbRouter, globalPool, fluvioClient)
	onboardingHandler := handler.NewOnboardingHandler(dbRouter, globalPool, fluvioClient)
	authHandler := handler.NewAuthHandler(dbRouter, globalPool, redisClient, tokenService, fluvioClient, cfg.AuthDefaultRegion)
	productOnboardingHandler := handler.NewProductOnboardingHandler(dbRouter, globalPool, fluvioClient)
	adminHandler := handler.NewAdminHandler(adminStore, dbRouter, globalPool, tokenService, fluvioClient)

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
			next.ServeHTTP(w, r)
		})
	})
	router.Use(chimiddleware.RequestID)
	router.Use(chimiddleware.RealIP)
	router.Use(chimiddleware.Recoverer)
	if len(cfg.CORSAllowedOrigins) > 0 {
		router.Use(cors.Handler(cors.Options{
			AllowedOrigins:   cfg.CORSAllowedOrigins,
			AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
			AllowCredentials: false,
			MaxAge:           300,
		}))
	}

	if docs.IsEnabled(cfg.AppEnv) {
		docs.RegisterRoutes(router, docs.Config{PublicAPIURL: cfg.PublicAPIURL})

		fluvioUIOrigin := cfg.FluvioUIOrigin
		if fluvioUIOrigin == "" {
			fluvioUIOrigin = "http://localhost:5173"
		}
		router.Handle("/fluvio/*", fluviui.Handler(fluvioClient, fluviui.WithAllowedOrigin(fluvioUIOrigin)))
	}

	router.Route("/api/v1", func(r chi.Router) {
		countriesHandler.RegisterRoutes(r)
		catalogHandler.RegisterRoutes(r)
		waitlistHandler.RegisterRoutes(r)
		onboardingHandler.RegisterRoutes(r)
		r.Route("/auth", authHandler.RegisterRoutes)
		productOnboardingHandler.RegisterRoutes(r)
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware.RequireAuth)
			productOnboardingHandler.RegisterProtectedRoutes(r)
		})

		r.Route("/admin", func(r chi.Router) {
			adminHandler.RegisterPublicRoutes(r)
			r.Group(func(r chi.Router) {
				r.Use(adminMiddleware.RequireAdmin)
				adminHandler.RegisterProtectedRoutes(r, adminMiddleware)
			})
		})
	})

	server := &http.Server{
		Addr:              ":" + cfg.ServerPort,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
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
