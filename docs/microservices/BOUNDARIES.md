# Service boundaries and coupling analysis

Scope: analysis of the monolith at `staging` (module `github.com/Angle-HR/server`) against the target of two BFFs plus five services. Nothing here changes code; it defines what has to be cut and how each cut is replaced. Contracts live in `proto/openhr/**` (generated into `gen/`), caller context in `pkg/rpc`.

Method: Go imports from `go list -f '{{.Imports}}' ./...`, mapped package to service and diffed (script kept out of tree); SQL from `grep` over every `FROM|JOIN|INTO|UPDATE` in `internal/` plus the FK clauses in `db/migrations/*.sql`. Line numbers are at the time of writing.

## 1. Package to service map

| Service | Owns (packages / files) | Owns (tables) |
|---|---|---|
| **identity** | `internal/auth` (jwt, totp, lockout, reset, revoke, verify, admin_jwt, admin_context, middleware); `internal/admin` (staff users, roles, invites, bootstrap); `internal/handler/{auth*, admin_auth, admin_staff, admin_roles, admin_users, admin_waitlist, waitlist}`; user and registry parts of `internal/query/accounts.go`; waitlist part of `query/queries.go` | global: `auth.users_registry`, `admin.users/roles/permissions/role_permissions/user_roles/invites/audit_logs`, `accounts.pending_users`, `waitlist.*`. Regional: `accounts.users`, `accounts.organization_invites` |
| **org** | `internal/kyb/**`, `internal/onboarding`, `internal/rbac`, `internal/org` (invite token helpers), `internal/countrycode`; `internal/handler/{business_onboarding, individual_onboarding, product_onboarding*, kyb, admin_verification, catalog, countries, doc_types, product_doc_types, admin_catalogs, admin_doc_types, region_pending}`; organization parts of `query/accounts.go`; `cmd/kyb-sweep` | global: `accounts.pending_organizations`, `accounts.pending_onboarding_progress`, `accounts.onboarding_industries` and other onboarding catalogs, `admin.verification_queue`. Regional: `accounts.organizations`, `organization_members`, `organization_member_roles`, `organization_agreements`, `organization_verifications`, `organization_verification_events`, `accounts.onboarding_progress` |
| **hiring** | `internal/hiring/{jobs, draft, gates, lifecycle, questions, public, expiry, drafttest}`; job/template/catalog/gate/DPIA parts of `hiringstore` (`jobs.go`, `catalog.go`, `global.go`, `registry.go`, `phase2.go`); draft/publish routes of `hiringhttp`; `cmd/hiring-expire` | regional: `hiring.job_postings`, `job_markets`, `job_skills`, `job_members`, `application_forms`, `form_versions`, `form_questions`, `templates`, `departments`, `org_counters`, `special_category_declarations`, `disqualification_rules`, `dpia_records`, `job_gate_confirmations`, `compliance_log`, `audit_events`. Global: `hiring.job_registry`, `skills`, `markets`, `seniority_levels`, `experience_ranges`, `compliance_gates` |
| **applications** | `internal/hiring/{apply, applications, screening, reapply, retention, uploads, uploads/r2, apply/redislimit}`; `internal/sensitive`; application parts of `hiringstore` (`applications.go`, `public.go` apply half, `retention.go`, `uploads.go`); apply/applications routes of `hiringhttp`; `cmd/hiring-retention`, `cmd/hiring-uploads-sweep` | regional: `hiring.applications`, `application_files`, `review_tasks`, `reapply_blocks`, `job_applicant_counts`, `org_settings`, `email_outbox`, `hiring_restricted.special_answers` |
| **notifications** | `internal/mailer`, `internal/queue`, `internal/worker/**`, `internal/hiring/outbox` (delivery half), `cmd/email-worker`, `internal/handler/admin_jobs.go` | River tables (`river_*`) |
| **shared library** | `internal/region`, `internal/dbrouter` (move to `pkg/`), `pkg/{apperror,response,besteffort,logger,config,db,redis}`, new `pkg/rpc`, `pkg/events` | none |
| **BFFs** | `internal/app` (composition root, routing), HTTP handler shells, OpenAPI (`internal/docs`, `internal/apidoc`), JWT middleware call-outs | none |

Two things stand out. First, `internal/handler` is one package of 40 files mixing identity and org; it must be split by the table above before anything else. Second, `hiringstore` is one `Store` that serves both hiring and applications (it imports `applications`, `apply`, `uploads`, `retention`, `outbox`, `screening`); it must be split along the same line.

## 2. Coupling matrix (Go imports)

Cell = number of cross-service package-to-package import edges (non-test code, shared libraries excluded).

| from \ to | identity | org | hiring | applications | notifications |
|---|---|---|---|---|---|
| **identity** | - | 0 | 0 | 0 | 0 (handlers) |
| **org** | 0 | - | 0 | 0 | 3 (`kybnotify`, `kyb-sweep` -> mailer, queue) |
| **hiring** | 0 | 2 (`draft`, `lifecycle` -> `rbac`) | - | 2 (`draft`, `hiringtypes` -> `screening`) | 0 |
| **applications** | 0 | 2 (`applications` -> `rbac`; `apply` -> `countrycode`) | 12 (`applications`, `apply`, `screening`, `uploads` -> `jobs`, `questions`, `public`, `hiringtypes`, `draft`) | - | 2 (`outbox`, `cmd/hiring-outbox` -> `mailer`) |
| **handler (identity+org, mixed)** | 2 (`auth`, `admin`) | 4 (`kyb`, `kybstore`, `onboarding`, `org`) | 0 | 0 | 2 (`mailer`, `queue`) |

Reading the matrix:

- **Identity has no Go-level dependencies on anyone.** Its coupling is all through SQL and the handler package.
- **Org and hiring have no import edges toward each other except `rbac`**; the real coupling is SQL (section 3).
- **Applications -> hiring is the heaviest edge (12).** Most of it is types, not behaviour: `jobs` (status constants, `IsUUID`, field errors), `questions` (form schema), `public` (`StateOf`, `NoticeVersion`, `ValidSource`), `hiringtypes`. `applications.Caller = draft.Caller` and `draft.ForbiddenError` (`applications.go:33,291`) is the one behavioural import.
- **Hiring -> applications (2)** is the reverse via `screening`: hiring authors knockout rules, applications evaluates them. `screening` plus `questions` are therefore a shared form-schema library, not owned by either side.
- Notifications is only ever depended on, never depending: a clean leaf.

## 3. SQL coupling (shared tables and cross-domain joins)

Every regional DB holds `accounts.*` and `hiring.*` in one schema set with real foreign keys between them, and a single pgx pool serves all code. That is the true monolith seam.

### 3.1 Foreign keys across service boundaries (must be dropped)

`db/migrations/000006_hiring.sql`: `hiring.org_settings.tenant_id`, `departments.tenant_id`, `job_postings.tenant_id` (lines 31, 37, 45) REFERENCE `accounts.organizations`; `job_postings.created_by` (46), `job_members.user_id` (198), `override_by` (159), `declared_by` (189) REFERENCE `accounts.users`; `organization_member_roles.member_id` (21) REFERENCE `accounts.organization_members`. Migrations 000010 and 000015 add four more `REFERENCES accounts.*` each. `tenant_id`/`user_id` become opaque ids validated at the API edge. Also note that `000006_hiring.sql:11-14` creates `accounts.organization_agreements` and `organization_member_roles` from a hiring migration: org-owned tables whose DDL must move into org's migration set.

### 3.2 Table-by-table cross reads and writes

| # | Where (file:line) | Reads/writes | Owner of data | Classification |
|---|---|---|---|---|
| 1 | `hiringhttp/scope.go:52-54` (`orgForUserSQL`) | `accounts.organizations` JOIN `organization_members` JOIN `organization_member_roles`: resolves company, owner flag and roles for the caller, on every hiring request; `PermissionsFor` then expands roles via `rbac.DefaultMatrix` (`scope.go:61`) | org | **sync gRPC** `OrgService.ResolveMembership`, called by the BFF once per request, result carried in `CallContext` (company_id, permissions). Hiring and applications stop resolving membership entirely |
| 2 | `hiringstore/phase2.go:397-400` (`companySetupSQL`), consumed at `draft/publish.go:75` | `organizations.kyb_status`, `organization_agreements` (accepted version), `privacy_contact_email`, `dpo_contact` | org | **sync gRPC** `OrgService.GetPublishReadiness` at publish time (strong consistency required), plus **event** `org.company.verified` so hiring can re-run blocked gates. See 4.2 |
| 3 | `hiringstore/phase2.go:445` (`acceptAgreementSQL`) | INSERT `accounts.organization_agreements` | org | **move the code** to org (`AcceptAgreement`); the hiring handler becomes a thin proxy. DPIA records stay in hiring (`hiring.dpia_records`) |
| 4 | `hiringstore/phase2.go:459` (`setPrivacyContactSQL`) | UPDATE `accounts.organizations` | org | **move the code** to org (`SetPrivacyContact`); publish `org.company.profile_updated` |
| 5 | `hiringstore/phase2.go:302-306, 338-346` | `accounts.users` + `organization_members` + `organization_member_roles` + owner check: the assignable team for a job | org + identity | **sync gRPC** `OrgService.ListMembers` then `IdentityService.BatchGetUsers`, composed in hiring (or the BFF). Membership changes invalidate via `org.membership.changed` |
| 6 | `hiringstore/jobs.go:574-578` | `hiring.job_members JOIN accounts.users` (name of hiring managers in the job list, `deleted_at IS NULL`) | identity | **sync gRPC** `IdentityService.BatchGetUsers` per page of jobs (page size bounded), or denormalize manager display names into `job_members` and refresh on `identity.user.*` events. Start with the RPC; the list page must tolerate a failed lookup by showing ids only |
| 7 | `hiringstore/catalog.go:289` (`tenantIDsSQL`) | `SELECT id FROM accounts.organizations` to iterate every company in sweepers (`cmd/hiring-expire`, retention, outbox) | org | **sync gRPC** `OrgService.ListCompanies`, or better a local `hiring.tenants` table fed by `org.company.profile_updated` / a created event (sweepers should not call org on a schedule). Initial seed via ListCompanies |
| 8 | `hiringstore/global.go:66, 122` | `accounts.onboarding_industries` (list and `EXISTS` validation of a job's industry) in the **global** DB | org | **sync gRPC** `OrgService.ListIndustries` / `ValidateIndustry` with a long TTL cache in hiring. Alternative: copy the industry catalog into hiring's own reference data (it is slow-moving, seeded by migration) |
| 9 | `hiringstore/public.go:38` (`publicCompanySQL`) | `organizations.legal_name`, privacy contact, DPO on the public careers page | org | **event + local read model**: snapshot `company_name`, privacy contact into the job at publish (carried in `hiring.job.published` and the frozen form), refresh through `org.company.profile_updated`. The public path must not need org to be up |
| 10 | `hiringstore/applications.go:334-336` (`loadChangeSQL`) | `applications JOIN job_postings JOIN accounts.organizations LEFT JOIN org_settings` to build the status-change email (title, job number, retention, company name) | hiring + org + applications | **snapshot at submit**: applications stores `job_number`, `title`, `public_id`, `retention_months`, `company_name` on the application row (from `hiring.job.published` read model). Removes both the hiring and org joins. `org_settings` moves to applications (item 14) |
| 11 | `hiringstore/retention.go:171` (`noticeRecipientSQL`) | `organizations JOIN accounts.users` (privacy contact else owner email) for the "applications deleted" notice | org + identity | **sync gRPC** `OrgService.GetCompanyContact` (org resolves the owner email via identity internally), or carry `notice_email` in the job snapshot. Prefer the RPC: it is an infrequent batch path |
| 12 | `kyb/kybnotify/notifier.go:46-47` | `organizations JOIN accounts.users` (owner email for KYB emails) | identity | stays inside org once org holds the owner email, or `IdentityService.GetUser`. Email send itself becomes an `EmailRequested` event |
| 13 | `hiringstore/applications.go:24-25`, `retention.go:28,70` | `hiring.job_postings` LEFT JOIN `hiring.job_members` (who may see which job's applications); retention joins `job_postings` for `retention_months` and closing dates | hiring | **event + local read model** in applications: a `jobs` table (id, company, status, retention, closing date) from `hiring.job.published/paused/closed` and a `job_access` table from `hiring.job.access_changed`. Sync fallback and repair: `HiringService.GetJobsSnapshot`, `CheckJobAccess` |
| 14 | `hiringstore/applications.go:461-464`, `catalog.go` (`hiring.org_settings`) | `reapply_block_months`, `send_status_emails`: application behaviour stored in a hiring table | applications | **move the code and table** to applications (`GetSettings/UpdateSettings`) |
| 15 | `hiringstore/public.go:23, 30-32, 111` (apply path) | `hiring.job_postings`, `application_forms`, `form_versions` to find the job and its frozen form at apply time | hiring | **event + local read model** (frozen form travels in `hiring.job.published`); sync fallback `HiringService.GetPublicJob`. See 4.3 |
| 16 | `hiringstore/public.go:202-206`, `applications.go:141` | `job_applicant_counts` written in the apply transaction, read by job lists in hiring | applications | **move the table** to applications; hiring reads totals with `ApplicationsService.GetApplicantCounts` (batched per page) or maintains its own counter from `applications.application.submitted` |
| 17 | `hiringstore/global.go:205-225` | `hiring.job_registry` (public id -> region, company, status) written at publish/close, read by public apply | hiring | **stays in hiring** (global DB). Applications reads the region via the job snapshot or `GetPublicJob`. Written on the same lifecycle events (item 15) |
| 18 | `handler/admin_users.go:84, 231` | `auth.users_registry` | identity | identity, via `AdminIdentityService.ListUsers` |
| 19 | `handler/admin_users.go:203-208, 260` | UPDATE/SELECT `accounts.users` (disable user) | identity | `AdminIdentityService.SetUserActive`; emit `identity.user.active_changed` |
| 20 | `handler/admin_users.go:271, 278` | `accounts.onboarding_progress`, `accounts.organizations.legal_name` for the admin user detail | org | admin-bff composes: `AdminIdentityService` user + `AdminOrgService.GetCompanyByOwner` |
| 21 | `handler/kyb.go:178` | `SELECT id FROM accounts.organizations WHERE owner_user_id` | org | already org-owned; moves with the handler |
| 22 | `handler/admin_verification.go:107, 151`; `kyb/kybstore/queue.go:20,37` | `admin.verification_queue` (global) joined to per-region `organization_verifications` | org | **org owns the queue** (`AdminOrgService.ListVerificationQueue`, `ReviewVerification`). The table is in the `admin` schema but belongs to org; rename schema in the move |
| 23 | `handler/product_onboarding_handlers.go:1072` | `SetAccountOnboardingCompleted` writes `accounts.users`/progress from the onboarding handler | org writes identity table | **move the code**: completion flag lives with onboarding progress (org) and identity learns through `identity.user.verified` or a field on the user; no shared write |
| 24 | `query/accounts.go` (single file) | users, users_registry, pending_users **and** organizations, members, invites, pending_organizations in one builder | identity and org | **move the code**: split into `identity/query` and `org/query`; no shared SQL package |
| 25 | `handler/admin_jobs.go` | reads the River queue (`fluvio`) directly | notifications | `AdminNotificationsService.ListEmailJobs/RetryEmailJob` |

### 3.3 Email: transactional enqueue across every domain

Every handler enqueues email in the same Postgres transaction as its own write, through River: `auth_handlers.go:832`, `auth_login_otp.go:187`, `auth_password.go:187`, `auth_invite.go:444`, `admin_staff.go:134`, `product_onboarding_handlers.go:1106`, `waitlist.go:199`, `kyb/kybnotify/notifier.go:84`. Hiring has its own outbox table (`hiring.email_outbox`, `hiringstore/public.go`, `retention.go:214-256`, drained by `cmd/hiring-outbox` through `mailer`).

Target: each service writes an `EmailRequested` event from its own transaction (service-local event outbox, relayed by `pkg/events`) and notifications consumes it. This keeps the "no email without commit, no commit without email" property without sharing a database. `NotificationsService.SendEmail` stays as the synchronous fallback for flows that must be acknowledged inline (OTP resend). One-time secrets (OTP, invite token) travel in the event body, so the NATS stream must be internal-only and the payload must be excluded from logs (noted in `events/v1/notifications.proto`). The hiring outbox is replaced by applications publishing the same event; `hiring.email_outbox` retention logic (`DeleteOldEmails`) becomes notifications' concern.

### 3.4 Shared code (library, not service)

| Item | Used by | Decision |
|---|---|---|
| `internal/rbac` permission slugs and default role matrix (`permissions.go`, `matrix.go`) | hiring (`draft`, `lifecycle`: ~60 checks, e.g. `draft/service.go:93-103`), applications (`applications.go:297-315`), org (computes) | **shared library** `pkg/rbac` for the slug constants; **org computes the matrix at runtime** and ships `permissions` in `CallContext`. Services only call `Has(slug)`; they never read roles |
| `hiring/questions`, `hiring/screening`, `hiring/jobs` field validation types | hiring authors, applications evaluates (`apply`, `uploads` imports) | **shared library** `pkg/formschema` (question and knockout-rule types, `FieldError`, `IsUUID`). Job status constants go to the proto enums |
| `hiring/public` (`StateOf`, `NoticeVersion`, `ValidSource`) | hiring (careers page, share kit), applications (apply) | `StateOf` is derived data: hiring computes `public_state` (see `GetPublicJobResponse`); `NoticeVersion`, `ValidSource` go to the shared form lib |
| `draft.Caller`, `draft.ForbiddenError`, `draft.ValidationError` reused by applications (`applications.go:33,291,294`) | applications | replace with `rpc.CallContext` and `pkg/apperror` |
| `internal/region`, `internal/dbrouter` | everyone | move to `pkg/region`, `pkg/dbrouter`; each service gets only its own tables, but the router (global + five regional pools) is shared infrastructure |
| `internal/countrycode` | org (`kyb`), applications (`apply`) | shared library (pure data) |
| `internal/sensitive` | applications | stays with applications |

## 4. The hard ones

### 4.1 Onboarding region migration with token swap

Today `migrateUserToRegion` (`handler/region_pending.go:125`) does, in one call: read `accounts.pending_users` (global), `pending_organizations`, `pending_onboarding_progress` (`:179-246`); write `users`, `organizations`, `onboarding_progress` into the target region's DB (`copyAccountToRegion`, `:250`, `ON CONFLICT DO NOTHING`); then update `users_registry` and delete the pending rows (`releasePendingAccount`, `:297`). Callers (`individual_onboarding.go:207`, `business_onboarding.go:217`, `product_onboarding_handlers.go:419`) then re-issue JWTs because the `region` claim is stale (`auth/jwt.go:67`, `IssuePair`, called at `individual_onboarding.go:147`, `business_onboarding.go:149`, `product_onboarding_handlers.go:73`).

It straddles two services: users and the registry are identity; organizations and progress are org. The FK `organizations.owner_user_id -> users.id` forces user-before-org order. The function is already safely re-runnable, which is the property a saga needs.

Mechanism: **saga orchestrated by org** (`OrgService.CompleteRegionOnboarding`), each step idempotent:

1. `IdentityService.PrepareRegionMove(user_id, region)`: copy the user into the target region.
2. Org copies its pending organization and progress into the target region.
3. `IdentityService.CommitRegionMove(user_id, region)`: repoint `users_registry`, delete the pending user, issue a new `TokenPair` with the new region claim, publish `identity.user.region_moved`.
4. Org deletes its pending rows and returns the tokens to the BFF, which swaps them in the response (and any cached `CallContext`).

Failure handling: crash after 1 or 2 means a retry repeats a no-op; crash after 3 but before 4 leaves only org's pending rows to clean, done on retry or by a janitor keyed off `region_moved`. A retry after 3 hits the existing `checkAlreadyMigrated` path (`:179`) and must still return fresh tokens, so `CommitRegionMove` is idempotent *including* token issuance. Until commit, the old region claim stays valid (the user still resolves to `global`), so there is no window where a token points at a region without the user. Open: the global pending tables (`accounts.pending_*`) are split by owner (identity: `pending_users`; org: `pending_organizations`, `pending_onboarding_progress`) but live in the same global DB, so migration 000002 needs ownership split, no data move.

### 4.2 Publish gating on company verification and agreements

Publish is blocked unless KYB is verified, the DPA for the current version is accepted, a privacy contact is set and a DPIA exists (`gates/publish.go:62-100`, `hiringstore/phase2.go:397-420` `CompanyState`, `draft/publish.go:75`). The inputs are org-owned rows read inside hiring's tenant transaction.

Mechanism: hybrid.

- **Authoritative check: sync** `OrgService.GetPublishReadiness` inside `PublishJob`. A stale local copy could publish for a company that was just rejected or whose DPA changed, which is a compliance failure, so the answer must be fresh at the moment of publish. Cost is one RPC on a rare, user-initiated action. If org is down, publish fails closed with a retryable error; drafting is unaffected (the gate message already says "You can keep drafting").
- **Reactive: event** `org.company.verified`, `org.agreement.accepted`, `org.company.profile_updated`: hiring consumes these to clear "blocked" state in UIs and to retry auto-publish of jobs queued behind verification. It is not the source of truth.
- Reverse path (a verified company becoming unverified while jobs are live): `org.company.kyb_rejected` triggers hiring to pause the company's published jobs (emit `hiring.job.paused`). Today no code does this; flagged as a new requirement the boundary exposes.
- DPIA and `compliance_log` stay in hiring; `AcceptDPA` and `SetPrivacyContact` move to org (items 3-4) and hiring proxies them.

### 4.3 Public careers and apply reading jobs

Public traffic (careers page, sitemap, apply) is anonymous and spiky and cannot depend on five services being up. Today: `hiring.job_registry` (global) maps public id to region and company (`registry.go`), then the regional DB is read for job, frozen form version and company (`public.go:23-38, 111`) and the apply transaction writes `applications`, `review_tasks`, `job_applicant_counts`, `email_outbox` together (`public.go:~195-210`), joining `job_postings` for state and retention.

Mechanism:

- **Careers page and sitemap: sync** `HiringService.GetPublicJob` and `ListOpenJobs` (registry plus one regional read), cacheable at the BFF by `public_id` with a short TTL and purge on `hiring.job.*` events. Hiring owns both because the page is rendered from hiring data (+ company snapshot from item 9).
- **Apply: event + local read model in applications.** `hiring.job.published` carries everything apply needs (public id, status, closing date, retention months, frozen form version and schema, company name and privacy contact). Applications validates and screens locally and writes in one local transaction (application, review task, counter, `applications.application.submitted` in its event outbox). Paused/closed events flip the local job to not-open.
- **Consistency gap**: after a pause or close there is a short window where applications still accepts. Mitigations: (1) pause/close are acceptable to be eventually consistent for seconds; (2) the reverse race is a job published but not yet projected, so apply falls back once to `HiringService.GetPublicJob` and caches. Hiring's `job_registry` status is the second guard because the BFF resolves public id there first.
- Region resolution at apply time uses `job_registry` (global); the BFF puts the resolved region into `CallContext` so applications never touches registry tables.

## 5. Mechanism summary

| Coupling | Mechanism |
|---|---|
| membership, roles, permissions per request | sync gRPC (BFF -> org), carried in CallContext |
| publish readiness (KYB, DPA, privacy contact) | sync gRPC + events for reaction |
| company name/contact for public pages and emails | event + local snapshot |
| user display data, user lookup | sync gRPC (batched) |
| job state/access/form for applications | event + local read model, sync fallback |
| applicant counts, application summaries for hiring | sync gRPC (batched) or event-fed counter |
| outbound email from any service | event (`EmailRequested`) + sync `SendEmail` fallback |
| region migration | orchestrated saga over sync gRPC |
| permission slugs, form schema, region, dbrouter | shared library (`pkg/*`) |
| agreements, privacy contact writes, onboarding completion, `org_settings`, `job_applicant_counts` | move-the-code (table moves too) |
| cross-schema FKs | dropped; opaque ids |

## 6. Suggested extraction order

1. Split `internal/handler` and `query/accounts.go` into identity and org halves inside the monolith (pure moves, no behaviour change). This is the largest pre-requisite.
2. Introduce `pkg/rbac`, `pkg/formschema`, `pkg/region`, `pkg/dbrouter`; drop hiring's `draft.Caller` reuse in applications.
3. Split `hiringstore` into hiring and applications stores; move `org_settings` and `job_applicant_counts` ownership to applications; drop cross-service FKs.
4. Replace direct `accounts.*` SQL in hiring with the org/identity clients (items 1, 2, 5-8, 11) behind interfaces hiring already has (`Directory`, `Company`, `Reference`), so the monolith can keep a local implementation until the service exists.
5. Move email to `EmailRequested` events; extract notifications first (leaf, lowest risk).
6. Extract applications, then hiring, then org and identity (org and identity last because of the region saga).

## 7. Decisions

Settled by the product owner on 2026-11-23.

1. **Waitlist: removed.** The public signup, the admin waitlist routes and permissions, the confirmation email and
   the query builders are deleted. No service owns it. The `waitlist` schema stays: it still holds the shared
   catalogs (industries, roles, business types, team sizes) that onboarding and the admin catalog CMS use. The
   waitlist data tables are untouched; dropping them is a separate, irreversible step once the data is exported or
   written off.
2. **`admin.verification_queue` belongs to identity's admin module**, not org. Org hands cases to it through
   identity's admin API and receives the decision back; the queue still stores only ids, region and status.
3. **Two RBAC models, kept apart**: admin RBAC (`admin.roles/permissions`) in identity, company RBAC
   (`internal/rbac`) in org.
4. **The BFF composes team lists** (items 5 and 6 above). Hiring stays free of identity and org clients.
5. **Publish fails closed** when org is unreachable: the request fails with a retryable error and nothing is
   published for a company that could not be checked.
6. **Public job payloads keep `details_json` / `form_schema_json` as JSON strings** in `GetPublicJob`, so the draft
   schema is not frozen into the wire contract.

Consequence of 2: the proto for the admin verification queue moves from the org contract to identity's.

## 8. Progress notes (integration branch)

Extracted and merged on `split/integration`: notifications (events transport, own inbox database), applications
(plus job read model, applicant counts over gRPC, audit trail), hiring, org, identity, web-bff and admin-bff, with
the hardening pass (TOTP key rotation, stream retention, structured logs). Each extraction is behind a switch that
defaults to the monolith behaviour (see [switches.md](switches.md)); the all-remote combination is exercised by
`scripts/e2e-auth.sh`, `e2e-apply.sh` and `e2e-identity.sh` through the web-bff (see [local-dev.md](local-dev.md)).

Still in the monolith: the admin catalogs and jobs routes (`internal/handler`, now only `AdminHandler` catalogs/jobs,
session rotation and health), the monolith's own copies of the org/identity handlers behind `ORG_REMOTE=off` /
`IDENTITY_REMOTE=off`, and the interim shared-database reads (`*=db` defaults) that the `grpc` switches replace one by
one. Open: `AdminOrgService` has no case-details RPC, so `GET /admin/verification/{id}` with `ORG_APPLY=grpc` is served
from identity's own read of the queue item; the region-migration saga (4.1) still runs inside org's handlers.
