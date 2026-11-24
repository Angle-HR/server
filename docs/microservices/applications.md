# Applications service

Second extracted service (after notifications). Owns applying to a job and everything a company does with the
applications: the public apply and upload endpoints, listing and opening applications, status changes, applicant
settings, retention of applications.

```
browser ─> web-bff ──(applications paths)──> cmd/applications ──> regional DBs (applications tables)
              │                                   ▲  hiring.job.*.v1 events (jobmodel projector -> hiring.app_jobs)
              └──(everything else)──> monolith ───┘  applications.application.*.v1 events (outbox)
```

## Switches

| Variable | Where | Meaning |
|---|---|---|
| `UPSTREAM_APPLICATIONS_URL` | web-bff | send the applications paths (`bff.ApplicationsPath`) to the service; empty keeps everything on the monolith |
| `APPLICATIONS_REMOTE=on` | monolith | stop serving those paths (the service serves them) |
| `APPLICATIONS_JOBSOURCE=db\|events` | applications | read jobs from the hiring tables (`db`) or from the local read model fed by hiring events (`events`, with read-through to `db`) |
| `HIRING_EVENTS=on` | monolith | hiring emits `hiring.job.*.v1` (needed for `events`) |
| `APPLICATIONS_EVENTS=on` | applications | publish `applications.application.submitted.v1` / `status_changed.v1` (no applicant data) |

Local: `HIRING_EVENTS=on APPLICATIONS_JOBSOURCE=events APPLICATIONS_REMOTE=on UPSTREAM_APPLICATIONS_URL=http://applications:8080 make local-up PROFILES="applications web-bff"`, then
`BASE=http://localhost:8081/api/v1 ./scripts/e2e-apply.sh` (everything through the BFF). The script includes a check
that pausing a job in hiring makes the service refuse applications within seconds, and resuming reopens it.

## Applicant counts on the job list

Hiring asks for the counters through `draft.ApplicantCounts` (one batched call per list page); the list SQL no
longer reads `job_applicant_counts`. Implementations in `internal/hiring/applicantcounts`:

| `APPLICANT_COUNTS` | |
|---|---|
| `db` (default) | reads `hiring.job_applicant_counts` of the caller's region (`hiringstore.Store.ApplicantCounts`) |
| `grpc` | `ApplicationsService.GetApplicantCounts` at `APPLICATIONS_GRPC_ADDR`, with company, region and user as CallContext metadata |

If the counter source cannot be reached the list still works (warning logged) and each row says so explicitly instead
of showing zero: `applicant_count` and `new_applicant_count` are `null` and `applicant_counts_available` is `false`.
When the source answers, `applicant_counts_available` is `true` and a job without applications is a real `0`. Clients
should show "unavailable" (or hide the badge) for `null` and never treat it as zero. The same applies to
`APPLICANT_COUNTS=db` when its read fails. (`internal/hiring/draft/service.go`, field docs in `hiringtypes.ListItem`.)

## Audit and compliance trail

Applications writes its own rows (migration `000021_applications_audit.sql`, regional databases, no foreign keys, RLS
on `app.tenant_id`): `hiring.app_audit_events` (`application.received|viewed|file_downloaded|status_changed`) and the
append-only `hiring.app_compliance_log` (retention and company deletion records). Hiring keeps `hiring.audit_events`
and `hiring.compliance_log` for the job itself. Rows written before 000021 stay in the hiring tables.

Operator view of a job's combined trail (run as the tenant, or as a superuser; both sides have the same columns):

```sql
SELECT 'hiring' AS source, created_at, actor_id, action, diff FROM hiring.audit_events     WHERE tenant_id = :company AND job_id = :job
UNION ALL
SELECT 'applications', created_at, actor_id, action, diff    FROM hiring.app_audit_events   WHERE tenant_id = :company AND job_id = :job
UNION ALL
SELECT 'hiring_compliance', created_at, actor_id, gate_id || ':' || event, NULL FROM hiring.compliance_log     WHERE tenant_id = :company AND job_id = :job
UNION ALL
SELECT 'applications_compliance', created_at, actor_id, gate_id || ':' || event, NULL FROM hiring.app_compliance_log WHERE tenant_id = :company AND job_id = :job
ORDER BY created_at;
```

Read path: `GET /jobs/audit` on the hiring routes (see [hiring.md](hiring.md#audit-trail-read-path)) returns the first two
sources of the UNION above as one stream; the compliance logs are not part of it.

## gRPC contract

`proto/openhr/applications/v1` now matches the real flow. `SubmitApplication` takes `public_job_id`,
`candidate_country`, `answers` (question id to the JSON of the answer), `privacy_notice_acknowledged`, `consent`,
`source`, `idempotency_key`, `client_ip`, and returns `received` (the same for a new application and a repeat; the old
`duplicate` flag would have leaked that an email applied). `CreateUploadUrl` is per public job id and file question and
returns the one-time token, method, URL, headers, expiry and `max_bytes` (as `POST /public/jobs/{id}/uploads`). The
token is the answer to the question and is scanned and claimed on submit. `ConfirmUpload` is an optional pre-flight that
runs the same check early (`uploads.Service.Verify`), attaches nothing and is repeatable. The three calls are public:
no CallContext user is required. Errors: validation is `INVALID_ARGUMENT` with a `BadRequest` detail per field,
not found / archived `NOT_FOUND`, paused or closed `FAILED_PRECONDITION`, key reuse `ALREADY_EXISTS`, rate limit
`RESOURCE_EXHAUSTED`, no scanner / no sensitive keys `UNAVAILABLE`. `buf breaking` differs from staging on purpose
(removed fields are reserved).

## Uploads and the public blob endpoint

Presigned URLs are signed for the host the client uses. `R2_PUBLIC_ENDPOINT` (or `ANGLEHR_<REGION>_R2_PUBLIC_ENDPOINT`)
names it when it differs from `R2_ENDPOINT` (`pkg/platform/blob/s3compat` keeps a second signing client; every
other call uses the internal endpoint). Local stacks default it to `http://localhost:${MINIO_API_PORT}`. The Azure
adapter is not changed (its URLs are public). `scripts/e2e-apply.sh` with `UPLOADS=on` runs presign, PUT from the host,
scan and claim on submit, the company-side download, an EICAR file refusal, a special category answer, a knockout flag
and a reapply block. The local stack carries insecure dev keys for `ANGLEHR_UK_SENSITIVE_KEYS` and
`ANGLEHR_REAPPLY_HMAC_KEY`; on Apple silicon use `CLAMAV_IMAGE=clamav/clamav-debian:stable`.

## Company deletion (`org.company.deleted.v1`)

Consumer `applications-org` (`internal/applicationsvc/companydeletion`, `APPLICATIONS_ORG_EVENTS=on`, needs `NATS_URL`)
behind the inbox. Nothing emits the event yet: org does not hard-delete companies. When it does it must publish
the event from the transaction that deletes the company, because the foreign keys that cascaded from
`accounts.organizations` were dropped in 000019.

| | |
|---|---|
| type | `org.company.deleted.v1` (subject filter `org.company.deleted.>`) |
| envelope | `CompanyID` and `Region` set; subject and partition key the company id |
| data | `{"company_id":"uuid","region":"uk","deleted_at":"RFC 3339","reason":"optional"}`; missing fields fall back to the envelope |

Effect, in the region named by the event: the company's uploaded objects (final and staging keys) are deleted first,
then in one transaction (with the inbox row) special answers, review tasks, application files, email outbox, reapply
blocks, applicant counts, applications, applicant settings and the job read model (`app_jobs`, `app_job_forms`). The
audit and compliance rows are kept on purpose (ids only, never candidate data; the compliance log is append-only) and a
compliance row records the deletion. A malformed event or unknown region is dead-lettered; a database or storage
failure is retried and repeats safely.

## Still shared or interim

- **Caller resolution** (company, roles) reads `accounts.*` in the shared regional databases
  (`internal/hiring/callerresolver`). Replace with an org gRPC call.
- **Job access** (`JobAccess`), the company list for retention and the public id registry are answered by the DB
  adapter, because no event carries them (see `applications-jobmodel.md`).
- **Region data**: the service and the monolith share the same regional databases locally; `job_applicant_counts`,
  `org_settings` and the audit tables are still in the `hiring` schema.
- The `grpc` applicant counts mode needs applications to be up for counters; when it is down the list reports them as unavailable (`null` plus `applicant_counts_available=false`), there is no cached fallback.
- `org.company.deleted.v1` has no producer yet. The audit trail is read through hiring (`GET /jobs/audit`), which reads `hiring.app_audit_events` of the shared regional database directly until applications serves its own rows.
- Azure presigned URLs have no separate public endpoint setting.


## Anywhere jobs, members and company deletion

- Jobs with `location_mode = anywhere` (no market list) accept a candidate from any valid ISO 3166 alpha-2 code. The
  code itself is still validated (`ZZ`, `GBR`, empty are refused). The application's market is the code's market
  (`UK`, `US`, `KE`, `EU`...) or `OTHER` for countries outside the known markets (migration 000022 widens the
  market check). Specific-area jobs still refuse countries outside their markets. The mode travels in
  `hiring.job.*.v1` (`location_mode`) and is stored in `hiring.app_jobs.location_mode`, so the db job source and the
  events read model agree (`ApplyTarget.Anywhere`).
- `hiring.job.members_changed.v1` fills `hiring.app_job_members`; job access no longer reads hiring tables with
  `APPLICATIONS_JOBSOURCE=events`. `ORG_TENANTS=grpc` and `ORG_NOTICE=grpc` move the retention sweep's company list
  and the notice recipient to org. Details and fallback counters: applications-jobmodel.md.
- `org.company.deleted.v1` (`APPLICATIONS_ORG_EVENTS=on`) deletes the company's applications and blob objects and
  writes a tombstone (`hiring.app_deleted_companies`) so late job events cannot recreate read model rows.
