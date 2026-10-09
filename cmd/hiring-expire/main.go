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

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/expiry"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
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

	failed := false
	for _, reg := range dbrouter.Regions() {
		pool, err := router.DB(reg)
		if err != nil {
			slogger.Warn("hiring expire: region skipped", "region", string(reg), "error", err)
			continue
		}
		store := &hiringstore.Store{DB: pool}
		sweeper := &expiry.Sweeper{
			Tenants: store.TenantIDs,
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
	if failed {
		return errors.New("hiring expire: one or more regions failed")
	}
	return nil
}
