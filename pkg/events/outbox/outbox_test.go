package outbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/eventstest"
	"github.com/Angle-HR/server/pkg/events/memory"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// Run with EVENTS_PG_URL=postgres://user:pass@localhost:25432/db (see docs/microservices/events.md).

type flaky struct {
	mu   sync.Mutex
	fail map[string]bool
	got  []events.Event
}

func (f *flaky) Publish(_ context.Context, e events.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[e.Type] {
		return errors.New("broker down")
	}
	f.got = append(f.got, e)
	return nil
}

func mk(t *testing.T, typ, key string) events.Event {
	ev, err := events.NewJSON("/test", typ, map[string]string{"k": key})
	if err != nil {
		t.Fatal(err)
	}
	return ev.WithPartitionKey(key)
}

func TestEnqueueIsTransactional(t *testing.T) {
	pool := eventstest.PGPool(t)
	ctx := context.Background()
	pub := memory.New()
	relay, _ := outbox.NewRelay(outbox.Config{DB: pool, Publisher: pub})

	tx, _ := pool.Begin(ctx)
	if err := outbox.Enqueue(ctx, tx, mk(t, "a.rolled.back", "")); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)

	tx, _ = pool.Begin(ctx)
	kept := mk(t, "a.committed", "")
	if err := outbox.Enqueue(ctx, tx, kept); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(ctx, tx, kept); err != nil { // same id: no-op
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	res, err := relay.RunOnce(ctx)
	if err != nil || res.Published != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got := pub.Published(); len(got) != 1 || got[0].ID != kept.ID {
		t.Fatalf("published %+v", got)
	}
	if res, _ = relay.RunOnce(ctx); res.Published != 0 {
		t.Errorf("sent row published again: %+v", res)
	}
	n, err := outbox.Purge(ctx, pool, time.Now().Add(time.Minute))
	if err != nil || n != 1 {
		t.Errorf("purge = %d, %v", n, err)
	}
}

func TestTraceContextCapturedAtEnqueue(t *testing.T) {
	pool := eventstest.PGPool(t)
	tp := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx := events.WithTraceParent(context.Background(), tp)
	if err := outbox.Enqueue(ctx, pool, mk(t, "a.traced", "")); err != nil {
		t.Fatal(err)
	}
	f := &flaky{fail: map[string]bool{}}
	relay, _ := outbox.NewRelay(outbox.Config{DB: pool, Publisher: f})
	if _, err := relay.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.got[0].TraceParent != tp {
		t.Fatalf("got %+v", f.got)
	}
}

func TestBackoffThenDeadAndHooks(t *testing.T) {
	pool := eventstest.PGPool(t)
	ctx := context.Background()
	f := &flaky{fail: map[string]bool{"a.bad": true}}
	now := time.Now().Add(time.Minute) // rows carry the database clock
	var infos []outbox.Info
	var batches int
	relay, _ := outbox.NewRelay(outbox.Config{
		DB: pool, Publisher: f, MaxAttempts: 3, Backoff: []time.Duration{time.Minute},
		Now: func() time.Time { return now },
		Hooks: outbox.Hooks{
			OnAttempt: func(i outbox.Info) { infos = append(infos, i) },
			OnBatch:   func(outbox.Result, time.Duration) { batches++ },
		},
	})
	_ = outbox.Enqueue(ctx, pool, mk(t, "a.bad", ""))

	res, _ := relay.RunOnce(ctx)
	if res.Retry != 1 {
		t.Fatalf("first: %+v", res)
	}
	if res, _ = relay.RunOnce(ctx); res.Retry+res.Dead != 0 {
		t.Fatalf("retried before the backoff elapsed: %+v", res)
	}
	now = now.Add(2 * time.Minute)
	if res, _ = relay.RunOnce(ctx); res.Retry != 1 {
		t.Fatalf("second: %+v", res)
	}
	now = now.Add(2 * time.Minute)
	if res, _ = relay.RunOnce(ctx); res.Dead != 1 {
		t.Fatalf("third must give up: %+v", res)
	}
	now = now.Add(time.Hour)
	if res, _ = relay.RunOnce(ctx); res.Retry+res.Dead+res.Published != 0 {
		t.Fatalf("dead row retried: %+v", res)
	}
	var attempts int
	var reason string
	_ = pool.QueryRow(ctx, `SELECT attempts, last_error FROM events_outbox`).Scan(&attempts, &reason)
	if attempts != 3 || reason != "broker down" {
		t.Errorf("attempts=%d reason=%q", attempts, reason)
	}
	if len(infos) != 3 || !infos[2].Dead || infos[0].Err == nil || batches != 5 {
		t.Errorf("hooks: %+v batches=%d", infos, batches)
	}
}

func TestFailedKeyBlocksLaterEventsOfSameKey(t *testing.T) {
	pool := eventstest.PGPool(t)
	ctx := context.Background()
	f := &flaky{fail: map[string]bool{"a.first": true}}
	now := time.Now().Add(time.Minute) // rows carry the database clock
	relay, _ := outbox.NewRelay(outbox.Config{DB: pool, Publisher: f, Now: func() time.Time { return now }})
	_ = outbox.Enqueue(ctx, pool, mk(t, "a.first", "k1"))
	_ = outbox.Enqueue(ctx, pool, mk(t, "a.second", "k1"))
	_ = outbox.Enqueue(ctx, pool, mk(t, "a.other", "k2"))

	if _, err := relay.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.got[0].Type != "a.other" {
		t.Fatalf("same key published past a failure: %+v", f.got)
	}
	if _, err := relay.RunOnce(ctx); err != nil { // first still backing off: second must wait too
		t.Fatal(err)
	}
	if len(f.got) != 1 {
		t.Fatalf("later event of the key published while the first backs off: %+v", f.got)
	}
	f.fail = map[string]bool{}
	now = now.Add(time.Hour)
	if _, err := relay.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 3 || f.got[1].Type != "a.first" || f.got[2].Type != "a.second" {
		t.Fatalf("order after recovery: %+v", f.got)
	}
}

func TestConcurrentRelaysDoNotDuplicate(t *testing.T) {
	pool := eventstest.PGPool(t)
	ctx := context.Background()
	pub := memory.New()
	for i := 0; i < 40; i++ {
		_ = outbox.Enqueue(ctx, pool, mk(t, "a.many", ""))
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		r, _ := outbox.NewRelay(outbox.Config{DB: pool, Publisher: pub, BatchSize: 5})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := r.RunOnce(ctx); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	var sent int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM events_outbox WHERE sent_at IS NOT NULL`).Scan(&sent)
	if sent != 40 {
		t.Errorf("sent rows = %d, want 40", sent)
	}
	if n := len(pub.Published()); n != 40 {
		t.Errorf("published %d, want 40 (a row was taken by two relays)", n)
	}
}
