# Notifications service

First extracted service. It owns sending transactional email.

## Flow

```
handler tx ──EnqueueTx──> events_outbox (same tx) ──relay──> NATS JetStream (NOTIFICATIONS stream)
                                                                   │  notifications.email.requested.v1
                                                         cmd/notifications ──inbox──> mailer ──SMTP
```

- Producers keep calling `EnqueueTx(ctx, tx, mailer.EmailArgs{...})`. `internal/notifications.EventEnqueuer`
  implements that call by writing the event to the outbox in the caller's transaction, so no email is requested for
  a rolled-back change.
- The monolith runs one outbox relay per database (global plus each region) when the events transport is on.
- `cmd/notifications` subscribes as durable consumer `notifications-email`, de-duplicates through `events_inbox`,
  renders and sends. A failed send is retried by the bus (1s, 5s, 30s, 2m), then dead-lettered to `dlq.>`.
  An undecodable payload goes straight to the dead letters.
- Delivery is at-least-once. If the SMTP send succeeds and the inbox commit fails, the email can be sent twice.

## Switching

`NOTIFICATIONS_TRANSPORT=events` (needs `NATS_URL`) uses the path above; `fluvio` is the old path (Fluvio queue drained
by `cmd/email-worker`). The monolith still defaults to `fluvio` so production changes only when you set it; the local
Docker stack defaults to `events`, runs `notifications` always, and keeps `email-worker` behind the `legacy-email`
profile. Jobs already in the Fluvio queue drain through `email-worker`; run it until the queue is empty before
retiring it.

The one-shot jobs follow the same variable:

- `cmd/kyb-sweep`: enqueues through the outbox in its own transactions, then publishes whatever the run left in
  every database's outbox before it exits (it has no long-running relay).
- `cmd/hiring-outbox`: keeps its durable per-tenant queue and replaces only the SMTP send with a publish
  (`notifications.BusSender`). A row is marked sent after the publish succeeds, so a failed publish is retried.

Retention: the `notifications` stream keeps events 24 hours (`notifications.StreamMaxAge`), not the 7 day default,
because the payloads hold recipient addresses and one-time codes. `NOTIFICATIONS_STREAM_MAX_AGE` (a Go duration, 5m to
168h) shortens it, for example `2h`. Every process that touches the bus must build its config with
`notifications.BusConfig` and run with the same value, because each start applies the limit to the stream. See
`hardening.md` for the trade-off (events older than the limit are lost if the service was down that long).

## Database

The service keeps its inbox in its own database when `NOTIFICATIONS_DB_URL` is set (migrations:
`db/migrations/notifications`, table `events_inbox` only; `db/migrations/migrate-all.sh` runs them when the variable is
set). Without it the service falls back to `DB_URL_GLOBAL`, as before. The local stack uses `openhr_notifications` by
default (created by `deploy/local/postgres-init` on a fresh volume, or by the migrate container otherwise).

Switching a running environment: create the database, run the notifications migration, copy the processed ids so a
redelivered event is not sent twice, then point the service at it:

```
psql "$DB_URL_GLOBAL" -c "\copy (SELECT consumer, event_id, processed_at FROM events_inbox WHERE consumer = 'notifications-email') TO 'inbox.csv' CSV"
psql "$NOTIFICATIONS_DB_URL" -c "\copy events_inbox (consumer, event_id, processed_at) FROM 'inbox.csv' CSV"
```

The old rows in the global database can stay; nothing reads them afterwards.

## Retiring email-worker

`cmd/email-worker` drains the Fluvio `email` queue (the old path). Do not delete it until the queue is provably empty
and nothing enqueues to it any more.

1. Production runs `NOTIFICATIONS_TRANSPORT=events` for the monolith, `kyb-sweep` and every other producer.
   Check with the producers' environment, not only the monolith's.
2. `make email-queue-depth` (`scripts/email-queue-depth.sh`, read-only, `DB_URL_GLOBAL` or `LOCAL=1` for the docker
   stack) prints the outstanding jobs by state. Exit code 0 means pending, scheduled, running and failed (retrying) are
   all zero. It also prints unreplayed dead jobs; those do not block retirement but need a decision (replay from the
   Fluvio UI or discard).
3. Wait for exit 0 on several consecutive checks spanning at least the longest scheduled retry (and a weekend if
   emails are scheduled by day).
4. Scale email-worker to zero but keep its task definition and image for a week. Run the depth check daily; if it ever
   shows jobs, scale it back up.
5. Remove: the `email-worker` service and image from the deploy config (infra repo and `container-images.yml`),
   `cmd/email-worker`, the `legacy-email` compose profile, the Fluvio enqueue path in `internal/app` and the email
   queue config in `internal/queue`. Keep Fluvio itself while any other queue (admin jobs) uses it.

## Not done yet

- Payloads still contain one-time codes while they sit on the stream. A reference-plus-lookup design (the event
  carries an id, the service reads the code from the producer) would need a synchronous dependency in the delivery path
  and a store with its own expiry; it is not small or safe enough to do here. The mitigation is the short retention
  above, an internal-only broker, and never logging payloads.
- Delivery is at-least-once; a send that succeeds but fails to record in the inbox can email twice.
- No gRPC surface yet (`NOTIFICATIONS_GRPC_ADDR` is reserved in the compose file).
- Remove `email-worker` and Fluvio's email queue after production runs on `events` and the queue is empty (see above).
