// Package nats is the NATS JetStream adapter of the events ports.
//
// Layout
//   - One stream per domain (first token of the event type): "hiring.application.submitted.v1" is stored in the
//     stream HIRING, which captures "hiring.>". Streams are created on first use (Config.Replicas, MaxAge,
//     DuplicateWindow). Retention is limits based, so every consumer sees every event.
//   - One stream DLQ captures "dlq.>". An event that exhausts its deliveries is published to
//     "dlq.<consumer>.<type>" with its envelope unchanged plus the extensions dlqreason, dlqattempts and
//     dlqconsumer; the original is then terminated.
//   - Publishing sets the header Nats-Msg-Id to the event id, so the server drops a duplicate inside the
//     duplicate window (default 2h). The outbox relay and the inbox cover everything beyond that window.
//   - A consumer is a durable pull consumer named after the consumer name (explicit acks, ack wait, DeliverAll),
//     one per stream it filters on. Replicas of a service use the same name and share the work.
//
// # Ordering
//
// JetStream orders per stream but not across redeliveries or across competing consumers, so ordering per
// partition key is enforced in the consumer process: the shared runner handles messages with the same key one at
// a time in stream order and retries a failing one in place (the key is blocked until it succeeds or is
// dead-lettered). This holds inside one process. Several replicas of one durable consumer may handle the same key
// concurrently; a consumer that needs strict order per key must therefore run as a single active instance
// (Concurrency does not matter), or make its handler order-insensitive with a version check.
package nats

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/internal/runner"
)

// Config configures the adapter.
type Config struct {
	URL             string        // e.g. nats://localhost:4222
	Name            string        // connection name shown by the server
	Options         []nats.Option // extra connection options (credentials, TLS)
	Replicas        int           // stream replicas; default 1
	MaxAge          time.Duration // how long events are kept; default 7 days
	DLQMaxAge       time.Duration // how long dead letters are kept; default 30 days
	DuplicateWindow time.Duration // Nats-Msg-Id window; default 2 hours
	StreamPrefix    string        // optional prefix for stream names, e.g. "LOCAL_"
	// DomainMaxAge overrides MaxAge for the stream of a domain (first subject token), e.g. {"notifications": 24h}.
	DomainMaxAge map[string]time.Duration
}

// Bus implements events.Bus on JetStream.
type Bus struct {
	cfg     Config
	nc      *nats.Conn
	js      jetstream.JetStream
	mu      sync.Mutex
	streams map[string]jetstream.Stream
}

var _ events.Bus = (*Bus)(nil)

// Connect opens the connection.
func Connect(cfg Config) (*Bus, error) {
	if cfg.URL == "" {
		cfg.URL = nats.DefaultURL
	}
	if cfg.Replicas <= 0 {
		cfg.Replicas = 1
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 7 * 24 * time.Hour
	}
	if cfg.DLQMaxAge <= 0 {
		cfg.DLQMaxAge = 30 * 24 * time.Hour
	}
	if cfg.DuplicateWindow <= 0 {
		cfg.DuplicateWindow = 2 * time.Hour
	}
	opts := append([]nats.Option{nats.Name(cfg.Name), nats.MaxReconnects(-1)}, cfg.Options...)
	nc, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats: connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats: jetstream: %w", err)
	}
	return &Bus{cfg: cfg, nc: nc, js: js, streams: map[string]jetstream.Stream{}}, nil
}

// Close drains and closes the connection.
func (b *Bus) Close() error { return b.nc.Drain() }

func (b *Bus) streamName(domain string) string {
	return b.cfg.StreamPrefix + strings.ToUpper(strings.NewReplacer("-", "_").Replace(domain))
}

func validDomain(d string) error {
	if d == "" || strings.ContainsAny(d, "*>") {
		return fmt.Errorf("nats: domain %q must be a literal token", d)
	}
	return nil
}

// ensure creates the stream of a domain if needed.
func (b *Bus) ensure(ctx context.Context, domain string) (jetstream.Stream, error) {
	if err := validDomain(domain); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.streams[domain]; ok {
		return s, nil
	}
	sc := jetstream.StreamConfig{
		Name: b.streamName(domain), Subjects: []string{domain + ".>"}, Storage: jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy, MaxAge: b.cfg.MaxAge, Replicas: b.cfg.Replicas,
		Duplicates: b.cfg.DuplicateWindow,
	}
	if age, ok := b.cfg.DomainMaxAge[domain]; ok && age > 0 {
		sc.MaxAge = age
	}
	if domain == "dlq" {
		sc.MaxAge = b.cfg.DLQMaxAge
	}
	s, err := b.js.CreateOrUpdateStream(ctx, sc)
	if err != nil {
		return nil, fmt.Errorf("nats: ensure stream %s: %w", sc.Name, err)
	}
	b.streams[domain] = s
	return s, nil
}

// Publish implements events.Publisher. It returns after the stream has stored the event.
func (b *Bus) Publish(ctx context.Context, e events.Event) error {
	e = events.InjectTrace(ctx, e)
	body, err := events.Encode(e)
	if err != nil {
		return err
	}
	return b.publish(ctx, e.Type, e.ID, e.TraceParent, body)
}

func (b *Bus) publish(ctx context.Context, subject, msgID, traceParent string, body []byte) error {
	if _, err := b.ensure(ctx, events.Domain(subject)); err != nil {
		return err
	}
	msg := nats.NewMsg(subject)
	msg.Data = body
	msg.Header.Set(jetstream.MsgIDHeader, msgID)
	msg.Header.Set("Content-Type", "application/cloudevents+json")
	if traceParent != "" {
		msg.Header.Set("traceparent", traceParent)
	}
	if _, err := b.js.PublishMsg(ctx, msg); err != nil {
		return fmt.Errorf("nats: publish %s: %w", subject, err)
	}
	return nil
}

// Subscribe implements events.Subscriber.
func (b *Bus) Subscribe(ctx context.Context, name string, subjects []string, h events.Handler,
	opts ...events.SubscribeOption) (events.Subscription, error) {
	if name == "" || len(subjects) == 0 || h == nil {
		return nil, errors.New("nats: consumer name, subjects and handler are required")
	}
	name = events.SanitizeConsumer(name)
	o := events.Resolve(opts...)
	byDomain := map[string][]string{}
	for _, s := range subjects {
		if err := events.ValidateSubject(s); err != nil {
			return nil, fmt.Errorf("nats: subject %q: %w", s, err)
		}
		d := events.Domain(s)
		if err := validDomain(d); err != nil {
			return nil, err
		}
		byDomain[d] = append(byDomain[d], s)
	}
	sub := &subscription{done: make(chan struct{})}
	sub.run = runner.New(ctx, runner.Config{
		Consumer: name, Handler: h, Opts: o,
		DeadLetter: func(c context.Context, d runner.Delivery, reason string, attempts int) error {
			return b.deadLetter(c, name, d.Event(), reason, attempts)
		},
	})
	for domain, filters := range byDomain {
		stream, err := b.ensure(ctx, domain)
		if err != nil {
			_ = sub.Stop(ctx)
			return nil, err
		}
		cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
			Durable: name, FilterSubjects: filters, AckPolicy: jetstream.AckExplicitPolicy,
			AckWait: o.AckWait, MaxDeliver: o.MaxDeliveries + 1, DeliverPolicy: jetstream.DeliverAllPolicy,
			MaxAckPending: o.Concurrency * 4,
		})
		if err != nil {
			_ = sub.Stop(ctx)
			return nil, fmt.Errorf("nats: consumer %s on %s: %w", name, domain, err)
		}
		cc, err := cons.Consume(func(m jetstream.Msg) {
			if d := b.wrap(ctx, name, m); d != nil {
				sub.run.Dispatch(d)
			}
		})
		if err != nil {
			_ = sub.Stop(ctx)
			return nil, fmt.Errorf("nats: consume %s: %w", name, err)
		}
		sub.ccs = append(sub.ccs, cc)
	}
	go func() {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), events.DefaultAckWait)
			defer cancel()
			_ = sub.Stop(sctx)
		case <-sub.done:
		}
	}()
	return sub, nil
}

type subscription struct {
	run  *runner.Runner
	ccs  []jetstream.ConsumeContext
	once sync.Once
	done chan struct{}
}

// Stop implements events.Subscription: stop fetching, finish running handlers, Nak the rest.
func (s *subscription) Stop(ctx context.Context) error {
	for _, cc := range s.ccs {
		cc.Stop()
	}
	err := s.run.Shutdown(ctx)
	s.once.Do(func() { close(s.done) })
	return err
}

// wrap turns a JetStream message into a runner delivery. A message that is not a valid envelope is
// dead-lettered raw straight away.
func (b *Bus) wrap(ctx context.Context, consumer string, m jetstream.Msg) *delivery {
	ev, err := events.Decode(m.Data())
	if err != nil {
		_ = b.publishRaw(ctx, consumer, m, err.Error())
		_ = m.Term()
		return nil
	}
	attempt := 1
	if md, merr := m.Metadata(); merr == nil {
		attempt = int(md.NumDelivered)
	}
	return &delivery{m: m, ev: ev, attempt: attempt}
}

func (b *Bus) publishRaw(ctx context.Context, consumer string, m jetstream.Msg, reason string) error {
	msg := nats.NewMsg(events.DLQSubject(consumer, m.Subject()))
	msg.Data = m.Data()
	msg.Header.Set("Dlq-Reason", reason)
	if id := m.Headers().Get(jetstream.MsgIDHeader); id != "" {
		msg.Header.Set(jetstream.MsgIDHeader, id+"/"+consumer)
	}
	if _, err := b.ensure(ctx, "dlq"); err != nil {
		return err
	}
	_, err := b.js.PublishMsg(ctx, msg)
	return err
}

func (b *Bus) deadLetter(ctx context.Context, consumer string, ev events.Event, reason string, attempts int) error {
	ev = ev.WithExtension("dlqreason", reason).
		WithExtension("dlqattempts", fmt.Sprint(attempts)).
		WithExtension("dlqconsumer", consumer)
	body, err := events.Encode(ev)
	if err != nil {
		return err
	}
	return b.publish(ctx, events.DLQSubject(consumer, ev.Type), ev.ID+"/"+consumer, ev.TraceParent, body)
}

type delivery struct {
	m       jetstream.Msg
	ev      events.Event
	attempt int
}

func (d *delivery) Event() events.Event { return d.ev }
func (d *delivery) Attempt() int        { return d.attempt }
func (d *delivery) Ack() error          { return d.m.Ack() }
func (d *delivery) Term() error         { return d.m.Term() }
func (d *delivery) InProgress() error   { return d.m.InProgress() }
func (d *delivery) Nak(delay time.Duration) error {
	if delay <= 0 {
		return d.m.Nak()
	}
	return d.m.NakWithDelay(delay)
}
