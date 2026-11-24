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
| `cmd/org`, `cmd/tools/orgprobe` | Binary and a gRPC probe. |

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
| `GetCompany`, `GetCompanyContact`, `GetPublishReadiness`, `ListMembers`, `AddMember`, `SetMemberRoles`, `AcceptAgreement`, `SetPrivacyContact`, `ListCompanies`, `ListIndustries`, `ValidateIndustry`, `GetKybStatus` | implemented |
| `AdminOrgService.ApplyKybDecision`, `GetCompanyByOwner`, `DeleteCompany` | implemented |
| `SubmitKyb` | Unimplemented: the request has no legal name or address; use the HTTP flow |
| `CompleteRegionOnboarding` | Unimplemented: the saga is not extracted |

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

`ApplyKybDecision` runs the same `kyb.Service.Review` as the monolith's admin handler. Identity closes its queue item
first, so closing is lenient (`kybidentity.LenientResolve`).

## Personal organizations (individual accounts)

An individual that completes onboarding gets an implicit organization (`accounts.organizations.kind = 'individual'`, migration
`000024_org_kind.sql`; every older row is `business`), so hiring, applications, agreements, readiness and gRPC membership work for
individuals through the paths above. Verification is a manual review through identity's queue with `case_type = 'individual'`
(`admin.verification_queue.case_type`, global migration `000017`; `EnqueueVerificationRequest.case_type`). Contract, rules and open legal
questions: [individual-accounts.md](individual-accounts.md). Code: `orgstore.EnsurePersonal` / `RequestPersonalReview`, called from
`POST /onboarding/complete`; `cmd/org-backfill-individuals` migrates existing accounts. Every `org.*` event carries `company_kind`.

## Switches

| Variable | Where | Meaning |
|---|---|---|
| `UPSTREAM_ORG_URL` | web-bff | send `bff.OrgPath` to the org service |
| `ORG_REMOTE=on` | monolith | stop serving the org routes and hiring's agreements/privacy-contact routes |
| `ORG_EVENTS=on` | monolith, org | emit `org.*` events through the regional outbox (relays run in the process; needs `NATS_URL`) |
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
- **Region migration**: unchanged. Onboarding steps still migrate a pending account inline, in the org process, writing
  `auth.users_registry` and `accounts.users` (identity tables). The saga needs: `identity.PrepareRegionMove` /
  `CommitRegionMove`, org copying `pending_organizations` and onboarding progress between them, an idempotent
  saga record, and the BFF swapping tokens (`CompleteRegionOnboarding`).
- `/auth/me` role/permission lookup still goes through hiring's DB directory in the monolith.
- Swagger (`internal/docs/spec`) was not regenerated; annotations were repointed to `orghttp.`.
