package jobmodel

import (
	"context"
	"testing"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
)

func TestProjector_Terminal(t *testing.T) {
	// None of these reach the database, so the pool is never asked for.
	p := NewProjector(func(string) (inbox.DB, error) { t.Fatal("database must not be used"); return nil, nil })
	cases := map[string]events.Event{
		"unknown type": ev(t, "hiring.job.deleted.v1", "e1", snap(1, "published", "open")),
		"not json": func() events.Event {
			e := ev(t, TypePublished, "e2", snap(1, "published", "open"))
			e.Data = []byte("{nope")
			return e
		}(),
		"version zero": ev(t, TypePublished, "e3", snap(0, "published", "open")),
		"no job id": ev(t, TypePublished, "e4", func() map[string]any {
			s := snap(1, "published", "open")
			delete(s, "job_id")
			return s
		}()),
		"no region": func() events.Event {
			s := snap(1, "published", "open")
			delete(s, "region")
			e := ev(t, TypePublished, "e5", s)
			e.Region = ""
			return e
		}(),
	}
	for name, e := range cases {
		err := p.Handle(context.Background(), e)
		if err == nil || !events.IsTerminal(err) {
			t.Errorf("%s: want a terminal error, got %v", name, err)
		}
	}
}

func TestProjector_UnknownRegionIsTerminal(t *testing.T) {
	p := NewProjector(func(string) (inbox.DB, error) { return nil, ErrUnknownRegion })
	s := snap(1, "published", "open")
	s["region"] = "mars"
	err := p.Handle(context.Background(), ev(t, TypePublished, "e1", s))
	if err == nil || !events.IsTerminal(err) {
		t.Fatalf("want a terminal error, got %v", err)
	}
}

func TestProjector_DatabaseErrorIsRetried(t *testing.T) {
	p := NewProjector(func(string) (inbox.DB, error) { return nil, context.DeadlineExceeded })
	err := p.Handle(context.Background(), ev(t, TypePublished, "e1", snap(1, "published", "open")))
	if err == nil || events.IsTerminal(err) {
		t.Fatalf("want a retryable error, got %v", err)
	}
}

func TestProjector_ConsumerAndSubject(t *testing.T) {
	if Consumer != "applications-jobs" || SubjectFilter != "hiring.job.>" {
		t.Fatal("consumer name and subject filter are part of the deployment contract")
	}
}

func TestProjector_Postgres(t *testing.T) {
	pool := newDB(t)
	p := NewProjector(pools(pool))
	ctx := context.Background()

	// published v1 creates the row with the form.
	mustHandle(t, p, ev(t, TypePublished, "e1", snap(1, "published", "open")))
	if v, st, state := dbVersion(t, pool); v != 1 || st != "published" || state != "open" {
		t.Fatalf("after published: %d %s %s", v, st, state)
	}
	var markets []string
	var ver int
	var closing string
	if err := pool.QueryRow(ctx, `SELECT markets, form_version, to_char(closing_date,'YYYY-MM-DD') FROM hiring.app_jobs`).Scan(&markets, &ver, &closing); err != nil {
		t.Fatal(err)
	}
	if jsonOf(markets) != `["GB","NG"]` || ver != 1 || closing != "2099-01-01" {
		t.Fatalf("markets=%v form_version=%d closing=%s", markets, ver, closing)
	}

	// paused v2, resumed v3, closed v4 in order.
	mustHandle(t, p, ev(t, TypePaused, "e2", snap(2, "paused", "paused")))
	if v, st, _ := dbVersion(t, pool); v != 2 || st != "paused" {
		t.Fatalf("after paused: %d %s", v, st)
	}
	mustHandle(t, p, ev(t, TypeResumed, "e3", snap(3, "published", "open")))
	mustHandle(t, p, ev(t, TypeClosed, "e4", snap(4, "closed", "closed")))
	if v, st, state := dbVersion(t, pool); v != 4 || st != "closed" || state != "closed" {
		t.Fatalf("after closed: %d %s %s", v, st, state)
	}
	var closedAt *string
	_ = pool.QueryRow(ctx, `SELECT closed_at::text FROM hiring.app_jobs`).Scan(&closedAt)
	if closedAt == nil {
		t.Fatal("closed_at must be set for a closed job")
	}

	// duplicate (same event id) is acknowledged without effect, even when its content differs.
	dup := snap(4, "published", "open")
	mustHandle(t, p, ev(t, TypeClosed, "e4", dup))
	// stale (older version, new event id) is ignored.
	mustHandle(t, p, ev(t, TypeUpdated, "e5", snap(2, "published", "open")))
	// same version, different event id: not newer, ignored.
	mustHandle(t, p, ev(t, TypeUpdated, "e6", snap(4, "published", "open")))
	if v, st, _ := dbVersion(t, pool); v != 4 || st != "closed" {
		t.Fatalf("stale and duplicate events changed the row: %d %s", v, st)
	}

	// reopen (published v5) applies.
	mustHandle(t, p, ev(t, TypePublished, "e7", snap(5, "published", "open")))
	if v, st, state := dbVersion(t, pool); v != 5 || st != "published" || state != "open" {
		t.Fatalf("after reopen: %d %s %s", v, st, state)
	}
	if err := pool.QueryRow(ctx, `SELECT closed_at::text FROM hiring.app_jobs`).Scan(&closedAt); err != nil || closedAt != nil {
		t.Fatalf("closed_at must be cleared on reopen: %v %v", closedAt, err)
	}

	// updated v6 replaces details and rules.
	u := snap(6, "published", "open")
	u["title"] = "Senior Engineer"
	u["rules"] = []any{}
	mustHandle(t, p, ev(t, TypeUpdated, "e8", u))
	var title, rules string
	_ = pool.QueryRow(ctx, `SELECT title, rules::text FROM hiring.app_jobs`).Scan(&title, &rules)
	if title != "Senior Engineer" || rules != "[]" {
		t.Fatalf("update not applied: %q %s", title, rules)
	}
}

func TestProjector_OutOfOrderArrival(t *testing.T) {
	pool := newDB(t)
	p := NewProjector(pools(pool))
	// closed(3) arrives before paused(2) and published(1): only the newest state survives.
	mustHandle(t, p, ev(t, TypeClosed, "c3", snap(3, "closed", "closed")))
	mustHandle(t, p, ev(t, TypePaused, "c2", snap(2, "paused", "paused")))
	mustHandle(t, p, ev(t, TypePublished, "c1", snap(1, "published", "open")))
	if v, st, _ := dbVersion(t, pool); v != 3 || st != "closed" {
		t.Fatalf("got %d %s", v, st)
	}
	// All three frozen forms share a version here, so one history row exists.
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM hiring.app_job_forms`).Scan(&n)
	if n != 1 {
		t.Fatalf("form history rows = %d", n)
	}
}

func TestProjector_KeepsEveryFormVersion(t *testing.T) {
	pool := newDB(t)
	p := NewProjector(pools(pool))
	mustHandle(t, p, ev(t, TypePublished, "f1", snap(1, "published", "open")))
	s := snap(2, "published", "open")
	s["form"] = map[string]any{"id": tForm, "version": 2, "schema": map[string]any{"questions": []any{}}}
	mustHandle(t, p, ev(t, TypePublished, "f2", s))
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM hiring.app_job_forms`).Scan(&n)
	if n != 2 {
		t.Fatalf("form versions kept = %d, want 2", n)
	}
}

func TestProjector_NeverPublishedJobHasNoForm(t *testing.T) {
	pool := newDB(t)
	p := NewProjector(pools(pool))
	s := snap(3, "draft", "closed")
	delete(s, "form")
	mustHandle(t, p, ev(t, TypeClosed, "d1", s))
	var formID *string
	if err := pool.QueryRow(context.Background(), `SELECT form_id FROM hiring.app_jobs`).Scan(&formID); err != nil || formID != nil {
		t.Fatalf("form_id = %v err=%v", formID, err)
	}
}
