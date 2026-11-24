// Command notifications is the notifications service: it consumes notifications.email.* events from the bus and
// sends the emails through the mailer. It has no inbound business API, only health endpoints.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/notifications"
	"github.com/Angle-HR/server/pkg/db"
	"github.com/Angle-HR/server/pkg/events"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/logger"
	"github.com/Angle-HR/server/pkg/platform/secrets"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func run() error {
	appEnv := env("APP_ENV", "development")
	slog := logger.New(appEnv, os.Getenv("LOG_LEVEL"))
	var sec secrets.Reader

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	appURL, adminAppURL, err := mailer.AppLinkURLs(appEnv, os.Getenv("APP_URL"), os.Getenv("ADMIN_APP_URL"))
	if err != nil {
		return err
	}
	m, err := mailer.New(mailer.Config{
		Host:        os.Getenv("SMTP_HOST"),
		Port:        env("SMTP_PORT", "587"),
		User:        os.Getenv("SMTP_USER"),
		Password:    sec.Get("SMTP_PASSWORD"),
		From:        os.Getenv("SMTP_FROM"),
		FromName:    os.Getenv("SMTP_FROM_NAME"),
		AppURL:      appURL,
		AdminAppURL: adminAppURL,
		Logger:      slog,
	})
	if err != nil {
		return fmt.Errorf("initialize mailer: %w", err)
	}

	// The inbox lives in the service's own database. Until a dedicated one is provisioned that is the global DB.
	dsn := env("NOTIFICATIONS_DB_URL", sec.Get("DB_URL_GLOBAL"))
	if dsn == "" {
		return errors.New("NOTIFICATIONS_DB_URL or DB_URL_GLOBAL is required")
	}
	pool, err := db.NewGlobalPool(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	bus, err := natsbus.Connect(notifications.BusConfig(env("NATS_URL", "nats://localhost:4222"), "notifications"))
	if err != nil {
		return err
	}
	defer func() { _ = bus.Close() }()

	sub, err := bus.Subscribe(ctx, notifications.Consumer, []string{notifications.SubjectFilter},
		notifications.Handler(pool, m), events.WithConcurrency(4))
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
	mux.HandleFunc("/healthz", ok)
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		ok(w, r)
	})
	srv := &http.Server{Addr: env("HTTP_ADDR", ":8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("health server failed", "error", err)
			stop()
		}
	}()
	slog.Info("notifications service started", "consumer", notifications.Consumer)

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	return sub.Stop(shutdown)
}
