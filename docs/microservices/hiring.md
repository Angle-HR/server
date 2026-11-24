# Hiring service

Third extracted service (after notifications and applications). Owns jobs and drafts, application forms, templates,
departments, the catalog and hiring settings, the publish lifecycle (publish, pause, resume, close, reopen, archive,
expiry), compliance gates, DPIA records, job members, the share kit, the public careers page, the public job JSON
and the sitemap.

```
browser ─> web-bff ──(hiring paths)────────> cmd/hiring ──> regional + global DBs (hiring tables)
              │                                  │  gRPC OrgService (optional, ORG_*=grpc)
              ├──(applications paths)──> cmd/applications
              └──(everything else)────> monolith
```

## Switches

| Variable | Where | Meaning |
|---|---|---|
| `UPSTREAM_HIRING_URL` | web-bff | send the hiring paths (`bff.HiringPath`) to the service; empty keeps them on the monolith |
| `HIRING_REMOTE=on` | monolith | stop serving the hiring routes, the careers pages and the public job JSON/sitemap. The applications routes follow `APPLICATIONS_REMOTE` independently. `/auth/me` still resolves company roles in the monolith |
| `HIRING_EVENTS=on` | hiring (and monolith when it still serves hiring) | emit `hiring.job.*.v1` and `hiring.job.members_changed.v1` through the outbox; the process relays them to `NATS_URL`. With `HIRING_REMOTE=on` set it off on the monolith (`SERVER_HIRING_EVENTS=off` in compose) so one relay per database runs |
| `ORG_READINESS=db\|grpc` | hiring | publish readiness (KYB verified, DPA accepted, privacy contact) from the accounts tables (default) or `OrgService.GetPublishReadiness` |
| `ORG_MEMBERSHIP=db\|grpc` | hiring | caller's company, roles and permissions from the accounts tables (default) or `OrgService.ResolveMembership` |
| `ORG_INDUSTRIES=db\|grpc` | hiring | industry catalog and validation from the global DB (default) or `OrgService.ListIndustries` (10 min cache, stale on error, then db) |
| `ORG_TENANTS=db\|grpc` | `cmd/hiring-expire` | companies to sweep from `accounts.organizations` (default) or `OrgService.ListCompanies` |
| `ORG_COMPANY=db\|grpc` | hiring | company row for the public page and job events (`GetCompany`, 60 s cache, stale on org errors; cold cache and org down fails the write or the page with 503), settings page (`GetPublishReadiness`), `AcceptDPA` (`AcceptAgreement`), `SetPrivacyContact` (`SetPrivacyContact`, drops the cache entry) |
| `IDENTITY_USERS=db\|grpc` | hiring | managers' names on the job list from `IdentityService.BatchGetUsers` (one batch per page, `IDENTITY_GRPC_ADDR`); the SQL then returns ids only |
| `HIRING_ORG_EVENTS=on` | hiring | consume `org.company.deleted.v1` (inbox consumer `hiring-org`) and archive the company's published/paused jobs |
| `ORG_GRPC_ADDR`, `ORG_GRPC_TIMEOUT` | hiring | org address (required when any switch is grpc), per call bound (default 3s) |

Routes the BFF sends to hiring (`internal/bff/routes.go`, `HiringPath`, disjoint from `ApplicationsPath`):
`/jobs`, `/jobs/**` except `/jobs/{id}/applications**`; `/hiring/**` except `/hiring/applicant-settings`;
`/organization/{agreements,privacy-contact,dpia}`; `/public/jobs/sitemap.xml`, `/public/jobs/{public_id}`;
`/j/{public_id}`, `/j/{public_id}/privacy` (pages, no API prefix). Everything else stays on the monolith.

Local: `HIRING_REMOTE=on APPLICATIONS_REMOTE=on HIRING_EVENTS=on SERVER_HIRING_EVENTS=off APPLICATIONS_JOBSOURCE=events UPSTREAM_HIRING_URL=http://hiring:8080 UPSTREAM_APPLICATIONS_URL=http://applications:8080 make local-up PROFILES="hiring applications web-bff"`,
then `BASE=http://localhost:8081/api/v1 ./scripts/e2e-apply.sh`.

## Publish fails closed

`orgclient.ReadinessSource` is the seam (`internal/hiring/orgclient`): the db adapter is today's behaviour, the gRPC
client is `GRPCReadiness`. With `ORG_READINESS=grpc`, if org is unreachable, slow (past `ORG_GRPC_TIMEOUT`) or answers
with any error, `draft.ErrCompanyUnavailable` is returned and the API answers `503 SERVICE_UNAVAILABLE`
(`details.retryable=true`, `Retry-After: 5`); nothing is published or registered. A company org does not know is 404.
Affected: publish, reopen (anything that runs the checks) and `GET /jobs/{id}/publish-check`. Not affected: creating
and editing drafts, forms, templates, confirming a gate and listing gates (the gate list does not depend on the
company). The DPIA stays hiring's own data. The service starts and serves drafting with org down (the connection is lazy).

## Job members event

`hiring.job.members_changed.v1`, emitted in the transaction that saves a job's team (`PUT /jobs/{id}/members`), any job
status, behind `HIRING_EVENTS=on`. Partition key and subject: job id. Event id is deterministic from (type, job, version).

```json
{"version": 7, "job_id": "...", "company_id": "...", "region": "uk",
 "members": [{"user_id": "...", "role": "hiring_manager"}, {"user_id": "...", "role": "collaborator"}]}
```

Ids and roles only. `members` is the complete team after the change (empty array, never null, when emptied).
`version` is `job_postings.revision` after the write. Consumer rule for applications: keep `(members, version)` per
job; replace the team when the event version is higher than the stored one, ignore otherwise. The job's creator has
access without being listed, so access = `created_by` (from the `hiring.job.*` snapshot) or a listed member. Roles used
by applications today: `hiring_manager` and `recruiter` may change statuses; other members may only view. Applications
consumes it (`jobmodel.Projector`, `hiring.app_job_members`), so with `APPLICATIONS_JOBSOURCE=events` `JobAccess` no
longer reads hiring tables; the creator comes from the new `created_by` field of the job snapshot events (the same
events carry `location_mode`). `hiring-jobs-backfill` / `EnqueueLiveJobEvents` also emits a members snapshot per live
job. Defined in `internal/hiring/jobevents` (`MembersPayload`, `BuildMembers`).

## gRPC (`HiringService`, port `GRPC_ADDR`)

Implemented: `CreateDraft`, `GetJob`, `ListJobs`, `PublishJob` (blocked checks come back as `published=false` with
`blocks`; org down is `Unavailable`), `PauseJob`, `ResumeJob`, `CloseJob` (reason accepted, not stored),
`GetPublicJob` (JSON strings; a paused job reports `CLOSED`), `ListOpenJobs`, `CheckJobAccess`, `GetJobsSnapshot`
(max 100 ids). Unimplemented: none. Company, user, region and permissions come from the `pkg/rpc` CallContext.
`GetPublicJob`'s `Job` carries only the public fields (no id, company id or status); `job_number` is 0.

## Still interim (stays on the shared databases)

- With defaults (`ORG_*=db`) everything reads `accounts.*` directly, as the monolith does.
- BOUNDARIES items 5 and 6 (assignable team `GET /hiring/people`, names on job members and the job list) stay SQL on
  `accounts.users`: the decision is that the BFF composes team lists, so hiring gets no identity client.
- Items 3, 4 and `CompanySetup` (`AcceptDPA`, `SetPrivacyContact`, settings page) and item 9 (public page and events
  company row) follow `ORG_COMPANY` (default `db`: `accounts.*`; `grpc`: org). The public page and events no longer need
  org up once a company's row is cached (60 s, stale served on error); they do need it on a cold cache.
- Manager names on the job list follow `IDENTITY_USERS` (default `db`).
- Item 11 (retention notice recipient) is done on the applications side (`ORG_NOTICE=grpc`, applications-jobmodel.md).
- Industries on the public page (`public.Catalog`) read the global table.
- Applicant counts on the job list are still read from `job_applicant_counts`.
- `hiring.job_registry` writes, audit and compliance rows are hiring's own tables.

## Audit trail read path

`GET /jobs/audit` (hiring routes, so `bff.HiringPath` already sends it to this service; the BFF tests cover it) pages the
company's audit events, newest first. It merges `hiring.audit_events` (`source: "hiring"`) and `hiring.app_audit_events`
(`source: "applications"`: received, viewed, file_downloaded, status_changed) in one tenant transaction, so row level
security applies on top of the `tenant_id` predicate. Rows carry ids, the action and the stored `diff` as written; they
hold no candidate data and no IP address (only `admin.audit_logs` stores IPs, see identity.md).

| Item | |
|---|---|
| Permission | `job.audit.view` (`rbac.JobAudit`; founder and legal by the default matrix), checked in `draft.Service.AuditTrail` before any read: 403 otherwise |
| Filters | `job_id` (the entity), `actor_id`, `action` (exact), `from` (inclusive) and `to` (exclusive), RFC 3339 or `YYYY-MM-DD`; a date `to` covers that whole day |
| Paging | `limit` (default 50, max 100) and `cursor` = `meta.next_cursor` of the previous page (opaque; position is `(created_at, source, id)`, so pages stay stable while rows are added). `meta.has_more` says whether another page follows |
| Errors | 400 with `details.fields` for a bad id, time, cursor or limit; 503 when the process has no audit reader |

Row shape: `{source, id, job_id, actor_id, action, diff, created_at}`. Compliance logs (`hiring.compliance_log`,
`hiring.app_compliance_log`) are not included. Hiring reads the applications-owned table directly, like
`job_applicant_counts`; when applications gets its own database, replace `hiringstore.Store.AuditEvents` with a call.
- `cmd/hiring-expire` stays a separate one-shot job (run it on a schedule); `ORG_TENANTS=grpc` makes it use org.
- `tools/orgfake` (`go run ./cmd/tools/orgfake`) is a stand-in OrgService over the shared DBs (readiness, membership,
  industries, companies) until org-svc lands; `tools/hiringprobe` calls the hiring gRPC API.

## Verified (hiring-svc branch, own compose project)

`scripts/e2e-apply.sh` through web-bff with `HIRING_REMOTE=on`, `APPLICATIONS_REMOTE=on`, hiring and applications
services behind `UPSTREAM_HIRING_URL`/`UPSTREAM_APPLICATIONS_URL`: passes with `ORG_*=db`, and with
`ORG_READINESS=ORG_MEMBERSHIP=ORG_INDUSTRIES=grpc` against `orgfake`. Pause/resume reach the applications service.
`/j/{id}`, `/j/{id}/privacy`, the sitemap and `/api/v1/public/jobs/{id}` are served by hiring through the BFF.
With `ORG_READINESS=grpc` and orgfake stopped: publish and publish-check answer 503 `Retry-After: 5`, editing a draft
and reading jobs keep working (200), the job stays a draft, and the same job publishes once orgfake is back.
With `ORG_MEMBERSHIP=grpc` and org down every authenticated hiring request is 503 (caller cannot be resolved), so
keep membership on `db` until org-svc is proven if drafting must survive an org outage.
The gRPC reconnect backoff is capped at 3s. Not run: uploads (no scanner), a real org-svc, the hiring-expire
`ORG_TENANTS=grpc` path against a live stack (unit-tested only), consuming `members_changed` on the applications side.
