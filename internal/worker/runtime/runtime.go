package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/pkg/logger"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	fluvio "github.com/software78/fluvio"
)

// Run connects to the global database, applies Fluvio migrations, registers
// workers, and blocks until a shutdown signal is received.
func Run(workerName string, queues map[string]fluvio.QueueConfig, register func(*fluvio.Workers)) error {
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
	register(workers)

	fluvioClient, err := queue.NewWorkerClient(dbPool, queues, workers, slogLogger)
	if err != nil {
		return fmt.Errorf("create Fluvio client: %w", err)
	}

	slogLogger.Info("starting Fluvio background worker...", "worker", workerName)
	if err := fluvioClient.Start(ctx); err != nil {
		return fmt.Errorf("start Fluvio client: %w", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	sig := <-stop
	slogLogger.Info("shutdown signal received, stopping background worker...",
		"worker", workerName,
		"signal", sig.String(),
	)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := fluvioClient.StopContext(shutdownCtx); err != nil {
		return fmt.Errorf("stop Fluvio client: %w", err)
	}

	slogLogger.Info("worker stopped successfully", "worker", workerName)
	return nil
}
