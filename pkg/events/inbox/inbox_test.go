package inbox_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/eventstest"
	"github.com/Angle-HR/server/pkg/events/inbox"
	"github.com/Angle-HR/server/pkg/events/memory"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

func TestRedeliveryIsNoOpAndFailureRollsBack(t *testing.T) {
	pool := eventstest.PGPool(t)
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `CREATE TABLE effects (n INT)`)

	var fail atomic.Bool
	fail.Store(true)
	h := inbox.Handle(pool, "svc-a", func(ctx context.Context, tx pgx.Tx, _ events.Event) error {
		if _, err := tx.Exec(ctx, `INSERT INTO effects VALUES (1)`); err != nil {
			return err
		}
		if fail.Load() {
			return errors.New("boom")
		}
		return nil
	})
	ev, _ := events.NewJSON("/t", "a.b", 1)
	count := func() int {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM effects`).Scan(&n)
		return n
	}
	if err := h(ctx, ev); err == nil {
		t.Fatal("want error")
	}
	if count() != 0 {
		t.Fatal("failed handler's writes were not rolled back")
	}
	if seen, _ := inbox.Seen(ctx, pool, "svc-a", ev.ID); seen {
		t.Fatal("failed event recorded as processed")
	}
	fail.Store(false)
	for i := 0; i < 3; i++ { // redelivered three times
		if err := h(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatalf("effect applied %d times, want 1", count())
	}
	// Another consumer has its own record.
	h2 := inbox.Handle(pool, "svc-b", func(ctx context.Context, tx pgx.Tx, _ events.Event) error {
		_, err := tx.Exec(ctx, `INSERT INTO effects VALUES (2)`)
		return err
	})
	if err := h2(ctx, ev); err != nil || count() != 2 {
		t.Fatalf("second consumer: err=%v count=%d", err, count())
	}
}

func TestConcurrentDuplicatesRunOnce(t *testing.T) {
	pool := eventstest.PGPool(t)
	ctx := context.Background()
	var runs atomic.Int32
	h := inbox.Handle(pool, "svc", func(context.Context, pgx.Tx, events.Event) error { runs.Add(1); return nil })
	ev, _ := events.NewJSON("/t", "a.b", 1)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = h(ctx, ev) }()
	}
	wg.Wait()
	if runs.Load() != 1 {
		t.Errorf("handler ran %d times", runs.Load())
	}
}

// TestOutboxToInboxEndToEnd wires the whole path on the in-memory bus: enqueue in a tx, relay, subscribe with the
// inbox, and a duplicate publish of the same event.
func TestOutboxToInboxEndToEnd(t *testing.T) {
	pool := eventstest.PGPool(t)
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `CREATE TABLE effects (n INT)`)
	bus := memory.New()

	sub, err := bus.Subscribe(ctx, "billing", []string{"hiring.>"},
		inbox.Handle(pool, "billing", func(ctx context.Context, tx pgx.Tx, _ events.Event) error {
			_, err := tx.Exec(ctx, `INSERT INTO effects VALUES (1)`)
			return err
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Stop(ctx) }()

	ev, _ := events.NewJSON("/services/hiring", "hiring.application.submitted.v1", map[string]string{"id": "a1"})
	tx, _ := pool.Begin(ctx)
	if err := outbox.Enqueue(ctx, tx, ev); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit(ctx)
	relay, _ := outbox.NewRelay(outbox.Config{DB: pool, Publisher: bus})
	if _, err := relay.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	_ = bus.Publish(ctx, ev) // duplicate: dropped by the bus; the inbox would catch it too
	for i := 0; i < 200; i++ {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM effects`).Scan(&n)
		if n == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("effect not applied")
}
