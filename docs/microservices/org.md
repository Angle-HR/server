# Org service

Third extracted service (after notifications and applications). Owns companies: onboarding, KYB, agreements and the
privacy contact, company membership and roles.

```
browser -> web-bff --(org paths: bff.OrgPath)--> cmd/org --> regional DBs (accounts.*) + global DB (onboarding catalogs)
              |                                     |  gRPC OrgService / AdminOrgService (:9090)
              |                                     +--> identity: EnqueueVerification (IDENTITY_QUEUE=grpc) or admin.verification_queue (db)
              +--(everything else)--> monolith      +--> outbox -> NATS: org.* events (ORG_EVENTS=on)
```

## Package map

| Package | What |
|---|---|
| `internal/orghttp` | The org HTTP handlers, moved by `git mv` out of `internal/handler`: onboarding (product, individual, business), address search/verify, KYB, countries, catalogs, region-pending helpers, plus `company.go` (agreements, privacy contact). `Handlers.Mount` serves them in `cmd/org`; the monolith mounts the same handlers unless `ORG_REMOTE=on`. |
| `internal/orgsvc` | `Run` (wiring), HTTP router (same middleware, CORS and Origin guard as the monolith), gRPC server (`RPC`). |
| `internal/orgstore` | Company data layer (organization row, members and roles, agreements, privacy contact, readiness, membership resolution with `pkg/rbac`). |
| `internal/orgevents` | Event contract and outbox enqueue helpers; `KYBSaveHook` for `kybstore`. |
| `internal/kyb/kybidentity` | The review-queue port towards identity (`db` or `grpc`). |
| `internal/hiring/callerresolver/grpc.go` | gRPC client for the caller resolver (`CALLER_RESOLVER=grpc`). |
| `cmd/org` | Binary. |

## HTTP routes (same cookies, CSRF and origin rules as the monolith)

`GET /countries`; `/onboarding/*` (business-types, industries, company-roles, identification-requirements, status,
profile, address, address/search, address/verify, compliance, business, complete, individual);
`/organization/verification` (+ retry, confirm-name, confirm-address, change-country);
`GET|POST /organization/agreements`, `PUT /organization/privacy-contact`.
The BFF matches these exactly (`bff.OrgPath`, tested; every path reaches one upstream). `/organization/dpia` stays with
hiring, `/organizations/invites` and `/auth/*` with identity.

## gRPC (`proto/openhr/org/v1`)

The caller's region travels in the `pkg/rpc` call context (`x-openhr-region`); a company id alone does not say which
regional database holds it.

| RPC | State |
|---|---|
| `ResolveMembership` | implemented (`has_company=false` for no company; `FailedPrecondition` for an account still in the global region) |
| `GetOnboardingProgress` | implemented (user id in the request, region from the call context; `found=false` when the user has no progress row; `FailedPrecondition` for a global-region account). Identity reads `/auth/me` progress and the admin user detail through it (`ORG_ONBOARDING=grpc`) |
| `GetCompany`, `GetCompanyContact`, `GetPublishReadiness`, `ListMembers`, `AddMember`, `SetMemberRoles`, `AcceptAgreement`, `SetPrivacyContact`, `ListCompanies`, `ListIndustries`, `ValidateIndustry`, `GetKybStatus` | implemented |
| `AdminOrgService.ApplyKybDecision`, `GetCompanyByOwner`, `DeleteCompany` | implemented |
| `AdminOrgService.ReactivateCompany` | implemented: undoes `DeleteCompany`, emits `org.company.reactivated.v1` |
| `CompleteRegionOnboarding` | implemented with `REGION_MOVE=saga` (else Unimplemented): runs the region-migration saga, issues no tokens |
| `SubmitKyb` | Unimplemented: the request has no legal name or address; use the HTTP flow |

`DeleteCompany(company_id, region, admin_id, reason)` sets `accounts.organizations.deleted_at` (migration
`000023_org_company_deleted_at.sql`) and enqueues `org.company.deleted.v1` in the same transaction (needs `ORG_EVENTS=on`
to leave the outbox). It is idempotent: a second call answers `already_deleted=true` and emits nothing (the event id
derives from the company). From then on `GetCompany`, `ResolveMembership`, `GetCompanyContact`, `GetPublishReadiness`,
`ListCompanies` and `GetCompanyByOwner` no longer see the company. Identity's admin module exposes it as
`DELETE /admin/companies/{id}?region=uk` (permission `companies:delete`, granted to `superadmin` only by
`global_registry/000013`; optional body `{"reason": "..."}`); it calls `AdminOrgService.DeleteCompany` and never
touches org tables, so it needs `ORG_APPLY=grpc` (otherwise 503). The web-bff/admin-bff route `/admin/companies` to
identity. Consumers: applications (`APPLICATIONS_ORG_EVENTS=on`: applications, answers, files and objects, job read model;
a tombstone `hiring.app_deleted_companies` keeps later job events from restoring it) and hiring (`HIRING_ORG_EVENTS=on`,
inbox consumer `hiring-org`: archives the live jobs, which emits their closed events and updates the public registry).

`ReactivateCompany(company_id, region, admin_id, reason)` clears `deleted_at` (idempotent: a company that is not deleted
answers `already_active=true` and emits nothing) and enqueues `org.company.reactivated.v1` in the same transaction.
Identity's admin module exposes it as `POST /admin/companies/{id}/reactivate?region=uk` (same permission as the delete,
needs `ORG_APPLY=grpc`). Reactivation does not restore data: applications and hiring removed or archived it when they
consumed the deletion, so the company starts empty. Applications lifts its tombstone (job events are projected again);
hiring only logs it, archived jobs stay archived.

`ApplyKybDecision` runs the same `kyb.Service.Review` as the monolith's admin handler. Identity closes its queue item
first, so closing is lenient (`kybidentity.LenientResolve`).

## Personal organizations (individual accounts)

An individual that completes onboarding gets an implicit organization (`accounts.organizations.kind = 'individual'`, migration
`000024_org_kind.sql`; every older row is `business`), so hiring, applications, agreements, readiness and gRPC membership work for
individuals through the paths above. Verification is a manual review through identity's queue with `case_type = 'individual'`
(`admin.verification_queue.case_type`, global migration `000017`; `EnqueueVerificationRequest.case_type`). Contract, rules and open legal
questions: [individual-accounts.md](individual-accounts.md). Code: `orgstore.EnsurePersonal` / `RequestPersonalReview`, called from
`POST /onboarding/complete`; `cmd/org-backfill-individuals` migrates existing accounts. Every `org.*` event carries `company_kind`.

## Region migration saga

An onboarding account lives in the global holding area (`accounts.pending_users`, `pending_organizations`,
`pending_onboarding_progress`, `auth.users_registry.region = 'global'`) until the step that gives it a country
(business address, individual and business one-shot onboarding). The saga moves it into that country's regional
database. Identity owns the user row and the registry, org owns the organization draft and the progress. Org drives it
(`internal/regionsaga`, `regionsaga.Coordinator`), identity serves its half as `IdentityService.PrepareRegionMove`,
`CommitRegionMove` and `AbortRegionMove`.

| State | Next step | Owner | Effect (all idempotent) |
|---|---|---|---|
| `started` | `PrepareRegionMove` | identity | copy the pending user into the regional `accounts.users` (`ON CONFLICT DO NOTHING`; `already_present` on a retry) |
| `identity_prepared` | copy | org | copy the pending organization and progress into the region |
| `org_copied` | `CommitRegionMove` | identity | in one transaction repoint `users_registry` at the region and delete the pending user. **Point of no return** |
| `identity_committed` | release | org | delete the pending organization and progress, enqueue `org.region.moved.v1` in the region's outbox |
| `completed` | | | |

Failure before the commit compensates in reverse: `compensating` -> org deletes its regional copies, identity
`AbortRegionMove` deletes the copied user (refused with `FailedPrecondition` once the registry points at the region) ->
`compensated`. Until the commit the registry still says `global` and the pending rows are untouched, so the account
stays usable and a new attempt starts from scratch. After the commit the saga only rolls forward. Compensation and
forward steps retry in place (2 retries, backoff) before the saga gives up on the call.

State is persisted after every step in `accounts.region_move_sagas` (global DB, migration `global_registry/000018`;
state, attempts, `last_error`, a lease so two processes never run the same saga, a unique index allowing one live saga
per user). `Move` is safe to call again after any failure: a finished move answers at once, a live one is continued (or
waited for when another request holds it, up to 10 s, else `ErrBusy`), a request that finds a stalled compensation
finishes it and then starts a fresh attempt. A saga towards another region than the live or completed one is refused
(`FailedPrecondition`). The resumer (`REGION_MOVE_RESUME_INTERVAL`) picks up sagas nobody touched for a minute, which
is how a crash between steps (or a failed call after the commit) heals without a client request.

No tokens: identity's contract once returned a JWT `TokenPair`; sessions are server-side now, so `CommitRegionMove` and
`CompleteRegionOnboarding` leave the token fields empty and the HTTP handlers rotate the session as before
(`rotateIfMarked`). Migration `000018` also drops the foreign keys from the pending organization and progress tables to
`pending_users` (identity deletes the user before org releases its rows).

`org.region.moved.v1` is written in the target region's outbox (partition key = the user id, id derived from user and
region, so a re-run cannot emit it twice). No service in this repository consumes it: it is for the services that cache
a user's region. Operating it: a saga stuck in `compensating` or `identity_committed` shows `last_error` and
`attempts`; `SELECT * FROM accounts.region_move_sagas WHERE state NOT IN ('completed','compensated')`.
Checked by `scripts/e2e-region-saga.sh` (see local-dev.md).

## Switches

| Variable | Where | Meaning |
|---|---|---|
| `UPSTREAM_ORG_URL` | web-bff | send `bff.OrgPath` to the org service |
| `ORG_REMOTE=on` | monolith | stop serving the org routes and hiring's agreements/privacy-contact routes |
| `ORG_EVENTS=on` | monolith, org | emit `org.*` events through the regional outbox (relays run in the process; needs `NATS_URL`) |
| `REGION_MOVE=inline\|saga` | monolith (while it serves org), org | `inline` (default): the onboarding step that gives the account a country copies it into the region inside the handler. `saga`: the persisted, resumable saga below, identity over gRPC at `IDENTITY_GRPC_ADDR` (without it identity's half runs in-process, for the monolith) |
| `REGION_MOVE_RESUME_INTERVAL` | org | how often the resumer looks for stalled sagas (Go duration, default `30s`; a saga is picked up after one minute without progress) |
| `IDENTITY_QUEUE=db\|grpc` | org | `db` (default) writes `admin.verification_queue` in the global DB; `grpc` calls `AdminIdentityService.EnqueueVerification` at `IDENTITY_GRPC_ADDR`. Org never owns the queue. |
| `CALLER_RESOLVER=db\|grpc`, `ORG_GRPC_ADDR` | applications service (any user of `callerresolver.FromEnv`) | resolve company and roles from the accounts tables (default) or via `ResolveMembership` |
| `NOTIFICATIONS_TRANSPORT` | org | `events` (outbox, notifications service) or `fluvio`, as the monolith |

Local: `ORG_REMOTE=on ORG_EVENTS=on UPSTREAM_ORG_URL=http://org:8080 make local-up PROFILES="org applications web-bff"`.

## Events (`/services/org`, stream ORG, partition key = company id)

| Type | When | Payload (no PII) |
|---|---|---|
| `org.company.verified.v1` | KYB verified (automatic or reviewer approval) | company_id, region, status, verified_at |
| `org.company.kyb_rejected.v1` | KYB failed or reviewer rejected | company_id, region, status, reason (code), rejected_at |
| `org.agreement.accepted.v1` | agreement version accepted | company_id, region, kind, version, accepted_by_user_id, accepted_at |
| `org.company.profile_updated.v1` | privacy contact changed | company_id, region, changed (field names only), updated_at |
| `org.company.deleted.v1` | company deleted by `DeleteCompany` | company_id, region, deleted_at, reason (operator text, optional) |
| `org.company.reactivated.v1` | `ReactivateCompany` undid a deletion | company_id, region, reactivated_at, reason (optional) |
| `org.region.moved.v1` | the region-migration saga finished | saga_id, user_id, company_id and company_kind (when the account has an organization), from_region (`global`), region (target), moved_at |

Written in the same transaction as the change; ids derive from the content so a repeat is a no-op. They carry no email
addresses or names (the proto catalog's `CompanyProfileUpdated` carries the contact; this contract deliberately does
not, consumers call `GetCompanyContact`). `org.company.deleted.v1` is produced by `DeleteCompany` only (`kyb-sweep` still only flags); the deletion is a
soft delete of the org row, the owner's user account is untouched. `org.membership.changed` is not emitted.

## Interim pieces and gaps

- **Verification queue**: owned by identity's admin module. The monolith's admin handler still resolves items through
  `kybstore.Queue` (db). Decisions made through that path do not emit `org.company.*` events (the handler is identity's
  file); decisions through `ApplyKybDecision` and automatic KYB do.
- **Agreements response** also carries `dpia_version` and `automated_screening_enabled`, read from `hiring.*` tables
  (`orgstore.Extras`), to keep the page's shape. Hiring should serve those and the BFF compose. Hiring's own copy of
  the agreements handlers stays for `ORG_REMOTE=off`; `CurrentDPAVersion` is duplicated (a test keeps both equal).
- **Org invites** (`POST /organizations/invites`, accept) stay with the identity half (`auth_invite.go`, per
  `proto/openhr/identity`). `AddMember` exists for identity to call; today accept writes the member row itself.
- **Org settings**: `hiring.org_settings` moved to applications; nothing org-side remains. **Admin catalogs**
  (`/admin/catalogs/*`) are methods of the monolith's `AdminHandler` and stay there until admin-bff.
- **Shared copies**: `internal/handler/split_shared.go`, `auth_doc_types.go` and `split_shared_test.go` hold copies of
  declarations the identity half used from moved files; `internal/orghttp/shared.go` holds copies the other way.
  Delete them when identity's split lands.
- **Region migration**: with `REGION_MOVE=inline` (default) onboarding steps still migrate a pending account inside the
  handler, in the org process, writing `auth.users_registry` and `accounts.users` (identity tables). `REGION_MOVE=saga`
  replaces that with the saga below; once it is on everywhere the inline path (`orghttp.migrateUserToRegion`) can go.
- `/auth/me` role/permission lookup still goes through hiring's DB directory in the monolith.
- Swagger (`internal/docs/spec`) was not regenerated; annotations were repointed to `orghttp.`.
