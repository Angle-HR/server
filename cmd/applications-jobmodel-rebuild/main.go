// Command applications-jobmodel-rebuild fills the applications service's job read model (hiring.app_jobs) from the
// hiring tables: for every published, paused, closed or expired job in every region it reads the job through the
// DB adapter (internal/hiring/jobsource) and writes it into the model. It runs once and exits, and is idempotent.
//
// It is the alternative to cmd/hiring-jobs-backfill, which re-emits hiring.job.* events instead. See
// docs/microservices/applications-jobmodel.md. Needs the regional database settings (ANGLEHR_<REGION>_*) and
// DB_URL_GLOBAL (the job registry the DB adapter resolves public ids with); no NATS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"

	"github.com/Angle-HR/server/internal/applicationsvc/jobmodel"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/jobsource"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/db"
	"github.com/Angle-HR/server/pkg/logger"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// lister reads the live and closed jobs of a region from the hiring tables, company by company (row level security
// hides jobs until a company is chosen).
type lister struct {
	router *dbrouter.DBRouter
	closed bool
}

const jobsSQL = `
SELECT public_id FROM hiring.job_postings
WHERE tenant_id = $1::uuid AND deleted_at IS NULL AND public_id IS NOT NULL AND public_id <> ''
  AND status = ANY($2::text[]) ORDER BY job_number`

func (l lister) Jobs(ctx context.Context, reg string) ([]jobmodel.JobRef, error) {
	pool, err := l.router.DB(region.Region(reg))
	if err != nil {
		return nil, err
	}
	st := &hiringstore.Store{DB: pool, Region: reg}
	ids, err := st.TenantIDs(ctx)
	if err != nil {
		return nil, err
	}
	statuses := []string{"published", "paused"}
	if l.closed {
		statuses = append(statuses, "closed", "expired")
	}
	var out []jobmodel.JobRef
	for _, tenant := range ids {
		err := st.InTenantTx(ctx, tenant, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, jobsSQL, tenant, statuses)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var pid string
				if err := rows.Scan(&pid); err != nil {
					return err
				}
				out = append(out, jobmodel.JobRef{TenantID: tenant, PublicID: pid})
			}
			return rows.Err()
		})
		if err != nil {
			return nil, fmt.Errorf("company %s: %w", tenant, err)
		}
	}
	return out, nil
}

func run() error {
	closed := flag.Bool("closed", true, "also fill closed and expired jobs (needed for the retention sweep)")
	flag.Parse()
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development"
	}
	slogger := logger.New(appEnv, os.Getenv("LOG_LEVEL"))
	ctx := context.Background()

	regionConfigs, err := dbrouter.LoadConfigsFromEnv()
	if err != nil {
		return fmt.Errorf("load regional database config: %w", err)
	}
	router, err := dbrouter.New(ctx, regionConfigs)
	if err != nil {
		return fmt.Errorf("connect regional databases: %w", err)
	}
	defer router.Close()

	pool := func(reg string) (hiringstore.DB, error) { return router.DB(region.Region(reg)) }
	// Rebuild only reads regional data, but the adapter's registry needs the global database to be complete.
	global, err := db.NewGlobalPool(ctx, os.Getenv("DB_URL_GLOBAL"))
	if err != nil {
		return fmt.Errorf("connect global database: %w", err)
	}
	defer global.Close()
	fallback := &jobsource.Source{Global: &hiringstore.Global{DB: global}, Pool: pool}
	m := &jobmodel.Model{
		Fallback: fallback,
		Pool:     func(reg string) (jobmodel.DB, error) { return router.DB(region.Region(reg)) },
	}
	regions := make([]string, 0, 5)
	for _, r := range dbrouter.Regions() {
		regions = append(regions, string(r))
	}
	reports, err := m.Rebuild(ctx, slogger, lister{router: router, closed: *closed}, regions)
	for _, r := range reports {
		slogger.Info("applications job model rebuilt", "region", r.Region, "listed", r.Listed, "written", r.Written,
			"skipped", r.Skipped, "failed", r.Failed)
	}
	return err
}
