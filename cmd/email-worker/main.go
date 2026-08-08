package main

import (
	"fmt"
	"log"
	"os"

	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/worker"
	"github.com/Angle-HR/server/internal/worker/runtime"
	"github.com/Angle-HR/server/pkg/logger"
	fluvio "github.com/software78/fluvio"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development"
	}
	slogLogger := logger.New(appEnv, os.Getenv("LOG_LEVEL"))

	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	if smtpPort == "" {
		smtpPort = "587"
	}
	smtpUser := os.Getenv("SMTP_USER")
	smtpPassword := os.Getenv("SMTP_PASSWORD")
	smtpFrom := os.Getenv("SMTP_FROM")
	smtpFromName := os.Getenv("SMTP_FROM_NAME")
	appURL := os.Getenv("APP_URL")
	adminAppURL := os.Getenv("ADMIN_APP_URL")

	if smtpHost == "" {
		slogLogger.Warn("SMTP_HOST is not set; emails may fail to deliver")
	}
	if appURL == "" {
		slogLogger.Warn("APP_URL is not set; waitlist email links will be invalid")
	}
	if adminAppURL == "" {
		slogLogger.Warn("ADMIN_APP_URL is not set; admin invite email links will be invalid")
	}

	slogLogger.Info("smtp configuration loaded",
		"host", smtpHost,
		"port", smtpPort,
		"from", smtpFrom,
		"from_name", smtpFromName,
		"user_set", smtpUser != "",
		"password_set", smtpPassword != "",
		"app_url", appURL,
		"admin_app_url", adminAppURL,
	)

	m, err := mailer.New(mailer.Config{
		Host:        smtpHost,
		Port:        smtpPort,
		User:        smtpUser,
		Password:    smtpPassword,
		From:        smtpFrom,
		FromName:    smtpFromName,
		AppURL:      appURL,
		AdminAppURL: adminAppURL,
		Logger:      slogLogger,
	})
	if err != nil {
		return fmt.Errorf("initialize mailer: %w", err)
	}

	return runtime.Run("email-worker", queue.EmailWorkerQueues(), func(workers *fluvio.Workers) {
		fluvio.AddWorker(workers, &worker.EmailWorker{Mailer: m, Logger: slogLogger})
	})
}
