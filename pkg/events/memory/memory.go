// Package memory is an in-process events.Bus for unit tests and single-process local development. It has the
// same observable semantics as the NATS adapter: durable named consumers that read the whole log, wildcard
// subjects, de-duplication by event id, redelivery with backoff, dead-lettering to "dlq.<consumer>.<type>",
// per-partition-key ordering and graceful Stop. Nothing is persisted.
package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/internal/runner"
)

type entry struct {
	subject string
	ev      events.Event
}

// Bus is the in-memory bus. The zero value is not usable; call New.
type Bus struct {
	mu        sync.Mutex
	log       []entry
	seen      map[string]struct{} // domain/msgID
	consumers map[string]*consumer
	closed    bool
}

var _ events.Bus = (*Bus)(nil)

// New returns an empty bus.
func New() *Bus {
	return &Bus{seen: map[string]struct{}{}, consumers: map[string]*consumer{}}
}

type consumer struct {
	name     string
	subjects []string
	cursor   int         // next log index to deliver
	redeliv  []*delivery // handed back by Nak
	subs     []*subscription
}

type subscription struct {
	bus  *Bus
	c    *consumer
	run  *runner.Runner
	wake chan struct{}
	stop chan struct{}
	done chan struct{}
	opts events.SubscribeOptions
	once sync.Once
}

// Publish appends the event to the log; an event whose id was already published to the same domain is dropped.
func (b *Bus) Publish(ctx context.Context, e events.Event) error {
	e = events.InjectTrace(ctx, e)
	if err := e.Validate(); err != nil {
		return err
	}
	return b.publish(e, e.Type, e.ID)
}

func (b *Bus) publish(e events.Event, subject, msgID string) error {
	// Round-trip through the wire form so adapters cannot be relied on to share memory with the caller.
	raw, err := events.Encode(e)
	if err != nil {
		return err
	}
	cp, err := events.Decode(raw)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("memory: bus closed")
	}
	k := events.Domain(subject) + "/" + msgID
	if _, dup := b.seen[k]; dup {
		return nil
	}
	b.seen[k] = struct{}{}
	b.log = append(b.log, entry{subject, cp})
	for _, c := range b.consumers {
		for _, s := range c.subs {
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}
	}
	return nil
}

// Published returns a copy of everything published so far (including dead letters).
func (b *Bus) Published() []events.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]events.Event, 0, len(b.log))
	for _, e := range b.log {
		out = append(out, e.ev)
	}
	return out
}

// Close stops accepting events.
func (b *Bus) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

// Subscribe implements events.Subscriber.
func (b *Bus) Subscribe(ctx context.Context, name string, subjects []string, h events.Handler,
	opts ...events.SubscribeOption) (events.Subscription, error) {
	if name == "" || len(subjects) == 0 || h == nil {
		return nil, errors.New("memory: consumer name, subjects and handler are required")
	}
	for _, s := range subjects {
		if err := events.ValidateSubject(s); err != nil {
			return nil, fmt.Errorf("memory: subject %q: %w", s, err)
		}
	}
	name = events.SanitizeConsumer(name)
	o := events.Resolve(opts...)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, errors.New("memory: bus closed")
	}
	c := b.consumers[name]
	if c == nil {
		c = &consumer{name: name, subjects: append([]string(nil), subjects...)}
		b.consumers[name] = c
	}
	s := &subscription{bus: b, c: c, wake: make(chan struct{}, 1), stop: make(chan struct{}),
		done: make(chan struct{}), opts: o}
	s.run = runner.New(ctx, runner.Config{
		Consumer: name, Handler: h, Opts: o,
		DeadLetter: func(_ context.Context, d runner.Delivery, reason string, attempts int) error {
			ev := d.Event().
				WithExtension("dlqreason", reason).
				WithExtension("dlqattempts", fmt.Sprint(attempts)).
				WithExtension("dlqconsumer", name)
			return b.publish(ev, events.DLQSubject(name, ev.Type), d.Event().ID+"/"+name)
		},
	})
	c.subs = append(c.subs, s)
	b.mu.Unlock()
	go s.loop()
	s.wake <- struct{}{}
	go func() { // ctx cancellation stops the subscription
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), events.DefaultAckWait)
			defer cancel()
			_ = s.Stop(sctx)
		case <-s.done:
		}
	}()
	return s, nil
}

func (s *subscription) loop() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
		}
		for _, d := range s.next() {
			s.run.Dispatch(d)
		}
	}
}

// next claims the deliveries that are ready.
func (s *subscription) next() []runner.Delivery {
	b, c := s.bus, s.c
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []runner.Delivery
	for _, d := range c.redeliv {
		out = append(out, d)
	}
	c.redeliv = nil
	for c.cursor < len(b.log) {
		e := b.log[c.cursor]
		c.cursor++
		for _, f := range c.subjects {
			if events.Match(f, e.subject) {
				out = append(out, &delivery{bus: b, c: c, ev: e.ev, attempt: 1})
				break
			}
		}
	}
	return out
}

// Stop implements events.Subscription.
func (s *subscription) Stop(ctx context.Context) error {
	s.once.Do(func() { close(s.stop) })
	err := s.run.Shutdown(ctx)
	<-s.done
	s.bus.mu.Lock()
	for i, x := range s.c.subs {
		if x == s {
			s.c.subs = append(s.c.subs[:i], s.c.subs[i+1:]...)
			break
		}
	}
	s.bus.mu.Unlock()
	return err
}

type delivery struct {
	bus     *Bus
	c       *consumer
	ev      events.Event
	attempt int
}

func (d *delivery) Event() events.Event { return d.ev }
func (d *delivery) Attempt() int        { return d.attempt }
func (d *delivery) Ack() error          { return nil }
func (d *delivery) Term() error         { return nil }
func (d *delivery) InProgress() error   { return nil }

// Nak puts the delivery back for another attempt after delay.
func (d *delivery) Nak(delay time.Duration) error {
	again := &delivery{bus: d.bus, c: d.c, ev: d.ev, attempt: d.attempt + 1}
	put := func() {
		d.bus.mu.Lock()
		d.c.redeliv = append(d.c.redeliv, again)
		subs := append([]*subscription(nil), d.c.subs...)
		d.bus.mu.Unlock()
		for _, s := range subs {
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}
	}
	if delay <= 0 {
		put()
	} else {
		time.AfterFunc(delay, put)
	}
	return nil
}
