package eventstest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGPool returns a pool on a fresh schema of the Postgres named by EVENTS_PG_URL with the events tables created
// from db/migrations/000018_events_outbox_inbox.sql. The test is skipped when the variable is not set.
func PGPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("EVENTS_PG_URL")
	if url == "" {
		t.Skip("EVENTS_PG_URL not set; skipping the Postgres tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "evt_" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "_")
	if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE; CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		admin.Close()
	})
	_, here, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "..", "db", "migrations",
		"000018_events_outbox_inbox.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	up = up[strings.Index(up, "-- +goose Up"):strings.Index(up, "-- +goose Down")]
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return pool
}
