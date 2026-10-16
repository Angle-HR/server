// Command hiring-uploads-sweep deletes candidate uploads that no application used within their time (24 hours):
// the objects in storage, then the rows. It runs once and exits, so schedule it (for example hourly) with cron
// or the platform's job scheduler. Files that belong to an application are never touched; the retention task
// deletes those when their retention date passes.
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
	"github.com/Angle-HR/server/internal/hiring/uploads"
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
			slogger.Warn("hiring uploads sweep: region skipped", "region", string(reg), "error", err)
			continue
		}
		client, err := router.R2(reg)
		if err != nil {
			slogger.Warn("hiring uploads sweep: region has no object storage, skipped", "region", string(reg), "error", err)
			continue
		}
		bucket, err := r2.New(client, router.Bucket(reg))
		if err != nil {
			slogger.Warn("hiring uploads sweep: region skipped", "region", string(reg), "error", err)
			continue
		}
		store := &hiringstore.Store{DB: pool}
		sweeper := &uploads.Sweeper{Tenants: store.TenantIDs, Store: store, Storage: bucket, Logger: slogger}
		res, err := sweeper.Run(ctx)
		attrs := []any{"region", string(reg), "companies", res.Companies, "deleted", res.Deleted, "errors", res.Errors}
		if err != nil {
			failed = true
			slogger.Error("hiring uploads sweep failed", append(attrs, "error", err)...)
			continue
		}
		slogger.Info("hiring uploads sweep done", attrs...)
	}
	if failed {
		return errors.New("hiring uploads sweep: one or more regions failed")
	}
	return nil
}
