# Applications job read model

The applications service answers public apply from a local, event-fed copy of the jobs it needs instead of reading
hiring tables (BOUNDARIES.md 4.3). Code: `internal/applicationsvc/jobmodel`. Table: `hiring.app_jobs` and
`hiring.app_job_forms` (migration `db/migrations/000020_applications_job_read_model.sql`), one set per regional
database.

## Pieces

| | |
|---|---|
| `jobmodel.Projector` | `events.Handler` for `hiring.job.{published,updated,paused,resumed,closed}.v1`. Wrapped in `inbox.Handle` with consumer `applications-jobs`, in the regional database named by the event's `region`. |
| `jobmodel.Subscribe(ctx, bus, router)` | starts the projector on subject `hiring.job.>` (`SubscribePools` takes any `Pools`). |
| `jobmodel.Model` | implements `appjobs.JobSource` (and the `RegionalJobs` it returns) from the tables, with `Fallback appjobs.JobSource` and `ReadThrough bool`. |
| `Model.Rebuild`, `cmd/applications-jobmodel-rebuild` | one-shot backfill, see below. |

## Where the table lives

`hiring.app_jobs`, in the `hiring` schema next to the other applications-owned tables. `internal/dbrouter` sets the
regional `search_path` to `accounts,waitlist,hiring,public`; a new schema would be invisible to every pool and DSN
until the router and each service learn about it, so the `app_` prefix marks ownership and all SQL qualifies the
name. When applications gets its own database the tables move with the others. No foreign keys, no row level
security (the projector writes across companies; every read filters on `company_id`).

`app_jobs` has one row per job: `job_id` (PK), `public_id`, `company_id`, `region`, `status` (stored status),
`public_state`, `closing_date`, `closed_at`, `retention_months`, `job_number`, `job_code`, `title`, `lawful_basis`,
`markets`, `form_id`, `form_version`, `form_schema` (the frozen `{"form_revision","questions"}` document), `rules`,
`declarations`, `company_name`, `privacy_email`, `dpo_contact`, `notice` (notice inputs), `close_reason`, `version`,
`source` (`event` or `read_through`), `published_at`, `updated_at`, `applied_at`. Indexes: `public_id`,
`(company_id, status)`. `app_job_forms` keeps every frozen form version seen (PK `form_id, version`), because an
application made against an older version must stay reviewable after the job was published again.

## Projection rules

- An event is applied only when `event.version > stored version` (`INSERT .. ON CONFLICT DO UPDATE .. WHERE
  app_jobs.version < EXCLUDED.version`). Duplicates (same event id, caught by the inbox, or same version),
  stale events and out-of-order arrival leave the row alone. Order does not matter, only the highest version wins.
- The frozen form version is inserted into `app_job_forms` even for a stale event (a frozen version never changes).
- `closed_at` is set from the event's `updated_at` for `closed` and `expired` jobs (hiring writes `closed_at = now()`
  in the same write that bumps `updated_at`); it is NULL for any other status, so a reopen clears it.
- Undecodable payloads, invalid snapshots (no ids, version < 1), unknown event types and unknown regions return
  `events.Terminal`, so the bus dead-letters them (`dlq.applications-jobs.<type>`). A database error is retried.
- The consumer-side payload is a copy in `payload.go`; `payload_contract_test.go` checks it reads what
  `internal/hiring/jobevents` writes. The package does not import hiring outside that test.

## Which methods are local and which are delegated

| Method | Answer | Why / long-term |
|---|---|---|
| `LoadForApply` | local | The snapshot carries everything: job, stored status and closing date (so the caller's `formschema.StateOf` decides open/paused/closed exactly as with jobsource), markets, frozen form, rules, declarations, company name. A job that never published has no form and is not found, as in jobsource. `Region` and `TenantID` stay empty (the caller sets them). |
| `JobSummary` | local | code, title, company name, public id, retention months are all in the snapshot. |
| `JobLabels` | local | code and title. |
| `FrozenQuestions` | local | from `app_job_forms`. A version older than the model's first event for that job is not local. |
| `ClosedJobs` | local | same clock as jobsource: `COALESCE(closed_at, closing_date + 1, now) + retention months` for `closed`/`expired`. |
| `NoticeRecipient` | local (privacy contact from the newest event row), else org `GetCompanyContact` when `ORG_NOTICE=grpc`, else delegated | Counted fallback `notice_recipient:no_contact`. |
| `JobAccess` | local | Reads `created_by` from the job row and the caller's role from `hiring.app_job_members`, fed by `hiring.job.members_changed.v1` (version wins). Rows written before events carried `created_by`, and jobs with no row, delegate to the fallback (counted `job_access:no_creator`/`no_job`) until the next job event. |
| `TenantIDs` | org `ListCompanies` when `ORG_TENANTS=grpc`, else delegated | Counted fallback `tenant_ids:no_org`. |
| `LookupJob` (Registry) | local through `hiring.app_job_index` in the global database (public id to region, company, job), written by the projector | Unindexed ids fall back (counted `lookup_job:unindexed`) and are written back. Ids of deleted companies stay in the index; the lookup then 404s at load. |

The Fallback is today the DB adapter (`jobsource.Source`); long term a hiring gRPC client. With no Fallback a
delegated method returns `jobmodel.ErrNoFallback`.

### Read-through

With `ReadThrough: true` and a Fallback, a job the model has no row for (published but not yet projected) is asked of
the Fallback once. `LoadForApply` then writes the answer into the model (`source = 'read_through'`, `version = 0`)
so the next call is local. Such a row never overwrites an event row and is replaced by the first event (any real
version is >= 1). It lacks the company's privacy contact (an `ApplyTarget` does not carry it), so `NoticeRecipient`
keeps delegating until an event arrives. `JobSummary`, `FrozenQuestions`, `JobLabels` and `ClosedJobs` also consult
the Fallback on a miss when the switch is on, but cannot build a complete row and write nothing; `ClosedJobs` merges
the Fallback's jobs the model lacks (the model's own clock wins), because a missed retention clock is a compliance
problem and the answer must err towards complete while the model is being filled. A job the Fallback does not know
stays not found and nothing is written. Each miss costs one Fallback call, so a flood of unknown public ids would be
forwarded; the registry lookup in front (which rejects unknown ids) is what bounds it. Turn the switch off once the
rebuild is verified and the projector is caught up.

## Backfill

`cmd/applications-jobmodel-rebuild` (built into the `jobs` image, one-shot, idempotent) lists the published, paused,
closed and expired jobs of every region from the hiring tables (`-closed=false` for live only), reads each through
the DB adapter and writes it with `Remember`. It needs the regional DSNs and `DB_URL_GLOBAL`, no NATS.

The alternative is `cmd/hiring-jobs-backfill`, which re-emits `published`/`paused` snapshots of every live job for
the projector to apply. Prefer it when events are available: its rows carry everything (privacy contact, notice
inputs, `source = 'event'`, the real version) and the projector path is the one production uses. Rebuild works
without the bus and also covers closed and expired jobs (the backfill does not emit closed events), which the
retention sweep needs; its rows are `read_through` with version 0, so later events replace them. Running both is
safe.

## Limitations

- Frozen form versions older than the first event the model saw for a job are only available through the Fallback
  (or `Rebuild` of the job once, which stores the current version only).
- Closed jobs from before events were switched on are only in the model after `Rebuild` (or read-through listing).
- `public_state` is stored for inspection; the apply path derives the state from `status` and `closing_date` the way
  jobsource callers do.
- No DLQ consumer or alerting exists yet for `dlq.applications-jobs.*`.

## Tests

`go test ./internal/applicationsvc/jobmodel` runs the unit tests always. The database tests (projector ordering,
duplicates, stale, per-method Model tests, read-through, rebuild, and the equivalence test) need a Postgres whose
user can `CREATE DATABASE`:

```sh
JOBMODEL_PG_URL=postgres://openhr:change-me@localhost:5432/postgres?sslmode=disable go test ./internal/applicationsvc/jobmodel
```

(`EVENTS_PG_URL` also works.) A template database with every migration applied is built once per run and dropped
after. The equivalence test seeds hiring tables with SQL, produces the events with hiring's own
`EnqueueLiveJobEvents`, projects them, and compares Model and jobsource on the same database for every method that
is local.



## Fallback counters

`GET /internal/jobmodel-stats` on the applications HTTP port (not routed by the BFFs) returns the number of times the
read model had to use the hiring tables, by `method:reason`, plus `total`. With the all-remote switches on it stays at
`{"total":0}`; `scripts/e2e-couplings.sh` asserts it. `APPLICATIONS_JOBFALLBACK` selects whether the fallback exists
at all (see switches.md).
