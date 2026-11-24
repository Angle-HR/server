// Package orgsvc hosts the org side as its own service: the orghttp routes under /api/v1 with the monolith's
// cookie sessions (onboarding, company verification, agreements and privacy contact), the OrgService and
// AdminOrgService gRPC servers, and the wiring of both (databases, email transport, outbox relays, the review
// queue adapter).
//
// Environment (besides the monolith's: DB_URL_GLOBAL, ANGLEHR_<REGION>_POSTGRES_DSN, REDIS_URL, session and CORS
// settings, the address and registry keys, which are read the same way):
//
//	HTTP_ADDR                 default :8080 (/healthz, /readyz, /api/v1/...)
//	GRPC_ADDR                 default :9090
//	ORG_EVENTS                on to publish org.* events through the outbox (needs NATS_URL)
//	NOTIFICATIONS_TRANSPORT   events (needs NATS_URL) or fluvio (default), as the monolith
//	IDENTITY_QUEUE            db (default) or grpc: how KYB review items reach identity's queue
//	IDENTITY_GRPC_ADDR        identity's gRPC address, for IDENTITY_QUEUE=grpc
//	REGION_MOVE               inline (default) or saga: how an onboarding account moves into its region
//	NATS_URL                  bus for the outbox relays
package orgsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/kyb/kybidentity"
	"github.com/Angle-HR/server/internal/notifications"
	"github.com/Angle-HR/server/internal/orgevents"
	"github.com/Angle-HR/server/internal/orghttp"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/regionsaga"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/config"
	"github.com/Angle-HR/server/pkg/db"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/events/outbox"
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

	// Email requests: the events outbox (then relays run here) or the Fluvio queue, as the monolith.
	natsState := "off"
	var enqueuer emailEnqueuer
	if cfg.NotificationsTransport == "events" || orgevents.Enabled() {
		if cfg.NATSURL == "" {
			return errors.New("NOTIFICATIONS_TRANSPORT=events or ORG_EVENTS=on requires NATS_URL")
		}
		stopRelays, rerr := startOutboxRelays(ctx, cfg.NATSURL, log, global, router)
		if rerr != nil {
			return fmt.Errorf("start outbox relays: %w", rerr)
		}
		defer stopRelays()
		natsState = "up"
		log.Info("outbox relays running", "org_events", orgevents.Enabled(), "email_transport", cfg.NotificationsTransport)
	}
	if cfg.NotificationsTransport == "events" {
		enqueuer = &notifications.EventEnqueuer{Source: orgevents.Source}
	} else {
		fluvio, ferr := queue.NewInsertClient(global)
		if ferr != nil {
			return fmt.Errorf("create Fluvio client: %w", ferr)
		}
		enqueuer = fluvio
	}

	queueFor, closeQueue, err := kybidentity.NewFactory(global)
	if err != nil {
		return err
	}
	defer closeQueue()
	log.Info("kyb review queue", "adapter", kybidentity.Mode())

	// Sessions: the monolith's manager over the same Redis, so the cookie it issued is accepted here.
	sessions := session.NewManager(redisClient, session.ManagerConfig{
		Kind: session.KindProduct, IdleTTL: cfg.SessionIdleTTL, AbsoluteTTL: cfg.SessionAbsoluteTTL, Secure: cfg.CookieSecure,
	})
	store := regionalStore(router)
	// REGION_MOVE=saga: the onboarding steps that give the account a country run the region-migration saga
	// (identity over gRPC) instead of the inline copy.
	saga, stopSaga, err := regionsaga.Start(ctx, global, regionsaga.Regional(router.DB), log)
	if err != nil {
		return fmt.Errorf("region move saga: %w", err)
	}
	defer stopSaga()
	var mover orghttp.RegionMover
	if saga != nil {
		mover = saga
		log.Info("region move: saga", "identity", env(regionsaga.EnvIdentityAddr, "in-process"))
	}
	orgHandlers := orghttp.New(orghttp.Deps{
		Mover:  mover,
		Router: router, Global: global, Sessions: sessions, Enqueuer: enqueuer, Config: cfg, Queue: queueFor,
		Company: store,
		Identify: func(ctx context.Context) (string, region.Region, bool) {
			id, reg, ok := auth.UserFromContext(ctx)
			return id.String(), reg, ok
		},
	})
	handler := NewRouter(HTTPDeps{
		Log: log, CORSAllowedOrigins: cfg.CORSAllowedOrigins, AllowedOrigins: cfg.AllowedOrigins,
		Ready:       global.Ping,
		NATS:        func() string { return natsState },
		RequireAuth: auth.NewMiddleware(sessions).RequireAuth,
		Org:         orgHandlers,
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
	grpcSrv := NewGRPCServer(&RPC{
		Mover: mover, Countries: globalCountries{DB: global},
		Store: store, Catalog: globalCatalog{DB: global}, Log: log, DPAVersion: orghttp.CurrentDPAVersion,
		KYB: &kybReviewer{
			Router: router, Queue: queueFor, Log: log,
			NotifierFor: orghttp.NewKYBNotifierFactory(router, global, enqueuer),
		},
	})

	errCh := make(chan error, 2)
	go func() {
		log.Info("org http listening", "addr", httpSrv.Addr, "env", cfg.AppEnv)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http: %w", err)
		}
	}()
	go func() {
		log.Info("org grpc listening", "addr", grpcAddr)
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

// emailEnqueuer is what the handlers use to request an email inside their transaction.
type emailEnqueuer interface {
	EnqueueTx(ctx context.Context, tx fluvio.Tx, args fluvio.JobArgs, opts ...fluvio.EnqueueOption) (*fluvio.JobRow, error)
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
	log.Info("org service stopped")
	if err != nil {
		return fmt.Errorf("shutdown http: %w", err)
	}
	return nil
}

// startOutboxRelays connects to NATS once and runs one outbox relay for the global database and one per region, so
// org events and email requests written in the same transaction as the change reach the bus. The returned func
// stops the relays and closes the connection.
func startOutboxRelays(
	ctx context.Context, natsURL string, log *slog.Logger, global *pgxpool.Pool, router *dbrouter.DBRouter,
) (func(), error) {
	bus, err := natsbus.Connect(notifications.BusConfig(natsURL, "org"))
	if err != nil {
		return nil, err
	}
	relayCtx, cancel := context.WithCancel(ctx)
	start := func(name string, d outbox.DB) error {
		r, rerr := outbox.NewRelay(outbox.Config{DB: d, Publisher: bus, Logger: log.With("outbox", name)})
		if rerr != nil {
			return rerr
		}
		go func() { _ = r.Run(relayCtx) }()
		return nil
	}
	if err = start("global", global); err != nil {
		cancel()
		_ = bus.Close()
		return nil, err
	}
	for _, reg := range dbrouter.Regions() {
		pool, perr := router.DB(reg)
		if perr != nil {
			continue
		}
		if err = start(string(reg), pool); err != nil {
			cancel()
			_ = bus.Close()
			return nil, err
		}
	}
	return func() { cancel(); _ = bus.Close() }, nil
}
