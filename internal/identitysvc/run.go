// Package identitysvc hosts the identity side as its own service: the identityhttp routes under /api/v1 with the
// monolith's cookie sessions (same Redis, same namespaces and cookie names), the IdentityService and
// AdminIdentityService gRPC servers, and the wiring of both (databases, outbox relays, org adapter).
//
// Environment (besides the monolith's: DB_URL_GLOBAL, ANGLEHR_<REGION>_POSTGRES_DSN, REDIS_URL, session, cookie, CORS
// and admin bootstrap settings, TOTP_ENCRYPTION_KEY, which are read the same way):
//
//	HTTP_ADDR        default :8080 (/healthz, /readyz, /api/v1/...)
//	GRPC_ADDR        default :9090
//	NATS_URL         required: mail requests and identity events leave through the outbox relays
//	IDENTITY_EVENTS  on to publish identity.user.* and identity.staff.* through the outbox, and to consume identity.staff.deactivated
//	ORG_APPLY        db (default) or grpc: how a KYB decision reaches org
//	ORG_GRPC_ADDR    org's gRPC address for ORG_APPLY=grpc
package identitysvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/handler"
	"github.com/Angle-HR/server/internal/hiring/callerresolver"
	"github.com/Angle-HR/server/internal/identityevents"
	"github.com/Angle-HR/server/internal/identityhttp"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybnotify"
	"github.com/Angle-HR/server/internal/notifications"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/internal/userdir"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/config"
	"github.com/Angle-HR/server/pkg/db"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/logger"
	redisclient "github.com/Angle-HR/server/pkg/redis"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Run starts the service and blocks until SIGINT/SIGTERM or a server fails.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log := logger.New(cfg.AppEnv, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	redisClient, err := redisclient.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() { besteffort.Log(context.Background(), "redisClient.Close", redisClient.Close()) }()

	regionConfigs, err := dbrouter.LoadConfigsFromEnv()
	if err != nil {
		return fmt.Errorf("load regional database config: %w", err)
	}
	router, err := dbrouter.New(ctx, regionConfigs)
	if err != nil {
		return fmt.Errorf("connect regional databases: %w", err)
	}
	defer router.Close()
	global, err := db.NewGlobalPool(ctx, cfg.DBUrlGlobal)
	if err != nil {
		return fmt.Errorf("connect global database: %w", err)
	}
	defer global.Close()

	// Mail requests always leave through the events outbox (the notifications service sends them), so the relays
	// are not optional here.
	stopRelays, err := StartOutboxRelays(ctx, cfg.NATSURL, log, global, router)
	if err != nil {
		return fmt.Errorf("start outbox relays: %w", err)
	}
	defer stopRelays()
	log.Info("outbox relays running (mail requests and identity events)")

	smCfg := session.ManagerConfig{
		IdleTTL: cfg.SessionIdleTTL, AbsoluteTTL: cfg.SessionAbsoluteTTL, Secure: cfg.CookieSecure,
	}
	smCfg.Kind = session.KindProduct
	productSessions := session.NewManager(redisClient, smCfg)
	smCfg.Kind = session.KindAdmin
	adminSessions := session.NewManager(redisClient, smCfg)
	totpCrypto, err := auth.NewTOTPCryptoKeys(cfg.TOTPEncryptionKey, cfg.TOTPEncryptionKeyPrevious)
	if err != nil {
		return fmt.Errorf("create totp crypto: %w", err)
	}

	adminStore := admin.NewStore(global)
	if identityevents.Enabled() {
		staffBus, berr := natsbus.Connect(notifications.BusConfig(cfg.NATSURL, "identity-staff"))
		if berr != nil {
			return fmt.Errorf("connect nats for staff events: %w", berr)
		}
		defer func() { _ = staffBus.Close() }()
		staffSub, serr := SubscribeStaffEvents(ctx, staffBus, &StaffDeactivation{DB: global, Sessions: adminSessions})
		if serr != nil {
			return fmt.Errorf("subscribe staff events: %w", serr)
		}
		defer func() { _ = staffSub.Stop(context.Background()) }()
		log.Info("staff deactivation: consuming identity.staff.deactivated events")
	}
	if err = bootstrapAdmin(ctx, cfg, adminStore); err != nil {
		return err
	}

	emailQueue := notifications.NewEventEnqueuer()
	notifierFor := func(reg region.Region) kyb.Notifier {
		pool, perr := router.DB(reg)
		if perr != nil {
			return nil
		}
		return &kybnotify.Notifier{Regional: pool, Global: global, Enqueuer: emailQueue}
	}
	review, closeReview, err := identityhttp.OrgReviewFromEnv(router, notifierFor)
	if err != nil {
		return err
	}
	defer closeReview()

	authHandler := identityhttp.NewAuthHandler(router, global, redisClient, productSessions, emailQueue, totpCrypto)
	// INTERIM: /auth/me shows the caller's company roles and permissions, read from the accounts tables of the shared
	// regional databases until org exposes membership over gRPC (docs/microservices/BOUNDARIES.md item 1).
	callers := &callerresolver.Resolver{Regional: func(reg region.Region) (callerresolver.DB, error) {
		p, perr := router.DB(reg)
		if perr != nil {
			return nil, perr
		}
		return p, nil
	}}
	authHandler.MeAccess = MeAccess(callers)
	adminHandler := identityhttp.NewAdminHandler(adminStore, router, global, redisClient, adminSessions, emailQueue, review)
	// ORG_MEMBERSHIP / ORG_ONBOARDING = grpc: /auth/me and the admin user detail read company data from org.
	orgReads, closeOrgReads, err := identityhttp.OrgReadsFromEnv()
	if err != nil {
		return err
	}
	defer closeOrgReads()
	authHandler.OrgReads, adminHandler.OrgReads = orgReads, orgReads
	adminHandler.UseTOTP(totpCrypto, cfg.AdminTOTPRequired)
	adminMW := auth.NewAdminMiddleware(adminSessions, adminStore)
	adminMW.RequireMFA = cfg.AdminTOTPRequired

	// The admin catalogs (countries, industries, roles, ...) are served here: they sit behind the admin session and
	// permissions this service owns.
	catalogs := handler.NewAdminHandler(adminStore, router, global, redisClient, adminSessions, emailQueue)

	httpHandler := NewRouter(HTTPDeps{
		Log: log, CORSAllowedOrigins: cfg.CORSAllowedOrigins, AllowedOrigins: cfg.AllowedOrigins,
		Ready: global.Ping,
		NATS:  func() string { return "up" },
		Identity: identityhttp.Deps{
			Auth: authHandler, Admin: adminHandler, RequireAuth: auth.NewMiddleware(productSessions).RequireAuth, AdminMW: adminMW,
			AdminExtra: func(r chi.Router) { catalogs.RegisterProtectedRoutes(r, adminMW) },
		},
	})
	httpSrv := &http.Server{
		Addr: env("HTTP_ADDR", ":8080"), Handler: httpHandler,
		ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, WriteTimeout: writeTimeout, IdleTimeout: idleTimeout,
	}

	grpcAddr := env("GRPC_ADDR", ":9090")
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("listen grpc %s: %w", grpcAddr, err)
	}
	grpcSrv := NewGRPCServer(&RPC{
		Sessions: productSessions, AdminSessions: adminSessions, RequireAdminMFA: cfg.AdminTOTPRequired, Admins: adminStore, AdminHandler: adminHandler,
		Users: &userdir.Directory{Global: global, Router: router},
	})

	errCh := make(chan error, 2)
	go func() {
		log.Info("identity http listening", "addr", httpSrv.Addr, "env", cfg.AppEnv)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http: %w", err)
		}
	}()
	go func() {
		log.Info("identity grpc listening", "addr", grpcAddr)
		if err := grpcSrv.Serve(lis); err != nil {
			errCh <- fmt.Errorf("grpc: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}
	return shutdown(log, httpSrv, func() { grpcSrv.GracefulStop() })
}

// MeAccess adapts the interim caller resolver to the /auth/me company roles and permissions. A user with no company,
// or any lookup failure, gets nothing so /auth/me keeps working.
func MeAccess(c *callerresolver.Resolver) func(ctx context.Context, userID string, reg region.Region) (roles, permissions []string) {
	return func(ctx context.Context, userID string, reg region.Region) ([]string, []string) {
		m, err := c.Lookup(ctx, userID, reg)
		if err != nil {
			return nil, nil
		}
		perms := make([]string, 0, len(m.Perms))
		for p := range m.Perms {
			perms = append(perms, string(p))
		}
		sort.Strings(perms)
		return append([]string{}, m.Roles...), perms
	}
}

func shutdown(log *slog.Logger, srv *http.Server, stopGRPC func()) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { stopGRPC(); close(done) }()
	err := srv.Shutdown(ctx)
	select {
	case <-done:
	case <-ctx.Done():
	}
	log.Info("identity service stopped")
	if err != nil {
		return fmt.Errorf("shutdown http: %w", err)
	}
	return nil
}

// bootstrapAdmin creates the initial admin account when bootstrap credentials are configured (same as the monolith).
func bootstrapAdmin(ctx context.Context, cfg config.Config, store *admin.Store) error {
	if cfg.AdminBootstrapEmail == "" || cfg.AdminBootstrapPassword == "" {
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminBootstrapPassword)
	if err != nil {
		return fmt.Errorf("hash admin bootstrap password: %w", err)
	}
	if err = admin.Bootstrap(ctx, store, admin.BootstrapConfig{
		Email: cfg.AdminBootstrapEmail, PasswordHash: hash, Name: cfg.AdminBootstrapName,
	}); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	return nil
}
