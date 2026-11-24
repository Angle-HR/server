# Event layer (`pkg/events`)

Cloud-neutral async messaging for the OpenHR services. Application code imports only `pkg/events` (ports, envelope)
plus `outbox` and `inbox`; the adapters (`memory`, `nats`) are wired in `main`. No AWS/Azure/GCP SDK may appear
under `pkg/events` (enforced by `TestNoCloudSDKs` and the `depguard` rule in `.golangci.yml`). Staging (AWS) and
production (Azure) both run NATS JetStream, so the broker is the same; only how it is hosted differs.

## Envelope

`events.Event` is a CloudEvents 1.0 envelope in JSON structured mode.

| Field | CloudEvents attribute | Notes |
|---|---|---|
| `ID` | `id` | UUIDv7 by default. The bus de-duplicates on it. |
| `Source` | `source` | Producing service, e.g. `/services/hiring`. |
| `Type` | `type` | `<domain>.<entity>.<event>[.vN]`, e.g. `hiring.application.submitted.v1`. Also the broker subject; the first token selects the stream. |
| `Subject` | `subject` | Id of the entity the event is about. |
| `Time`, `DataContentType`, `Data` | `time`, `datacontenttype`, `data` / `data_base64` | JSON payloads are embedded; anything else (protobuf) is base64. |
| `Region`, `CompanyID`, `PartitionKey` | `region`, `companyid`, `partitionkey` | OpenHR extensions. Same partition key means handled in order. |
| `TraceParent`, `TraceState` | `traceparent`, `tracestate` | W3C trace context. |
| `Extensions` | any | Extra lower-case attributes (`[a-z0-9]{1,20}`). |

Builders: `events.NewJSON(source, type, v)`, `events.NewProto(source, type, protoBytes)` (call `proto.Marshal` at
the call site; the package does not import the generated contracts), then `.WithSubject/.WithRegion/.WithCompany/
.WithPartitionKey/.WithExtension`.

## Ports

```go
type Publisher  interface{ Publish(ctx, Event) error }
type Subscriber interface{ Subscribe(ctx, consumer string, subjects []string, h Handler, opts ...SubscribeOption) (Subscription, error) }
type Handler    func(ctx, Event) error        // nil = ack; error = retry; events.Terminal(err) = dead-letter now
```

Semantics, identical in both adapters (they run the same `internal/runner`, and the same `eventstest` suite):

- **At-least-once.** A returned error retries after the backoff (`WithBackoff`, default 1s, 5s, 30s, 2m; or
  `events.Retry(err, d)`). After `WithMaxDeliveries` attempts (default 5) the event is published to
  `dlq.<consumer>.<type>` with the original envelope plus extensions `dlqreason`, `dlqattempts`, `dlqconsumer`, and
  acknowledged. `events.DeliveryFrom(ctx)` gives the attempt number.
- **Consumers are durable and named.** Same name = competing consumers; different names each get every event.
  A new consumer starts at the beginning of the retained stream.
- **Subjects** are NATS filters (`hiring.>`, `hiring.application.*`); the first token must be literal.
- **De-duplication by id** on publish (NATS: `Nats-Msg-Id`, 2 h window). Beyond the window use the inbox.
- **Ordering per `PartitionKey`**: within one consumer process, events with the same key are handled one at a time in
  stream order; a failing event blocks its key and is retried in place until acked or dead-lettered. Events with no
  key run concurrently (`WithConcurrency`, default 8). Several replicas of one consumer do not coordinate: a consumer
  that needs strict per-key order must run one active instance, or check a version in the handler. (A subject-per-
  partition design would give cross-replica order but costs a consumer per partition; revisit if needed.)
- **Graceful shutdown**: `Subscription.Stop(ctx)` stops fetching, waits for running handlers, and hands events
  waiting for a retry back to the broker. Cancelling the `Subscribe` ctx does the same.
- **Trace context**: `Publish` and `outbox.Enqueue` copy the trace context of `ctx` into the envelope; the handler's
  ctx carries it again. The default propagator needs no library. With OpenTelemetry, once at start-up:

```go
events.SetPropagator(otelProp{})

type otelProp struct{}
func (otelProp) Inject(ctx context.Context) (string, string) {
	c := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, c)
	return c["traceparent"], c["tracestate"]
}
func (otelProp) Extract(ctx context.Context, tp, ts string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{"traceparent": tp, "tracestate": ts})
}
```

## NATS layout

One JetStream stream per domain (`HIRING` captures `hiring.>`), created on first use with file storage, limits
retention (7 days), 2 h duplicate window; stream `DLQ` captures `dlq.>` (30 days). A durable pull consumer per
(consumer name, stream) with explicit acks and DeliverAll. `Config.StreamPrefix`, `Replicas`, `MaxAge` adjust it.
Retries run in the process (heartbeating with `InProgress`), so the broker's redelivery covers only crashes: the
consumer's `MaxDeliver` is `MaxDeliveries+1`.

## Transactional outbox and inbox

Tables `events_outbox` and `events_inbox` come from `db/migrations/000018_events_outbox_inbox.sql`. The tables are
unqualified, so every service applies the file to its own database (a regional service applies it to each region
database). Publish in the transaction that changes the data:

```go
tx, _ := pool.Begin(ctx)
defer tx.Rollback(ctx)
// ... business writes through tx ...
ev, _ := events.NewJSON("/services/hiring", "hiring.application.submitted.v1", payload)
ev = ev.WithSubject(appID).WithCompany(companyID).WithRegion("eu").WithPartitionKey(appID)
if err := outbox.Enqueue(ctx, tx, ev); err != nil { return err }
return tx.Commit(ctx)
```

Per database, run one relay next to the service:

```go
relay, _ := outbox.NewRelay(outbox.Config{
	DB: pool, Publisher: bus,
	Hooks: outbox.Hooks{OnAttempt: recordPublishMetric, OnBatch: recordBatchMetric},
})
go relay.Run(ctx)
```

The relay selects due rows with `FOR UPDATE SKIP LOCKED`, publishes, marks `sent_at`, and on failure sets
`next_attempt_at` from the backoff (1s, 5s, 30s, 2m, 10m, 1h) and gives up after 20 attempts (`failed_at`, kept for an
operator). A failed row blocks later rows with the same partition key. `outbox.Purge` deletes old sent rows.

Subscribe with the inbox so redelivery is a no-op; the handler's writes and the "seen" record commit together:

```go
sub, _ := bus.Subscribe(ctx, "applications-on-hiring", []string{"hiring.application.>"},
	inbox.Handle(pool, "applications-on-hiring", func(ctx context.Context, tx pgx.Tx, e events.Event) error {
		var p ApplicationSubmitted
		if err := e.DecodeJSON(&p); err != nil { return events.Terminal(err) }
		return projectApplication(ctx, tx, p)
	}))
defer sub.Stop(shutdownCtx)
```

### Migration note: the existing hiring outbox

`internal/hiring/outbox` is unchanged. It is an email specific, per-tenant leased queue stored in the regional
databases and drained by the mailer; it does not publish events. Leave it until the notifications service exists.
At that point the hiring service should stop writing email rows and instead `Enqueue` events
(`hiring.application.received.v1`, `...decided.v1`, `...retention_notice.v1`); notifications subscribes and sends the
emails through its own inbox. Rows already in the old table drain on their own, then the table can be dropped.

## Running the tests

```sh
go test ./pkg/events/...                       # unit + memory adapter; NATS and Postgres tests skip

docker run --rm -d --name events-nats -p 24222:4222 nats:2 -js
docker run --rm -d --name events-pg -p 25432:5432 -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=ev postgres:16-alpine
EVENTS_NATS_URL=nats://localhost:24222 \
EVENTS_PG_URL=postgres://postgres:pw@localhost:25432/ev \
go test ./pkg/events/... -race -count=1
```

A new adapter passes `eventstest.Run(t, factory)`.

## Hiring job events

Hiring publishes the job lifecycle through the outbox of the job's regional database so the applications service can
keep a local read model (BOUNDARIES 4.3). Contract and payload: `internal/hiring/jobevents`.

| Type | When |
|---|---|
| `hiring.job.published.v1` | publish, or reopen |
| `hiring.job.updated.v1` | details, rules or declarations of a live job change |
| `hiring.job.paused.v1` / `hiring.job.resumed.v1` | pause / resume |
| `hiring.job.closed.v1` | close, expiry (`close_reason`), back to draft |

Every event carries the full job snapshot and `version` (the job revision); partition key is the job id; the event id
is derived from type, job id and version. `HIRING_EVENTS=on` (default off) turns emission on; the monolith then runs
outbox relays (needs `NATS_URL`), `hiring-expire` drains the outbox before exiting, and `hiring-jobs-backfill` emits
the current state of every open or paused job (safe to re-run).

## Hiring members event (consumer: applications)

`hiring.job.members_changed.v1` (payload in hiring.md) is projected by applications into `hiring.app_job_members`
(`jobmodel.Projector`, same inbox consumer `applications-jobs`, replace when `version` is higher). The job snapshot
events now also carry `created_by` and `location_mode`. Together they let `JobAccess` answer without the hiring tables;
`hiring-jobs-backfill` emits a members snapshot next to every live job's event so a consumer that starts later learns
the existing teams. The public id index (`hiring.app_job_index`, global DB) is fed by the same projector.

## org.company.deleted.v1 / org.company.reactivated.v1 consumers

| Consumer (inbox name) | Service | Effect |
|---|---|---|
| `applications-org` | applications (`APPLICATIONS_ORG_EVENTS=on`) | deleted: deletes the company's applications data, answer rows, file rows and blob objects, job read model; records a tombstone. Reactivated: lifts the tombstone and logs it (removed data does not come back) |
| `hiring-org` | hiring (`HIRING_ORG_EVENTS=on`) | deleted: archives the company's published and paused jobs (closed events, registry status `archived`). Reactivated: logs only, jobs stay archived |

## Region move, reactivation and staff deactivation

| Type | Producer and switch | Consumers |
|---|---|---|
| `org.region.moved.v1` | org, `ORG_EVENTS=on`; written when the region-migration saga completes (`REGION_MOVE=saga`), in the target region's outbox, partition key = user id | none in this repository (cache invalidation for services that remember a user's region) |
| `org.company.reactivated.v1` | org, `ORG_EVENTS=on`; `AdminOrgService.ReactivateCompany`, partition key = company id | `applications-org`, `hiring-org` (table above) |
| `identity.staff.deactivated.v1` | identity, `IDENTITY_EVENTS=on`; a platform staff member goes from active to inactive (`PATCH /admin/staff/{id}`), global outbox, in the same transaction, partition key = staff id; repeating the PATCH emits nothing | `identity-staff` (identity itself, `IDENTITY_EVENTS=on`): ends every admin session of the member again, so a Redis failure during the PATCH cannot leave them signed in |

Payloads and the saga: [org.md](org.md), [identity.md](identity.md).

`org.*` payloads also carry `company_kind` (`business` or `individual`, a personal organization); see [individual-accounts.md](individual-accounts.md).
