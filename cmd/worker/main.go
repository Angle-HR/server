package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/worker"
	"github.com/Angle-HR/server/pkg/logger"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
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
	_ = godotenv.Load()

	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development"
	}

	slogLogger := logger.New(appEnv)
	ctx := context.Background()

	dbURL := os.Getenv("DB_URL_GLOBAL")
	if dbURL == "" {
		return errors.New("DB_URL_GLOBAL is required")
	}

	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	if smtpPort == "" {
		smtpPort = "587"
	}
	smtpUser := os.Getenv("SMTP_USER")
	smtpPassword := os.Getenv("SMTP_PASSWORD")
	smtpFrom := os.Getenv("SMTP_FROM")
	appURL := os.Getenv("APP_URL")

	if smtpHost == "" {
		slogLogger.Warn("SMTP_HOST is not set; emails may fail to deliver")
	}
	if appURL == "" {
		slogLogger.Warn("APP_URL is not set; waitlist email links will be invalid")
	}

	slogLogger.Info("smtp configuration loaded",
		"host", smtpHost,
		"port", smtpPort,
		"from", smtpFrom,
		"user_set", smtpUser != "",
		"password_set", smtpPassword != "",
		"app_url", appURL,
	)

	m, err := mailer.New(mailer.Config{
		Host:     smtpHost,
		Port:     smtpPort,
		User:     smtpUser,
		Password: smtpPassword,
		From:     smtpFrom,
		AppURL:   appURL,
		Logger:   slogLogger,
	})
	if err != nil {
		return fmt.Errorf("initialize mailer: %w", err)
	}

	dbPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to global database: %w", err)
	}
	defer dbPool.Close()

	slogLogger.Info("applying Fluvio schema migrations...")
	if err := queue.Migrate(ctx, dbPool); err != nil {
		return fmt.Errorf("apply Fluvio migrations: %w", err)
	}
	slogLogger.Info("Fluvio schema migrations applied successfully")

	workers := fluvio.NewWorkers()
	fluvio.AddWorker(workers, &worker.EmailWorker{Mailer: m, Logger: slogLogger})

	fluvioClient, err := queue.NewWorkerClient(dbPool, workers, slogLogger)
	if err != nil {
		return fmt.Errorf("create Fluvio client: %w", err)
	}

	slogLogger.Info("starting Fluvio background email worker...")
	if err := fluvioClient.Start(ctx); err != nil {
		return fmt.Errorf("start Fluvio client: %w", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	sig := <-stop
	slogLogger.Info("shutdown signal received, stopping background worker...", "signal", sig.String())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := fluvioClient.StopContext(shutdownCtx); err != nil {
		return fmt.Errorf("stop Fluvio client: %w", err)
	}

	slogLogger.Info("worker stopped successfully")
	return nil
}
