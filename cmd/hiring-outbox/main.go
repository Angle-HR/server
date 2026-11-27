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
	"github.com/Angle-HR/server/internal/hiring/appstore"
	"github.com/Angle-HR/server/internal/hiring/jobsource"
	"github.com/Angle-HR/server/internal/hiring/outbox"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/notifications"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
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

	var sender outbox.Sender
	if os.Getenv("NOTIFICATIONS_TRANSPORT") == "events" {
		bus, berr := natsbus.Connect(notifications.BusConfig(os.Getenv("NATS_URL"), "hiring-outbox"))
		if berr != nil {
			return berr
		}
		defer func() { _ = bus.Close() }()
		sender = notifications.BusSender{Pub: bus, Source: "/jobs/hiring-outbox"}
		slogger.Info("hiring outbox: sending through the events transport")
	}

	smtpPort := os.Getenv("SMTP_PORT")
	if smtpPort == "" {
		smtpPort = "587"
	}
	var appURL string
	if sender == nil {
		if os.Getenv("SMTP_HOST") == "" {
			return errors.New("SMTP_HOST is required")
		}
		var adminAppURL string
		var uerr error
		appURL, adminAppURL, uerr = mailer.AppLinkURLs(os.Getenv("APP_ENV"), os.Getenv("APP_URL"), os.Getenv("ADMIN_APP_URL"))
		if uerr != nil {
			return uerr
		}
		m, merr := mailer.New(mailer.Config{
			Host: os.Getenv("SMTP_HOST"), Port: smtpPort, User: os.Getenv("SMTP_USER"), Password: os.Getenv("SMTP_PASSWORD"),
			From: os.Getenv("SMTP_FROM"), FromName: os.Getenv("SMTP_FROM_NAME"), AppURL: appURL,
			AdminAppURL: adminAppURL, Logger: slogger,
		})
		if merr != nil {
			return fmt.Errorf("initialize mailer: %w", merr)
		}
		sender = m
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
		store := &appstore.Store{DB: pool}
		jobs := jobsource.NewRegional(pool)
		w := &outbox.Worker{Tenants: jobs.TenantIDs, Store: store, Sender: sender, Logger: slogger, LinkBase: appURL}
		res, err := w.Run(ctx)
		// Messages people wrote to candidates, including scheduled ones that are now due.
		mres, merr := w.RunMessages(ctx, store)
		res.Sent, res.Retry, res.Failed = res.Sent+mres.Sent, res.Retry+mres.Retry, res.Failed+mres.Failed
		if err == nil {
			err = merr
		}
		attrs := []any{"region", string(reg), "sent", res.Sent, "retry", res.Retry, "failed", res.Failed, "messages_sent", mres.Sent}
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
