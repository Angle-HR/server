// Command hiring-expire expires published jobs whose closing date has passed. It runs once and exits, so
// schedule it (for example hourly) with cron or the platform's job scheduler. It only changes a job's status;
// nothing is deleted.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/joho/godotenv"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/expiry"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/jobevents"
	"github.com/Angle-HR/server/internal/hiring/orgclient"
	"github.com/Angle-HR/server/internal/hiringsvc"
	"github.com/Angle-HR/server/pkg/db"
	"github.com/Angle-HR/server/pkg/events/outbox"
	"github.com/Angle-HR/server/pkg/logger"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development"
	}
	slogger := logger.New(appEnv, os.Getenv("LOG_LEVEL"))
	ctx := context.Background()

	dbURL := os.Getenv("DB_URL_GLOBAL")
	if dbURL == "" {
		return errors.New("DB_URL_GLOBAL is required")
	}
	global, err := db.NewGlobalPool(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to global database: %w", err)
	}
	defer global.Close()

	regionConfigs, err := dbrouter.LoadConfigsFromEnv()
	if err != nil {
		return fmt.Errorf("load regional database config: %w", err)
	}
	router, err := dbrouter.New(ctx, regionConfigs)
	if err != nil {
		return fmt.Errorf("connect regional databases: %w", err)
	}
	defer router.Close()

	var orgClient orgclient.OrgClient
	if os.Getenv("ORG_TENANTS") == "grpc" {
		addr := os.Getenv("ORG_GRPC_ADDR")
		if addr == "" {
			return errors.New("ORG_TENANTS=grpc requires ORG_GRPC_ADDR")
		}
		conn, derr := hiringsvc.Dial(addr, orgclient.DefaultTimeout)
		if derr != nil {
			return fmt.Errorf("dial org: %w", derr)
		}
		defer func() { _ = conn.Close() }()
		orgClient = orgv1.NewOrgServiceClient(conn)
	}
	failed := false
	pools := map[string]outbox.DB{}
	for _, reg := range dbrouter.Regions() {
		pool, err := router.DB(reg)
		if err != nil {
			slogger.Warn("hiring expire: region skipped", "region", string(reg), "error", err)
			continue
		}
		pools[string(reg)] = pool
		store := &hiringstore.Store{DB: pool, Region: string(reg)}
		tenants := store.TenantIDs
		if os.Getenv("ORG_TENANTS") == "grpc" { // ListCompanies instead of reading accounts.organizations
			tenants = (&orgclient.GRPCTenants{Client: orgClient, Region: string(reg)}).TenantIDs
		}
		sweeper := &expiry.Sweeper{
			Tenants: tenants,
			Expirer: &draft.Service{Store: store, Ref: &hiringstore.Global{DB: global}},
			Logger:  slogger,
		}
		res, err := sweeper.Run(ctx)
		attrs := []any{"region", string(reg), "companies", res.Companies, "expired", res.Expired, "errors", res.Errors}
		if err != nil {
			failed = true
			slogger.Error("hiring expire failed", append(attrs, "error", err)...)
			continue
		}
		slogger.Info("hiring expire done", attrs...)
	}
	if jobevents.Enabled() { // the job has no long-running relay: publish what the run left in the outboxes
		if err := drainOutboxes(ctx, slogger, pools); err != nil {
			return fmt.Errorf("hiring expire: drain outbox: %w", err)
		}
	}
	if failed {
		return errors.New("hiring expire: one or more regions failed")
	}
	return nil
}
