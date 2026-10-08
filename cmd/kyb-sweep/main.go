// Command kyb-sweep sends the company verification (KYB) re-engagement emails and
// flags accounts left unresolved for 30 days. It runs once and exits, so schedule
// it (for example hourly) with cron or the platform's job scheduler. It never deletes anything.
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
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybnotify"
	"github.com/Angle-HR/server/internal/kyb/kybstore"
	"github.com/Angle-HR/server/internal/queue"
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

	enqueuer, err := queue.NewInsertClient(global)
	if err != nil {
		return fmt.Errorf("create Fluvio client: %w", err)
	}

	failed := false
	for _, reg := range dbrouter.Regions() {
		pool, err := router.DB(reg)
		if err != nil {
			slogger.Warn("kyb sweep: region skipped", "region", string(reg), "error", err)
			continue
		}
		sweeper := &kyb.Sweeper{
			Store:    &kybstore.Store{DB: pool},
			Notifier: &kybnotify.Notifier{Regional: pool, Global: global, Enqueuer: enqueuer},
			Logger:   slogger,
		}
		res, err := sweeper.Run(ctx)
		attrs := []any{
			"region", string(reg), "nudged", res.Nudged, "flagged_for_deletion", res.Flagged, "errors", res.Errors,
		}
		if err != nil {
			failed = true
			slogger.Error("kyb sweep failed", append(attrs, "error", err)...)
			continue
		}
		slogger.Info("kyb sweep done", attrs...)
	}
	if failed {
		return errors.New("kyb sweep: one or more regions failed")
	}
	return nil
}
