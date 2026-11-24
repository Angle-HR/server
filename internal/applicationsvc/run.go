// Package applicationsvc hosts the applications side as its own service: the apphttp routes under /api/v1 with
// the monolith's cookie sessions, the ApplicationsService gRPC server, and the wiring of both (databases, blob
// store, job source, outbox relays).
//
// Environment (besides the monolith's: DB_URL_GLOBAL, ANGLEHR_<REGION>_POSTGRES_DSN, R2_*, REDIS_URL, session and
// CORS settings, which are read the same way):
//
//	HTTP_ADDR              default :8080 (/healthz, /readyz, /api/v1/...)
//	GRPC_ADDR              default :9090
//	APPLICATIONS_JOBSOURCE db (default) or events
//	APPLICATIONS_ORG_EVENTS on to consume org.company.deleted.v1 and delete the company's data (needs NATS_URL)
//	APPLICATIONS_EVENTS    on to publish applications.application.* through the outbox (needs NATS_URL)
//	NATS_URL               bus for the outbox relays
package applicationsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Angle-HR/server/internal/applicationsvc/companydeletion"
	"github.com/Angle-HR/server/internal/applicationsvc/jobmodel"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/appevents"
	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/callerresolver"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/session"
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

// EnvOrgEvents turns on the org.company.deleted.v1 consumer.
const EnvOrgEvents = "APPLICATIONS_ORG_EVENTS"

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

	orgSrc, orgTenants, orgNotice, closeOrg, err := OrgSourceFromEnv()
	if err != nil {
		return err
	}
	defer closeOrg()
	stats := &jobmodel.Stats{OnFallback: func(method, reason string) {
		log.Warn("job read model used the hiring tables", "method", method, "reason", reason)
	}}
	jobs, err := NewJobSource(os.Getenv(EnvJobSource), JobSourceDeps{
		Global: global, Router: router, NATSURL: cfg.NATSURL, Org: orgSrc, OrgTenants: orgTenants, OrgNotice: orgNotice,
		Stats: stats, NoFallback: strings.EqualFold(strings.TrimSpace(os.Getenv(EnvJobFallback)), "off"),
	})
	if err != nil {
		return fmt.Errorf("job source: %w", err)
	}
	log.Info("job source", "kind", env(EnvJobSource, JobSourceDB))

	natsState := "off"
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvJobSource)), JobSourceEvents) {
		if cfg.NATSURL == "" {
			return fmt.Errorf("%s=events requires NATS_URL", EnvJobSource)
		}
		bus, berr := natsbus.Connect(natsbus.Config{URL: cfg.NATSURL, Name: "applications"})
		if berr != nil {
			return fmt.Errorf("connect nats: %w", berr)
		}
		defer func() { _ = bus.Close() }()
		sub, serr := jobmodel.SubscribeIndexed(ctx, bus, router, &jobmodel.PGIndex{DB: global})
		if serr != nil {
			return fmt.Errorf("subscribe job events: %w", serr)
		}
		defer func() { _ = sub.Stop(context.Background()) }()
		natsState = "up"
		log.Info("job read model: consuming hiring.job.* events")
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvOrgEvents)), "on") {
		if cfg.NATSURL == "" {
			return fmt.Errorf("%s=on requires NATS_URL", EnvOrgEvents)
		}
		orgBus, berr := natsbus.Connect(natsbus.Config{URL: cfg.NATSURL, Name: "applications-org"})
		if berr != nil {
			return fmt.Errorf("connect nats: %w", berr)
		}
		defer func() { _ = orgBus.Close() }()
		orgSub, serr := companydeletion.Subscribe(ctx, orgBus, router)
		if serr != nil {
			return fmt.Errorf("subscribe org events: %w", serr)
		}
		defer func() { _ = orgSub.Stop(context.Background()) }()
		natsState = "up"
		log.Info("company deletion: consuming org.company.deleted events")
	}
	if appevents.Enabled() {
		stopRelays, rerr := StartOutboxRelays(ctx, cfg.NATSURL, log, router)
		if rerr != nil {
			return fmt.Errorf("start outbox relays: %w", rerr)
		}
		defer stopRelays()
		natsState = "up"
		log.Info("application events on: outbox relays running")
	}

	svcs := NewServices(ServicesDeps{Config: cfg, Router: router, Jobs: jobs, Redis: redisClient, Log: log})

	// Sessions: the monolith's manager over the same Redis, so the cookie it issued is accepted here.
	sessions := session.NewManager(redisClient, session.ManagerConfig{
		Kind: session.KindProduct, IdleTTL: cfg.SessionIdleTTL, AbsoluteTTL: cfg.SessionAbsoluteTTL, Secure: cfg.CookieSecure,
	})
	// Company and roles: CALLER_RESOLVER=db (default, INTERIM: the accounts tables of the shared regional
	// databases) or grpc (OrgService.ResolveMembership at ORG_GRPC_ADDR).
	callers, closeCallers, err := callerresolver.FromEnv(func(reg region.Region) (callerresolver.DB, error) {
		p, perr := router.DB(reg)
		if perr != nil {
			return nil, perr
		}
		return p, nil
	})
	if err != nil {
		return fmt.Errorf("caller resolver: %w", err)
	}
	defer closeCallers()
	log.Info("caller resolver", "kind", env(callerresolver.EnvResolver, "db"))
	handler := NewRouter(HTTPDeps{
		Log: log, CORSAllowedOrigins: cfg.CORSAllowedOrigins, AllowedOrigins: cfg.AllowedOrigins,
		Ready:       global.Ping,
		Stats:       stats.Snapshot,
		NATS:        func() string { return natsState },
		RequireAuth: auth.NewMiddleware(sessions).RequireAuth,
		Apps: apphttp.Deps{
			Identify: func(ctx context.Context) (string, region.Region, bool) {
				id, reg, ok := auth.UserFromContext(ctx)
				return id.String(), reg, ok
			},
			Callers: callers, Applications: svcs.Applications, Apply: svcs.Apply, Uploads: svcs.Uploads,
		},
	})
	httpSrv := &http.Server{
		Addr: env("HTTP_ADDR", ":8080"), Handler: handler,
		ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, WriteTimeout: writeTimeout, IdleTimeout: idleTimeout,
	}

	grpcAddr := env("GRPC_ADDR", ":9090")
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("listen grpc %s: %w", grpcAddr, err)
	}
	rpcSvc := &RPC{Applications: svcs.Applications, Lookup: svcs.Lookup, Apply: svcs.Apply}
	if svcs.Uploads != nil { // a nil *uploads.Service must not become a non-nil interface
		rpcSvc.Uploads = svcs.Uploads
	}
	grpcSrv := NewGRPCServer(rpcSvc)

	errCh := make(chan error, 2)
	go func() {
		log.Info("applications http listening", "addr", httpSrv.Addr, "env", cfg.AppEnv)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http: %w", err)
		}
	}()
	go func() {
		log.Info("applications grpc listening", "addr", grpcAddr)
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
	log.Info("applications service stopped")
	if err != nil {
		return fmt.Errorf("shutdown http: %w", err)
	}
	return nil
}
