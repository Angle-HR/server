package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/worker"
	"github.com/Angle-HR/server/pkg/logger"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	// Load env file if it exists (useful for local development).
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

	if smtpHost == "" {
		slogLogger.Warn("SMTP_HOST is not set; emails may fail to deliver")
	}

	m, err := mailer.New(mailer.Config{
		Host:     smtpHost,
		Port:     smtpPort,
		User:     smtpUser,
		Password: smtpPassword,
		From:     smtpFrom,
	})
	if err != nil {
		return fmt.Errorf("initialize mailer: %w", err)
	}

	dbPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to global database: %w", err)
	}
	defer dbPool.Close()

	// Apply River schema migrations programmatically on startup.
	slogLogger.Info("applying River schema migrations...")
	migrator, err := rivermigrate.New(riverpgxv5.New(dbPool), nil)
	if err != nil {
		return fmt.Errorf("create River migrator: %w", err)
	}

	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("apply River migrations: %w", err)
	}
	slogLogger.Info("River schema migrations applied successfully")

	workers := river.NewWorkers()
	river.AddWorker(workers, &worker.EmailWorker{Mailer: m})

	riverClient, err := river.NewClient(riverpgxv5.New(dbPool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 10},
		},
		Workers: workers,
		Logger:  slogLogger,
	})
	if err != nil {
		return fmt.Errorf("create River client: %w", err)
	}

	slogLogger.Info("starting River background email worker...")
	if err := riverClient.Start(ctx); err != nil {
		return fmt.Errorf("start River client: %w", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	sig := <-stop
	slogLogger.Info("shutdown signal received, stopping background worker...", "signal", sig.String())

	// Stop workers gracefully
	if err := riverClient.Stop(ctx); err != nil {
		return fmt.Errorf("stop River client: %w", err)
	}

	slogLogger.Info("worker stopped successfully")
	return nil
}
