package companydeletion

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The database tests need a Postgres whose user may CREATE DATABASE. Point COMPANYDELETION_PG_URL (or EVENTS_PG_URL, as in
// pkg/events) at any database of it, for example
//
//	COMPANYDELETION_PG_URL=postgres://openhr:change-me@localhost:5432/postgres?sslmode=disable go test ./internal/applicationsvc/jobmodel
//
// A template database with every db/migrations file applied is built once; each test gets a copy and drops it.
// Without the variable these tests are skipped.

var (
	tplOnce sync.Once
	tplName string
	tplErr  error
	dbSeq   int
	dbMu    sync.Mutex
)

func adminURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv("COMPANYDELETION_PG_URL")
	if u == "" {
		u = os.Getenv("EVENTS_PG_URL")
	}
	if u == "" {
		t.Skip("COMPANYDELETION_PG_URL (or EVENTS_PG_URL) not set; skipping the Postgres tests")
	}
	return u
}

func withDB(t testing.TB, base, name string) string {
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func migrationsDir() string {
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "db", "migrations")
}

func upSection(sql string) string {
	i := strings.Index(sql, "-- +goose Up")
	j := strings.Index(sql, "-- +goose Down")
	if i < 0 {
		return sql
	}
	if j < 0 {
		j = len(sql)
	}
	return sql[i:j]
}

func buildTemplate(ctx context.Context, base string) (string, error) {
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		return "", err
	}
	defer admin.Close()
	name := fmt.Sprintf("cd_tpl_%d", os.Getpid())
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name); err != nil {
		return "", err
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		return "", err
	}
	u, _ := url.Parse(base)
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		return "", err
	}
	defer pool.Close()
	files, err := filepath.Glob(filepath.Join(migrationsDir(), "0*.sql"))
	if err != nil {
		return "", err
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		if _, err := pool.Exec(ctx, upSection(string(raw))); err != nil {
			return "", fmt.Errorf("apply %s: %w", filepath.Base(f), err)
		}
	}
	return name, nil
}

// newDB returns a pool on a fresh database with every migration applied.
func newDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	base := adminURL(t)
	ctx := context.Background()
	tplOnce.Do(func() { tplName, tplErr = buildTemplate(ctx, base) })
	if tplErr != nil {
		t.Fatalf("build template database: %v", tplErr)
	}
	dbMu.Lock()
	dbSeq++
	name := fmt.Sprintf("cd_t%d_%d", os.Getpid(), dbSeq)
	dbMu.Unlock()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, name, tplName)); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(withDB(t, base, name))
	if err != nil {
		t.Fatal(err)
	}
	// The regional search_path of internal/dbrouter, so unqualified names behave as in production.
	cfg.ConnConfig.RuntimeParams["search_path"] = "accounts,waitlist,hiring,public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		admin.Close()
	})
	return pool
}

func TestMain(m *testing.M) {
	code := m.Run()
	if tplName != "" {
		ctx := context.Background()
		if admin, err := pgxpool.New(ctx, adminURLNoT()); err == nil {
			_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+tplName+` WITH (FORCE)`)
			admin.Close()
		}
	}
	os.Exit(code)
}

func adminURLNoT() string {
	if u := os.Getenv("COMPANYDELETION_PG_URL"); u != "" {
		return u
	}
	return os.Getenv("EVENTS_PG_URL")
}
