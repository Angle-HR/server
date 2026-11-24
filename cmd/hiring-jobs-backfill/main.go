// Command hiring-jobs-backfill publishes the current state of every published or paused job as a
// hiring.job.published.v1 or hiring.job.paused.v1 event, in every region, so a consumer that starts empty (the
// applications service) can build its read model of jobs. It runs once and exits.
//
// It is idempotent: event ids derive from type, job id and the job's version, so running it again writes nothing
// new to the outbox (ON CONFLICT DO NOTHING) and the bus drops a repeat publish. It does not need
// HIRING_EVENTS=on, but it needs NATS_URL and the regional databases, and it drains the outbox before exiting.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"os"

	"github.com/joho/godotenv"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/jobevents"
	"github.com/Angle-HR/server/internal/notifications"
	"github.com/Angle-HR/server/pkg/events"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
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

// companyBackfiller enqueues the live-job events of one company.
type companyBackfiller interface {
	TenantIDs(ctx context.Context) ([]string, error)
	EnqueueLiveJobEvents(ctx context.Context, tenantID string) (int, error)
}

// backfillRegion enqueues events for every company of a region and returns the number of jobs covered. One
// company failing does not stop the others; the error says how many failed.
func backfillRegion(ctx context.Context, log *slog.Logger, region string, s companyBackfiller) (int, error) {
	ids, err := s.TenantIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list companies: %w", err)
	}
	total, failed := 0, 0
	for _, id := range ids {
		n, err := s.EnqueueLiveJobEvents(ctx, id)
		total += n
		if err != nil {
			failed++
			log.Error("hiring jobs backfill: company failed", "region", region, "company", id, "error", err)
		}
	}
	if failed > 0 {
		return total, fmt.Errorf("%d of %d companies failed", failed, len(ids))
	}
	return total, nil
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

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		return errors.New("NATS_URL is required")
	}
	regionConfigs, err := dbrouter.LoadConfigsFromEnv()
	if err != nil {
		return fmt.Errorf("load regional database config: %w", err)
	}
	router, err := dbrouter.New(ctx, regionConfigs)
	if err != nil {
		return fmt.Errorf("connect regional databases: %w", err)
	}
	defer router.Close()
	bus, err := natsbus.Connect(notifications.BusConfig(natsURL, "hiring-jobs-backfill"))
	if err != nil {
		return err
	}
	defer func() { _ = bus.Close() }()

	failed := false
	pools := map[string]outbox.DB{}
	for _, reg := range dbrouter.Regions() {
		pool, err := router.DB(reg)
		if err != nil {
			slogger.Warn("hiring jobs backfill: region skipped", "region", string(reg), "error", err)
			continue
		}
		pools[string(reg)] = pool
		n, err := backfillRegion(ctx, slogger, string(reg), &hiringstore.Store{DB: pool, Region: string(reg)})
		attrs := []any{"region", string(reg), "jobs_covered", n}
		if err != nil {
			failed = true
			slogger.Error("hiring jobs backfill failed", append(attrs, "error", err)...)
			continue
		}
		slogger.Info("hiring jobs backfill done", attrs...)
	}
	if err := jobevents.Drain(ctx, slogger, events.Publisher(bus), pools); err != nil {
		return fmt.Errorf("hiring jobs backfill: drain outbox: %w", err)
	}
	if failed {
		return errors.New("hiring jobs backfill: one or more regions failed")
	}
	return nil
}
