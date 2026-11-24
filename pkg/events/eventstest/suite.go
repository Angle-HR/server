// Package eventstest is the conformance suite every events.Bus adapter must pass:
//
//	func TestConformance(t *testing.T) {
//		eventstest.Run(t, func(t *testing.T) events.Bus { return memory.New() })
//	}
//
// Each subtest gets a fresh Bus from the factory and uses unique domains and consumer names, so the factory may
// return a Bus connected to a shared broker.
package eventstest

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/pkg/events"
)

// Factory returns a ready Bus; the suite closes it.
type Factory func(t *testing.T) events.Bus

const (
	wait = 15 * time.Second
	fast = 20 * time.Millisecond
)

// Run executes the suite.
func Run(t *testing.T, f Factory) {
	t.Helper()
	cases := map[string]func(*testing.T, events.Bus){
		"PublishSubscribe":        publishSubscribe,
		"WildcardFilters":         wildcardFilters,
		"DurableResume":           durableResume,
		"RedeliveryOnError":       redelivery,
		"DedupeByID":              dedupe,
		"DeadLetterAfterMax":      deadLetter,
		"TerminalErrorGoesToDLQ":  terminal,
		"PerKeyOrdering":          ordering,
		"GracefulShutdownDrains":  shutdownDrains,
		"ShutdownReturnsRetrying": shutdownReturnsRetrying,
		"CompetingConsumers":      competing,
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			bus := f(t)
			t.Cleanup(func() { _ = bus.Close() })
			c(t, bus)
		})
	}
}

type env struct {
	dom      string // unique domain
	consumer string
}

func newEnv() env {
	id := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	return env{dom: "t" + id, consumer: "c" + id}
}

func (e env) typ(rest string) string { return e.dom + "." + rest }

func (e env) event(t *testing.T, rest string, v any) events.Event {
	t.Helper()
	ev, err := events.NewJSON("/test", e.typ(rest), v)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func stop(t *testing.T, s events.Subscription) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Errorf("stop: %v", err)
	}
}

func publish(t *testing.T, b events.Bus, ev events.Event) {
	t.Helper()
	if err := b.Publish(context.Background(), ev); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

type collector struct {
	mu  sync.Mutex
	got []events.Event
}

func (c *collector) add(e events.Event) { c.mu.Lock(); c.got = append(c.got, e); c.mu.Unlock() }
func (c *collector) n() int             { c.mu.Lock(); defer c.mu.Unlock(); return len(c.got) }
func (c *collector) all() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Event(nil), c.got...)
}

func publishSubscribe(t *testing.T, b events.Bus) {
	e := newEnv()
	var c collector
	var attempt atomic.Int32
	sub, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"},
		func(ctx context.Context, ev events.Event) error {
			if d, ok := events.DeliveryFrom(ctx); ok {
				attempt.Store(int32(d.Attempt))
			}
			c.add(ev)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, sub)

	tp := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	in := e.event(t, "thing.created.v1", map[string]any{"n": 1}).
		WithSubject("thing-1").WithRegion("eu").WithCompany("co-1").WithPartitionKey("k").WithExtension("custom", "x")
	in.TraceParent = tp
	publish(t, b, in)

	eventually(t, "delivery", func() bool { return c.n() == 1 })
	out := c.all()[0]
	if out.ID != in.ID || out.Type != in.Type || out.Source != "/test" || out.Subject != "thing-1" ||
		out.Region != "eu" || out.CompanyID != "co-1" || out.PartitionKey != "k" || out.TraceParent != tp ||
		out.Extensions["custom"] != "x" || out.DataContentType != events.ContentTypeJSON {
		t.Errorf("envelope changed in transit:\n in=%+v\nout=%+v", in, out)
	}
	var data struct{ N int }
	if err := out.DecodeJSON(&data); err != nil || data.N != 1 {
		t.Errorf("data = %+v err=%v", data, err)
	}
	if !out.Time.Equal(in.Time) {
		t.Errorf("time %v != %v", out.Time, in.Time)
	}
	if attempt.Load() != 1 {
		t.Errorf("first delivery attempt = %d", attempt.Load())
	}
}

func wildcardFilters(t *testing.T, b events.Bus) {
	e := newEnv()
	var one, all collector
	s1, err := b.Subscribe(context.Background(), e.consumer+"-one", []string{e.typ("a.*")},
		func(_ context.Context, ev events.Event) error { one.add(ev); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, s1)
	s2, err := b.Subscribe(context.Background(), e.consumer+"-all", []string{e.dom + ".>"},
		func(_ context.Context, ev events.Event) error { all.add(ev); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, s2)
	publish(t, b, e.event(t, "a.x", 1))
	publish(t, b, e.event(t, "a.x.y", 1))
	publish(t, b, e.event(t, "b.x", 1))
	eventually(t, "all", func() bool { return all.n() == 3 })
	time.Sleep(200 * time.Millisecond)
	if one.n() != 1 {
		t.Errorf("filter a.* matched %d events, want 1", one.n())
	}
}

func durableResume(t *testing.T, b events.Bus) {
	e := newEnv()
	var c collector
	h := func(_ context.Context, ev events.Event) error { c.add(ev); return nil }
	sub, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"}, h)
	if err != nil {
		t.Fatal(err)
	}
	publish(t, b, e.event(t, "x.one", 1))
	eventually(t, "first", func() bool { return c.n() == 1 })
	stop(t, sub)
	publish(t, b, e.event(t, "x.two", 2))

	sub, err = b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"}, h)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, sub)
	eventually(t, "event published while stopped", func() bool { return c.n() == 2 })
	time.Sleep(200 * time.Millisecond)
	if c.n() != 2 {
		t.Errorf("handled %d events, want 2 (no replay of acked events)", c.n())
	}
}

func redelivery(t *testing.T, b events.Bus) {
	e := newEnv()
	var mu sync.Mutex
	var attempts []int
	sub, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"},
		func(ctx context.Context, _ events.Event) error {
			d, _ := events.DeliveryFrom(ctx)
			mu.Lock()
			attempts = append(attempts, d.Attempt)
			mu.Unlock()
			if d.Attempt < 3 {
				return errors.New("not yet")
			}
			return nil
		}, events.WithBackoff(fast), events.WithMaxDeliveries(5))
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, sub)
	publish(t, b, e.event(t, "r.x", 1))
	eventually(t, "3 attempts", func() bool { mu.Lock(); defer mu.Unlock(); return len(attempts) == 3 })
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(attempts) != "[1 2 3]" {
		t.Errorf("attempts = %v, want [1 2 3]", attempts)
	}
}

func dedupe(t *testing.T, b events.Bus) {
	e := newEnv()
	var c collector
	sub, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"},
		func(_ context.Context, ev events.Event) error { c.add(ev); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, sub)
	ev := e.event(t, "d.x", 1)
	publish(t, b, ev)
	publish(t, b, ev)
	publish(t, b, e.event(t, "d.x", 2)) // a different id is delivered
	eventually(t, "two events", func() bool { return c.n() >= 2 })
	time.Sleep(300 * time.Millisecond)
	if c.n() != 2 {
		t.Errorf("handled %d events, want 2 (same id published twice)", c.n())
	}
}

func deadLetter(t *testing.T, b events.Bus) {
	e := newEnv()
	var calls atomic.Int32
	sub, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"},
		func(context.Context, events.Event) error { calls.Add(1); return errors.New("always broken") },
		events.WithBackoff(fast), events.WithMaxDeliveries(3))
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, sub)
	var dl collector
	dsub, err := b.Subscribe(context.Background(), e.consumer+"-dlqwatch", []string{events.DLQSubject(e.consumer, e.dom+".>")},
		func(_ context.Context, ev events.Event) error { dl.add(ev); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, dsub)

	in := e.event(t, "bad.x", map[string]int{"n": 7})
	publish(t, b, in)
	eventually(t, "dead letter", func() bool { return dl.n() == 1 })
	time.Sleep(300 * time.Millisecond)
	got := dl.all()[0]
	if calls.Load() != 3 {
		t.Errorf("handler ran %d times, want 3", calls.Load())
	}
	if dl.n() != 1 || got.ID != in.ID || got.Type != in.Type || got.Extensions["dlqattempts"] != "3" ||
		!strings.Contains(got.Extensions["dlqreason"], "always broken") ||
		got.Extensions["dlqconsumer"] != events.SanitizeConsumer(e.consumer) {
		t.Errorf("dead letter = %+v (count %d)", got, dl.n())
	}
}

func terminal(t *testing.T, b events.Bus) {
	e := newEnv()
	var calls atomic.Int32
	sub, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"},
		func(context.Context, events.Event) error { calls.Add(1); return events.Terminal(errors.New("poison")) },
		events.WithBackoff(fast))
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, sub)
	var dl collector
	dsub, err := b.Subscribe(context.Background(), e.consumer+"-dlqwatch", []string{events.DLQSubject(e.consumer, e.dom+".>")},
		func(_ context.Context, ev events.Event) error { dl.add(ev); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, dsub)
	publish(t, b, e.event(t, "bad.x", 1))
	eventually(t, "dead letter", func() bool { return dl.n() == 1 })
	if calls.Load() != 1 {
		t.Errorf("terminal error retried: %d calls", calls.Load())
	}
}

func ordering(t *testing.T, b events.Bus) {
	e := newEnv()
	const keys, per = 4, 12
	var mu sync.Mutex
	seen := map[string][]int{}
	failed := map[string]bool{}
	sub, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"},
		func(_ context.Context, ev events.Event) error {
			var d struct{ Seq int }
			if err := ev.DecodeJSON(&d); err != nil {
				return events.Terminal(err)
			}
			time.Sleep(time.Duration(rand.IntN(8)) * time.Millisecond)
			mu.Lock()
			defer mu.Unlock()
			if d.Seq == 3 && !failed[ev.PartitionKey] { // each key's 4th event fails once: later ones must wait
				failed[ev.PartitionKey] = true
				return errors.New("transient")
			}
			seen[ev.PartitionKey] = append(seen[ev.PartitionKey], d.Seq)
			return nil
		}, events.WithBackoff(fast), events.WithConcurrency(8))
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, sub)
	for i := 0; i < per; i++ {
		for k := 0; k < keys; k++ {
			publish(t, b, e.event(t, "o.x", map[string]int{"Seq": i}).WithPartitionKey(fmt.Sprintf("key-%d", k)))
		}
	}
	eventually(t, "all events", func() bool {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, s := range seen {
			n += len(s)
		}
		return n == keys*per
	})
	mu.Lock()
	defer mu.Unlock()
	for k, s := range seen {
		for i, v := range s {
			if v != i {
				t.Errorf("key %s handled out of order: %v", k, s)
				break
			}
		}
	}
}

func shutdownDrains(t *testing.T, b events.Bus) {
	e := newEnv()
	started, release := make(chan struct{}), make(chan struct{})
	var done, calls atomic.Int32
	h := func(_ context.Context, _ events.Event) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		done.Add(1)
		return nil
	}
	sub, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"}, h, events.WithAckWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	publish(t, b, e.event(t, "s.x", 1))
	select {
	case <-started:
	case <-time.After(wait):
		t.Fatal("handler never started")
	}
	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		stopped <- sub.Stop(ctx)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a handler was still running")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatalf("stop: %v", err)
	}
	if done.Load() != 1 {
		t.Fatal("handler did not finish before Stop returned")
	}
	// The event was acked by the drained handler: a new subscription must not see it again.
	sub2, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"}, h, events.WithAckWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, sub2)
	time.Sleep(500 * time.Millisecond)
	if calls.Load() != 1 {
		t.Errorf("event redelivered after graceful shutdown: %d calls", calls.Load())
	}
}

func shutdownReturnsRetrying(t *testing.T, b events.Bus) {
	e := newEnv()
	var calls atomic.Int32
	failing := true
	var mu sync.Mutex
	h := func(_ context.Context, _ events.Event) error {
		calls.Add(1)
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return errors.New("down")
		}
		return nil
	}
	sub, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"}, h,
		events.WithBackoff(time.Hour), events.WithAckWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	publish(t, b, e.event(t, "s.y", 1))
	eventually(t, "first failure", func() bool { return calls.Load() == 1 })
	stop(t, sub) // the event is waiting for an hour-long retry: Stop must hand it back, not block

	mu.Lock()
	failing = false
	mu.Unlock()
	sub2, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"}, h, events.WithAckWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, sub2)
	eventually(t, "redelivery after restart", func() bool { return calls.Load() == 2 })
}

func competing(t *testing.T, b events.Bus) {
	e := newEnv()
	const n = 20
	var a, c collector
	s1, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"},
		func(_ context.Context, ev events.Event) error { a.add(ev); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, s1)
	s2, err := b.Subscribe(context.Background(), e.consumer, []string{e.dom + ".>"},
		func(_ context.Context, ev events.Event) error { c.add(ev); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, s2)
	for i := 0; i < n; i++ {
		publish(t, b, e.event(t, "c.x", i))
	}
	eventually(t, "all handled", func() bool { return a.n()+c.n() >= n })
	time.Sleep(300 * time.Millisecond)
	if a.n()+c.n() != n {
		t.Errorf("same consumer name handled %d events in total, want %d (work is shared, not duplicated)", a.n()+c.n(), n)
	}
}
