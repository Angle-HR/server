// Command hiring-retention deletes applications whose retention period has ended, with their files, encrypted
// special category answers, review tasks and unsent emails, and tells each company what was deleted. It also
// deletes expired reapply blocks and old outbox rows. It runs once and exits, so schedule it (for example
// nightly) with cron or the platform's job scheduler. Run hiring-outbox afterwards, or on its own schedule, to
// deliver the notices it queues.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/joho/godotenv"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/retention"
	"github.com/Angle-HR/server/internal/hiring/uploads/r2"
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

	regionConfigs, err := dbrouter.LoadConfigsFromEnv()
	if err != nil {
		return fmt.Errorf("load regional config: %w", err)
	}
	router, err := dbrouter.New(ctx, regionConfigs)
	if err != nil {
		return fmt.Errorf("connect regions: %w", err)
	}
	defer router.Close()

	failed := false
	for _, reg := range dbrouter.Regions() {
		pool, err := router.DB(reg)
		if err != nil {
			slogger.Warn("hiring retention: region skipped", "region", string(reg), "error", err)
			continue
		}
		// A region without object storage cannot hold uploads, so it has no files to delete either; but
		// deleting rows without removing files is never done, so such a region is skipped.
		client, err := router.R2(reg)
		if err != nil {
			slogger.Warn("hiring retention: region has no object storage, skipped", "region", string(reg), "error", err)
			continue
		}
		bucket, err := r2.New(client, router.Bucket(reg))
		if err != nil {
			slogger.Warn("hiring retention: region skipped", "region", string(reg), "error", err)
			continue
		}
		store := &hiringstore.Store{DB: pool}
		sweeper := &retention.Sweeper{Tenants: store.TenantIDs, Store: store, Storage: bucket, Logger: slogger}
		res, err := sweeper.Run(ctx)
		attrs := []any{
			"region", string(reg), "companies", res.Companies, "shortened", res.Shortened, "deleted", res.Deleted,
			"blocks_deleted", res.Blocks, "notices", res.Emails, "errors", res.Errors,
		}
		if err != nil {
			failed = true
			slogger.Error("hiring retention failed", append(attrs, "error", err)...)
			continue
		}
		slogger.Info("hiring retention done", attrs...)
	}
	if failed {
		return errors.New("hiring retention: one or more regions failed")
	}
	return nil
}
