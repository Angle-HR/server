// Command hiring-outbox sends the emails the hiring code left in each region's outbox: application received,
// application decided, and the retention notice to companies. It runs once and exits, so schedule it every
// minute or two. It uses the same SMTP settings as email-worker (SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASSWORD,
// SMTP_FROM, SMTP_FROM_NAME).
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
	"github.com/Angle-HR/server/internal/hiring/outbox"
	"github.com/Angle-HR/server/internal/mailer"
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

	smtpPort := os.Getenv("SMTP_PORT")
	if smtpPort == "" {
		smtpPort = "587"
	}
	if os.Getenv("SMTP_HOST") == "" {
		return errors.New("SMTP_HOST is required")
	}
	m, err := mailer.New(mailer.Config{
		Host: os.Getenv("SMTP_HOST"), Port: smtpPort, User: os.Getenv("SMTP_USER"), Password: os.Getenv("SMTP_PASSWORD"),
		From: os.Getenv("SMTP_FROM"), FromName: os.Getenv("SMTP_FROM_NAME"), AppURL: os.Getenv("APP_URL"),
		AdminAppURL: os.Getenv("ADMIN_APP_URL"), Logger: slogger,
	})
	if err != nil {
		return fmt.Errorf("initialize mailer: %w", err)
	}

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
			slogger.Warn("hiring outbox: region skipped", "region", string(reg), "error", err)
			continue
		}
		store := &hiringstore.Store{DB: pool}
		w := &outbox.Worker{Tenants: store.TenantIDs, Store: store, Sender: m, Logger: slogger}
		res, err := w.Run(ctx)
		attrs := []any{"region", string(reg), "sent", res.Sent, "retry", res.Retry, "failed", res.Failed}
		if err != nil {
			failed = true
			slogger.Error("hiring outbox failed", append(attrs, "error", err)...)
			continue
		}
		slogger.Info("hiring outbox done", attrs...)
	}
	if failed {
		return errors.New("hiring outbox: one or more regions failed")
	}
	return nil
}
