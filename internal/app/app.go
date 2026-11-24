// Package app wires configuration, persistence, and HTTP routes for the server.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/docs"
	"github.com/Angle-HR/server/internal/handler"
	"github.com/Angle-HR/server/internal/hiring/applicantcounts"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringhttp"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/identityhttp"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybnotify"
	"github.com/Angle-HR/server/internal/orghttp"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/config"
	"github.com/Angle-HR/server/pkg/db"
	"github.com/Angle-HR/server/pkg/logger"
	redisclient "github.com/Angle-HR/server/pkg/redis"
)

// corsMaxAgeSeconds is how long browsers may cache CORS preflight results.
const corsMaxAgeSeconds = 300

const (
	readHeaderTimeout   = 5 * time.Second
	readTimeout         = 15 * time.Second
	writeTimeout        = 15 * time.Second
	idleTimeout         = 60 * time.Second
	maxRequestBodyBytes = 1 << 20 // 1 MB
)

// bootstrapAdmin creates the initial admin account when bootstrap credentials are configured.
func bootstrapAdmin(ctx context.Context, cfg config.Config, store *admin.Store) error {
	if cfg.AdminBootstrapEmail == "" || cfg.AdminBootstrapPassword == "" {
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminBootstrapPassword)
	if err != nil {
		return fmt.Errorf("hash admin bootstrap password: %w", err)
	}
	if err = admin.Bootstrap(ctx, store, admin.BootstrapConfig{
		Email:        cfg.AdminBootstrapEmail,
		PasswordHash: hash,
		Name:         cfg.AdminBootstrapName,
	}); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	return nil
}

// useCommonMiddleware installs body limits, request IDs, logging, panic recovery and CORS.
func useCommonMiddleware(router chi.Router, cfg config.Config, log *slog.Logger) {
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
			next.ServeHTTP(w, r)
		})
	})
	router.Use(chimiddleware.RequestID)
	router.Use(chimiddleware.RealIP)
	router.Use(logger.RequestLogger(log))
	router.Use(chimiddleware.Recoverer)
	if len(cfg.CORSAllowedOrigins) > 0 {
		allowedOrigins := cfg.CORSAllowedOrigins
		if cfg.FluvioUIOrigin != "" {
			// This middleware answers every preflight, so the dashboard's authenticated calls to /fluvio/* need it too.
			allowedOrigins = append(slices.Clone(allowedOrigins), cfg.FluvioUIOrigin)
		}
		router.Use(cors.Handler(cors.Options{
			AllowedOrigins:   allowedOrigins,
			AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Accept", "Content-Type", "If-Match", "Idempotency-Key", session.CSRFHeader},
			AllowCredentials: true, // session cookies; origins are an explicit allow-list, never "*"
			MaxAge:           corsMaxAgeSeconds,
		}))
	}
}

// Run starts the HTTP server and blocks until shutdown.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.AppEnv, cfg.LogLevel)
	ctx := context.Background()

	redisClient, err := redisclient.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() { besteffort.Log(context.Background(), "redisClient.Close", redisClient.Close()) }()

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

	if migrateErr := queue.Migrate(ctx, globalPool); migrateErr != nil {
		return fmt.Errorf("apply Fluvio migrations: %w", migrateErr)
	}

	fluvioClient, err := queue.NewInsertClient(globalPool)
	if err != nil {
		return fmt.Errorf("create Fluvio client: %w", err)
	}

	emailQueue, stopEmailQueue, err := selectEmailEnqueuer(ctx, cfg, log, fluvioClient, globalPool, dbRouter)
	if err != nil {
		return fmt.Errorf("select email transport: %w", err)
	}
	defer stopEmailQueue()

	sessionCfg := session.ManagerConfig{
		IdleTTL: cfg.SessionIdleTTL, AbsoluteTTL: cfg.SessionAbsoluteTTL, Secure: cfg.CookieSecure,
	}
	sessionCfg.Kind = session.KindProduct
	productSessions := session.NewManager(redisClient, sessionCfg)
	sessionCfg.Kind = session.KindAdmin
	adminSessions := session.NewManager(redisClient, sessionCfg)
	totpCrypto, err := auth.NewTOTPCryptoKeys(cfg.TOTPEncryptionKey, cfg.TOTPEncryptionKeyPrevious)
	if err != nil {
		return fmt.Errorf("create totp crypto: %w", err)
	}
	authMiddleware := auth.NewMiddleware(productSessions)

	adminStore := admin.NewStore(globalPool)
	if bootstrapErr := bootstrapAdmin(ctx, cfg, adminStore); bootstrapErr != nil {
		return bootstrapErr
	}
	adminMiddleware := auth.NewAdminMiddleware(adminSessions, adminStore)
	adminMiddleware.RequireMFA = cfg.AdminTOTPRequired

	var identityDeps identityhttp.Deps
	var authHandler *identityhttp.AuthHandler
	var identityAdmin *identityhttp.AdminHandler
	if !identityhttp.Remote() {
		authHandler = identityhttp.NewAuthHandler(dbRouter, globalPool, redisClient, productSessions, emailQueue, totpCrypto)
	}
	// The org routes (countries, onboarding, company verification) live in internal/orghttp so cmd/org serves the
	// same handlers. ORG_REMOTE=on stops this process serving them (and hiring's agreements routes).
	var orgHandlers *orghttp.Handlers
	if !orghttp.Remote() {
		orgHandlers = orghttp.New(orghttp.Deps{
			Router: dbRouter, Global: globalPool, Sessions: productSessions, Enqueuer: emailQueue, Config: cfg,
		})
	}
	kybNotifierFor := newKYBNotifierFactory(dbRouter, globalPool, emailQueue)
	counts, closeCounts, err := applicantcounts.FromEnv(func(reg string) (*hiringstore.Store, error) {
		pool, perr := dbRouter.DB(region.Region(reg))
		if perr != nil {
			return nil, perr
		}
		return &hiringstore.Store{DB: pool, Region: reg}, nil
	})
	if err != nil {
		return fmt.Errorf("applicant counts: %w", err)
	}
	defer closeCounts()
	hiringHandler := newHiringHandler(dbRouter, globalPool, counts)
	publicHandler := newPublicHandler(cfg, dbRouter, globalPool, redisClient, log)
	hiringHandler.Public = publicHandler.Public
	hiringHandler.Applications = newApplicationsService(cfg, dbRouter, log)
	hiringHandler.Remote = hiringhttp.HiringRemote()
	publicHandler.Remote = hiringhttp.HiringRemote()
	if authHandler != nil {
		authHandler.MeAccess = hiringHandler.Access
	}
	adminHandler := handler.NewAdminHandler(
		adminStore,
		dbRouter,
		globalPool,
		redisClient,
		adminSessions,
		fluvioClient,
		emailQueue,
	)

	adminHandler.KYBNotifierFor = kybNotifierFor
	if !identityhttp.Remote() {
		review, closeReview, reviewErr := identityhttp.OrgReviewFromEnv(dbRouter, identityhttp.KYBNotifierFactory(kybNotifierFor))
		if reviewErr != nil {
			return reviewErr
		}
		defer closeReview()
		identityAdmin = identityhttp.NewAdminHandler(adminStore, dbRouter, globalPool, redisClient, adminSessions,
			emailQueue, review)
		identityAdmin.UseTOTP(totpCrypto, cfg.AdminTOTPRequired)
	}
	identityDeps = identityhttp.Deps{
		Auth: authHandler, Admin: identityAdmin, RequireAuth: authMiddleware.RequireAuth, AdminMW: adminMiddleware,
		AdminExtra: func(r chi.Router) { adminHandler.RegisterProtectedRoutes(r, adminMiddleware) },
	}

	router := chi.NewRouter()
	useCommonMiddleware(router, cfg, log)

	if docs.IsEnabled(cfg.AppEnv) {
		docs.RegisterRoutes(router, docs.Config{PublicAPIURL: cfg.PublicAPIURL})

		mountFluvioUI(router, cfg, fluvioClient, log)
	}

	handler.NewHealthHandler().RegisterRoutes(router)

	publicHandler.RegisterPages(router)

	router.Route("/api/v1", func(r chi.Router) {
		// Browsers must come from an allowed origin on unsafe methods (CSRF defence in depth).
		r.Use(session.OriginGuard(cfg.AllowedOrigins))
		if orgHandlers != nil {
			orgHandlers.RegisterPublicRoutes(r)
		}
		publicHandler.RegisterRoutes(r)
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware.RequireAuth)
			if orgHandlers != nil {
				orgHandlers.RegisterProtectedRoutes(r)
			}
			hiringHandler.RegisterProtectedRoutes(r)
		})

		// Identity routes (/auth/*, /organizations/invites, the identity half of /admin). With IDENTITY_REMOTE=on they
		// are left to cmd/identity and only the admin catalogs and jobs stay here.
		identityhttp.Mount(r, identityDeps)
	})

	server := &http.Server{
		Addr:              ":" + cfg.ServerPort,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	return serve(server, cfg, log)
}

// serve runs the HTTP server until it fails or a shutdown signal arrives, then shuts it down gracefully.
func serve(server *http.Server, cfg config.Config, log *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		level, levelErr := logger.ParseLevel(cfg.LogLevel, cfg.AppEnv)
		if levelErr != nil {
			log.Warn("invalid log level, using fallback", "error", levelErr)
		}
		log.Info("server listening", "addr", server.Addr, "env", cfg.AppEnv, "log_level", level.String())
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

// newHiringHandler builds the job drafts and form builder handler. Company data is read from the caller's own
// region; markets and catalogs come from the global database.
func newHiringHandler(router *dbrouter.DBRouter, global *pgxpool.Pool, counts draft.ApplicantCounts) *hiringhttp.HiringHandler {
	dir := &hiringhttp.PostgresHiringDirectory{
		Regional: func(reg region.Region) (hiringstore.DB, error) {
			pool, err := router.DB(reg)
			if err != nil {
				return nil, err
			}
			return pool, nil
		},
		Global: global,
		Counts: counts,
	}
	identify := func(ctx context.Context) (string, region.Region, bool) {
		id, reg, ok := auth.UserFromContext(ctx)
		return id.String(), reg, ok
	}
	return hiringhttp.NewHiringHandler(dir, identify)
}

// newKYBNotifierFactory returns the factory that builds a region's KYB email notifier.
// A region with no database yields no notifier, so verification still works without emails.
func newKYBNotifierFactory(
	router *dbrouter.DBRouter, global *pgxpool.Pool, enqueuer kybnotify.Enqueuer,
) handler.KYBNotifierFactory {
	return func(reg region.Region) kyb.Notifier {
		pool, err := router.DB(reg)
		if err != nil {
			return nil
		}
		return &kybnotify.Notifier{Regional: pool, Global: global, Enqueuer: enqueuer}
	}
}
