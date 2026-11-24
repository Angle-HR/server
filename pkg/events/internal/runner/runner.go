// Package runner is the delivery loop shared by the adapters, so the retry, dead-letter, ordering and shutdown
// semantics are identical whatever the broker.
//
// Semantics:
//   - A message without a partition key is handled concurrently with the others (up to Concurrency handlers).
//   - Messages with the same partition key are handled one at a time in the order Dispatch received them.
//     A failing message blocks its key: it is retried in place (with the backoff) until it succeeds or is
//     dead-lettered, and only then does the next message of that key run. That is what keeps the order.
//   - While a message waits for its retry the runner keeps telling the broker it is in progress.
//   - On Shutdown the runner stops starting handlers, waits for the running ones, and gives every message it
//     still holds back to the broker with Nak so it is redelivered.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Angle-HR/server/pkg/events"
)

var errClosing = errors.New("runner closing")

// Delivery is one message handed out by a broker.
type Delivery interface {
	Event() events.Event
	Attempt() int // 1 for the first delivery
	Ack() error
	Nak(delay time.Duration) error
	// Term tells the broker to stop redelivering (used after the message was dead-lettered).
	Term() error
	// InProgress extends the ack deadline.
	InProgress() error
}

// DeadLetterFunc publishes the failed delivery to the dead-letter subject.
type DeadLetterFunc func(ctx context.Context, d Delivery, reason string, attempts int) error

// Config configures a Runner.
type Config struct {
	Consumer   string
	Handler    events.Handler
	Opts       events.SubscribeOptions
	DeadLetter DeadLetterFunc
	Logger     *slog.Logger
}

// Runner dispatches deliveries to a handler.
type Runner struct {
	cfg  Config
	base context.Context
	stop context.CancelFunc
	sem  chan struct{}

	closing chan struct{}
	once    sync.Once
	wg      sync.WaitGroup

	mu   sync.Mutex
	keys map[string]*keyQueue
}

type keyQueue struct{ items []Delivery }

// New returns a Runner. ctx supplies the values (not the cancellation) of the handler contexts.
func New(ctx context.Context, cfg Config) *Runner {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	base, stop := context.WithCancel(context.WithoutCancel(ctx))
	return &Runner{
		cfg: cfg, base: base, stop: stop,
		sem:     make(chan struct{}, cfg.Opts.Concurrency),
		closing: make(chan struct{}),
		keys:    map[string]*keyQueue{},
	}
}

// Dispatch hands a delivery to the runner without blocking. After Shutdown it gives the message back.
func (r *Runner) Dispatch(d Delivery) {
	select {
	case <-r.closing:
		_ = d.Nak(0)
		return
	default:
	}
	key := d.Event().PartitionKey
	if key == "" {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.handle(d) }()
		return
	}
	r.mu.Lock()
	q, running := r.keys[key]
	if !running {
		q = &keyQueue{}
		r.keys[key] = q
	}
	q.items = append(q.items, d)
	r.mu.Unlock()
	if !running {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.drain(key, q) }()
	}
}

func (r *Runner) drain(key string, q *keyQueue) {
	for {
		r.mu.Lock()
		if len(q.items) == 0 {
			delete(r.keys, key)
			r.mu.Unlock()
			return
		}
		d := q.items[0]
		q.items = q.items[1:]
		r.mu.Unlock()
		select {
		case <-r.closing:
			_ = d.Nak(0)
		default:
			r.handle(d)
		}
	}
}

func (r *Runner) handle(d Delivery) {
	opts := r.cfg.Opts
	ev := d.Event()
	attempt := d.Attempt()
	for {
		err := r.call(d, ev, attempt)
		if err == nil {
			if aerr := d.Ack(); aerr != nil {
				r.cfg.Logger.Warn("events: ack failed", "consumer", r.cfg.Consumer, "id", ev.ID, "error", aerr)
			}
			return
		}
		if errors.Is(err, errClosing) || (errors.Is(err, context.Canceled) && r.base.Err() != nil) {
			_ = d.Nak(0)
			return
		}
		if events.IsTerminal(err) || attempt >= opts.MaxDeliveries {
			r.deadLetter(d, attempt, err)
			return
		}
		delay, ok := events.RetryAfter(err)
		if !ok {
			delay = opts.DelayAfter(attempt)
		}
		r.cfg.Logger.Warn("events: handler failed, will retry", "consumer", r.cfg.Consumer, "id", ev.ID,
			"type", ev.Type, "attempt", attempt, "delay", delay, "error", err)
		if !r.wait(d, delay) {
			_ = d.Nak(0) // shutting down: let the broker redeliver it
			return
		}
		attempt++
	}
}

func (r *Runner) call(d Delivery, ev events.Event, attempt int) (err error) {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-r.closing:
		return errClosing
	}
	ctx := events.ContextWithDelivery(r.base, events.Delivery{
		Consumer: r.cfg.Consumer, Attempt: attempt, Max: r.cfg.Opts.MaxDeliveries,
	})
	ctx = events.ExtractTrace(ctx, ev)
	hbDone := make(chan struct{})
	go r.heartbeat(d, hbDone)
	defer close(hbDone)
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("handler panic: %v", p)
		}
	}()
	return r.cfg.Handler(ctx, ev)
}

// heartbeat keeps the broker from redelivering a message a handler is still working on.
func (r *Runner) heartbeat(d Delivery, done <-chan struct{}) {
	t := time.NewTicker(r.cfg.Opts.AckWait / 3)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			_ = d.InProgress()
		}
	}
}

// wait sleeps for delay, heartbeating, and reports false if the runner is shutting down first.
func (r *Runner) wait(d Delivery, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	tick := time.NewTicker(r.cfg.Opts.AckWait / 3)
	defer tick.Stop()
	for {
		select {
		case <-timer.C:
			return true
		case <-tick.C:
			_ = d.InProgress()
		case <-r.closing:
			return false
		}
	}
}

func (r *Runner) deadLetter(d Delivery, attempts int, cause error) {
	ev := d.Event()
	reason := cause.Error()
	if len(reason) > 300 {
		reason = reason[:300]
	}
	if r.cfg.DeadLetter != nil {
		if err := r.cfg.DeadLetter(r.base, d, reason, attempts); err != nil {
			r.cfg.Logger.Error("events: dead-letter publish failed; message will be redelivered",
				"consumer", r.cfg.Consumer, "id", ev.ID, "error", err)
			_ = d.Nak(r.cfg.Opts.DelayAfter(attempts))
			return
		}
	}
	r.cfg.Logger.Error("events: event dead-lettered", "consumer", r.cfg.Consumer, "id", ev.ID, "type", ev.Type,
		"attempts", attempts, "error", cause)
	_ = d.Term()
}

// Shutdown stops starting handlers and waits for the running ones. If ctx ends first the handlers' contexts are
// cancelled and Shutdown still waits for them to return.
func (r *Runner) Shutdown(ctx context.Context) error {
	r.once.Do(func() { close(r.closing) })
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		r.stop()
		return nil
	case <-ctx.Done():
		r.stop()
		<-done
		return ctx.Err()
	}
}
