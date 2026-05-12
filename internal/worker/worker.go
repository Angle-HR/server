// Package worker runs the Asynq background job processor.
package worker

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"

	"github.com/Angle-HR/server/internal/jobs"
	"github.com/Angle-HR/server/pkg/config"
	"github.com/Angle-HR/server/pkg/logger"
	"github.com/Angle-HR/server/pkg/queue"
)

const shutdownTimeout = 10 * time.Second

const (
	workerConcurrency   = 10
	criticalQueueWeight = 6
	defaultQueueWeight  = 3
	lowQueueWeight      = 1
)

// Run starts the Asynq worker and blocks until shutdown.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.AppEnv)

	server, err := queue.NewServer(cfg.RedisURL, &asynq.Config{
		Concurrency: workerConcurrency,
		Queues: map[string]int{
			"critical": criticalQueueWeight,
			"default":  defaultQueueWeight,
			"low":      lowQueueWeight,
		},
	})
	if err != nil {
		return fmt.Errorf("create asynq server: %w", err)
	}

	mux := asynq.NewServeMux()
	jobs.Register(mux, log)

	errCh := make(chan error, 1)
	go func() {
		log.Info("worker listening", "env", cfg.AppEnv)
		if runErr := server.Run(mux); runErr != nil {
			errCh <- fmt.Errorf("run asynq server: %w", runErr)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Info("shutdown signal received", "signal", sig.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	server.Shutdown()

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case <-shutdownCtx.Done():
		return fmt.Errorf("shutdown asynq server: %w", shutdownCtx.Err())
	}

	log.Info("worker stopped")
	return nil
}
