// Package hiringsvc hosts the hiring side as its own service: the hiring routes under /api/v1 and the careers
// pages with the monolith's cookie sessions, the HiringService gRPC server, and the wiring of both (databases,
// org adapters, outbox relays).
//
// Environment (besides the monolith's: DB_URL_GLOBAL, ANGLEHR_<REGION>_POSTGRES_DSN, REDIS_URL, session and CORS
// settings, PUBLIC_SITE_URL, which are read the same way):
//
//	HTTP_ADDR        default :8080 (/healthz, /readyz, /api/v1/..., /j/{public_id})
//	GRPC_ADDR        default :9090
//	HIRING_EVENTS    on to publish hiring.job.* and hiring.job.members_changed.v1 through the outbox (needs NATS_URL)
//	ORG_READINESS    db (default) or grpc: where the publish readiness (KYB, DPA, privacy contact) comes from
//	ORG_MEMBERSHIP   db (default) or grpc: where the caller's company and permissions come from
//	ORG_INDUSTRIES   db (default) or grpc: where the industry catalog comes from
//	HIRING_ORG_EVENTS on to consume org.company.deleted.v1 and archive the company's live jobs (needs NATS_URL)
//	ORG_COMPANY      db (default) or grpc: company row of the public page and events, settings page, DPA, privacy contact
//	IDENTITY_USERS   db (default) or grpc: hiring managers' names on the job list (IDENTITY_GRPC_ADDR)
//	ORG_GRPC_ADDR    org service address, required when any ORG_* switch is grpc
//	ORG_GRPC_TIMEOUT bound for one org call, default 3s
package hiringsvc

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

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/applicantcounts"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringhttp"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/jobevents"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/notifications"
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

	orgWiring, err := NewOrgWiring(os.Getenv, log)
	if err != nil {
		return err
	}
	defer orgWiring.Close()

	natsState := "off"
	if jobevents.Enabled() {
		stopRelays, rerr := StartOutboxRelays(ctx, cfg.NATSURL, log, global, router)
		if rerr != nil {
			return fmt.Errorf("start outbox relays: %w", rerr)
		}
		defer stopRelays()
		natsState = "up"
		log.Info("hiring events on: outbox relays running")
	}

	regional := func(reg region.Region) (hiringstore.DB, error) {
		p, perr := router.DB(reg)
		if perr != nil {
			return nil, perr
		}
		return p, nil
	}
	counts, closeCounts, cerr := applicantcounts.FromEnv(func(reg string) (*hiringstore.Store, error) {
		pool, perr := router.DB(region.Region(reg))
		if perr != nil {
			return nil, perr
		}
		return &hiringstore.Store{DB: pool, Region: reg}, nil
	})
	if cerr != nil {
		return fmt.Errorf("applicant counts: %w", cerr)
	}
	defer closeCounts()
	dir := &hiringhttp.PostgresHiringDirectory{Regional: regional, Global: global, Counts: counts}
	orgWiring.Apply(dir, &hiringstore.Global{DB: global})

	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvOrgEvents)), "on") {
		if cfg.NATSURL == "" {
			return fmt.Errorf("%s=on requires NATS_URL", EnvOrgEvents)
		}
		orgBus, berr := natsbus.Connect(notifications.BusConfig(cfg.NATSURL, "hiring-org"))
		if berr != nil {
			return fmt.Errorf("connect nats: %w", berr)
		}
		defer func() { _ = orgBus.Close() }()
		orgSub, serr := SubscribeOrgEvents(ctx, orgBus, router, func(reg string) (*draft.Service, error) {
			return dir.Service(region.Region(reg))
		}, log)
		if serr != nil {
			return fmt.Errorf("subscribe org events: %w", serr)
		}
		defer func() { _ = orgSub.Stop(context.Background()) }()
		natsState = "up"
		log.Info("company deletion: consuming org.company.deleted events")
	}

	identify := func(ctx context.Context) (string, region.Region, bool) {
		id, reg, ok := auth.UserFromContext(ctx)
		return id.String(), reg, ok
	}
	hiring := hiringhttp.NewHiringHandler(dir, identify)
	hiring.NoApplications = true

	g := &hiringstore.Global{DB: global}
	pub := &public.Service{
		Registry: g, Slugs: g, Directory: hiringhttp.PublicDirectory{Pool: hiringhttp.RegionalDB(regional), Org: orgWiring.Company}, Catalog: g,
		SiteURL: publicSiteURL(cfg),
	}
	publicHandler := hiringhttp.NewPublicHandler(pub, nil)
	publicHandler.NoApplications = true
	hiring.Public = pub

	// Sessions: the monolith's manager over the same Redis, so the cookie it issued is accepted here.
	sessions := session.NewManager(redisClient, session.ManagerConfig{
		Kind: session.KindProduct, IdleTTL: cfg.SessionIdleTTL, AbsoluteTTL: cfg.SessionAbsoluteTTL, Secure: cfg.CookieSecure,
	})
	modes := map[string]string{}
	for k, v := range orgWiring.Modes {
		modes[strings.ToLower(strings.TrimPrefix(k, "ORG_"))] = string(v)
	}
	handler := NewRouter(HTTPDeps{
		Log: log, CORSAllowedOrigins: cfg.CORSAllowedOrigins, AllowedOrigins: cfg.AllowedOrigins,
		Ready: global.Ping, NATS: func() string { return natsState }, Org: modes,
		RequireAuth: auth.NewMiddleware(sessions).RequireAuth,
		Hiring:      hiring, Public: publicHandler,
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
	grpcSrv := NewGRPCServer(&RPC{Service: dir.Service, Public: pub, Access: storeAccess{regional: regional}})

	errCh := make(chan error, 2)
	go func() {
		log.Info("hiring http listening", "addr", httpSrv.Addr, "env", cfg.AppEnv)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http: %w", err)
		}
	}()
	go func() {
		log.Info("hiring grpc listening", "addr", grpcAddr)
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
	log.Info("hiring service stopped")
	if err != nil {
		return fmt.Errorf("shutdown http: %w", err)
	}
	return nil
}

// publicSiteURL is the origin that serves /j/{public_id}: PUBLIC_SITE_URL, else PUBLIC_API_URL.
func publicSiteURL(cfg config.Config) string {
	siteURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_SITE_URL")), "/")
	if siteURL == "" {
		siteURL = config.NormalizePublicAPIURL(cfg.PublicAPIURL)
	}
	return siteURL
}

// storeAccess reads a job's creator and team from the job's region (CheckJobAccess).
type storeAccess struct {
	regional func(reg region.Region) (hiringstore.DB, error)
}

// JobTeam implements AccessReader.
func (a storeAccess) JobTeam(ctx context.Context, reg region.Region, companyID, jobID, userID string) (string, string, error) {
	pool, err := a.regional(reg)
	if err != nil {
		return "", "", err
	}
	rec, err := (&hiringstore.Store{DB: pool, Region: string(reg)}).GetJob(ctx, companyID, jobID)
	if err != nil {
		return "", "", err
	}
	for _, m := range rec.Members {
		if m.UserID == userID {
			return rec.Job.CreatedBy, m.Role, nil
		}
	}
	return rec.Job.CreatedBy, "", nil
}
