# Environment switches

Every switch that changes which process serves a route, where data is read from, or which events leave a service.
Defaults keep the monolith behaviour; nothing here is needed to run `make local-up`. "Compose" is
`deploy/local/compose.split.yml`, where each variable is passed through with the same name.

## Moving routes out of the monolith

| Name | Default | Service | Meaning |
| --- | --- | --- | --- |
| `APPLICATIONS_REMOTE` | `off` | monolith, hiring | `on`: stop serving the applications routes (`bff.ApplicationsPath`); `cmd/applications` does |
| `HIRING_REMOTE` | `off` | monolith | `on`: stop serving jobs, drafts, forms, templates, catalog, settings, careers pages, public job JSON, DPIA |
| `ORG_REMOTE` | `off` | monolith, hiring | `on`: stop serving countries, onboarding, company verification, agreements and privacy-contact (`bff.OrgPath`) |
| `IDENTITY_REMOTE` | `off` | monolith | `on`: stop serving `/auth/*`, `POST /organizations/invites` and the identity half of `/admin` |
| `UPSTREAM_API_URL` | required | web-bff, admin-bff | the monolith, default upstream for everything not routed elsewhere |
| `UPSTREAM_APPLICATIONS_URL` | empty | web-bff | route `ApplicationsPath` to this URL |
| `UPSTREAM_HIRING_URL` | empty | web-bff | route `HiringPath` to this URL |
| `UPSTREAM_ORG_URL` | empty | web-bff | route `OrgPath` to this URL |
| `UPSTREAM_IDENTITY_URL` | empty | web-bff | route `IdentityPath` to this URL |

The route sets are disjoint (tests in `internal/bff`): agreements and privacy-contact belong to org, `/organization/dpia`
to hiring, `/organizations/invites` to identity. Set a `*_REMOTE` flag and its `UPSTREAM_*_URL` together.

## Where a service reads its collaborators from

| Name | Default | Service | Meaning |
| --- | --- | --- | --- |
| `APPLICATIONS_JOBSOURCE` | `db` | applications | `db`: read hiring tables; `events`: local read model fed by `hiring.job.*` |
| `CALLER_RESOLVER` | `db` | applications | company and roles of the caller: `db` (accounts tables) or `grpc` (`OrgService.ResolveMembership`) |
| `APPLICANT_COUNTS` | `db` | monolith, hiring | applicant counters on the job list: `db` or `grpc` (`ApplicationsService.GetApplicantCounts`). A failing source gives `applicant_count`/`new_applicant_count` = `null` and `applicant_counts_available=false`, not zero |
| `ORG_READINESS` | `db` | hiring | publish readiness from accounts tables or `OrgService.GetPublishReadiness` (fails closed with 503) |
| `ORG_MEMBERSHIP` | `db` | hiring, identity, monolith | caller membership from accounts tables or `OrgService.ResolveMembership`. In identity (`/auth/me` roles and permissions) `grpc` fails closed: org down answers 503, never empty roles; no database fallback |
| `ORG_ONBOARDING` | `db` | identity, monolith | onboarding progress on `/auth/me` and progress plus owned company name on `GET /admin/users/{id}` from accounts tables or `OrgService.GetOnboardingProgress` / `AdminOrgService.GetCompanyByOwner`. `grpc`: `/auth/me` and login answer 503 while org is down; the admin detail leaves the two fields out (logged). Accounts still in the global holding region keep reading their own pending progress |
| `ORG_INDUSTRIES` | `db` | hiring | industry catalog from the global DB or `OrgService.ListIndustries` |
| `ORG_TENANTS` | `db` | hiring-expire, applications (`APPLICATIONS_JOBSOURCE=events`) | companies to sweep: `db` or `OrgService.ListCompanies` |
| `ORG_NOTICE` | `db` | applications (events source) | retention notice recipient: `db` (privacy contact from job events, else hiring/accounts tables) or `OrgService.GetCompanyContact` (first choice; events value if org fails) |
| `APPLICATIONS_JOBFALLBACK` | `db` | applications (events source) | `off`: the read model never reads hiring tables (no read-through, unknown job is not found). Fallback use is counted at `GET /internal/jobmodel-stats` on the service port |
| `ORG_COMPANY` | `db` | hiring | company row of the public page and job events (`OrgService.GetCompany`, 60 s cache, stale on error), settings page (`GetPublishReadiness`), accept DPA (`AcceptAgreement`), privacy contact (`SetPrivacyContact`). Org down and cold cache: 503 / the job write fails closed |
| `IDENTITY_USERS` | `db` | hiring | hiring managers' names on the job list via `IdentityService.BatchGetUsers` at `IDENTITY_GRPC_ADDR` (deactivated users dropped). Team lists (`/hiring/people`) stay with the BFF composition |
| `ORG_APPLY` | `db` | identity, monolith | also required for `DELETE /admin/companies/{id}` (`AdminOrgService.DeleteCompany`, 503 otherwise). KYB decision reaches org by writing regional tables (`db`) or `AdminOrgService.ApplyKybDecision` (`grpc`). With `grpc`, `GET /admin/verification/{id}` falls back to identity's own read of the queue item because the contract has no case-details RPC |
| `REGION_MOVE` | `inline` | org, monolith (while it serves org) | `inline`: the onboarding step moves the account out of the global holding area inside the handler. `saga`: the persisted, resumable region-migration saga (org drives, identity over `IDENTITY_GRPC_ADDR`, in-process when unset); needs migration `global_registry/000018`. See org.md |
| `REGION_MOVE_RESUME_INTERVAL` | `30s` | org, monolith | how often stalled sagas are resumed (a saga idle for a minute is picked up) |
| `IDENTITY_QUEUE` | `db` | org | KYB review items: write `admin.verification_queue` (`db`) or `EnqueueVerification` (`grpc`) |
| `GRPC_AUTH` | `off` | all gRPC services and clients | `off`, `token` or `mtls`; see hardening.md section 7. Companions: `GRPC_AUTH_SECRET(_FILE)`, `GRPC_TLS_CERT/KEY/CA(_FILE)`, `GRPC_SERVICE_NAME`. Compose passes them through; certificates from `make local-grpc-certs` |
| `*_GRPC_ADDR` | compose: `<service>:9090` | all | `IDENTITY_`, `ORG_`, `HIRING_`, `APPLICATIONS_`, `NOTIFICATIONS_` gRPC address; `ORG_GRPC_TIMEOUT` (3s) bounds each call |

## Events

| Name | Default | Service | Meaning |
| --- | --- | --- | --- |
| `NATS_URL` | empty | all | the bus; required by any switch below that publishes or consumes |
| `NOTIFICATIONS_TRANSPORT` | `fluvio` (compose: `events`) | monolith, org, identity | email requests through Fluvio or the events outbox |
| `HIRING_EVENTS` | `off` | hiring (monolith while it serves hiring) | emit `hiring.job.*` and `members_changed` through the outbox. Compose: `SERVER_HIRING_EVENTS=off` turns it off on the monolith when `HIRING_REMOTE=on` |
| `APPLICATIONS_EVENTS` | `off` | applications | emit `applications.application.*` |
| `APPLICATIONS_ORG_EVENTS` | `off` | applications | consume `org.company.deleted.v1` and delete that company's applications data; `org.company.reactivated.v1` lifts the tombstone |
| `HIRING_ORG_EVENTS` | `off` | hiring | consume `org.company.deleted.v1` (inbox consumer `hiring-org`) and archive the company's live jobs; `org.company.reactivated.v1` is logged only |
| `ORG_EVENTS` | `off` | org (monolith unless `ORG_REMOTE=on`) | emit `org.*`, including `org.company.reactivated.v1` and `org.region.moved.v1` |
| `IDENTITY_EVENTS` | `off` | identity (monolith unless `IDENTITY_REMOTE=on`) | emit `identity.*` (including `identity.staff.deactivated.v1`) and, in the identity service, consume that event (`identity-staff`: revoke the member's admin sessions) |
| `NOTIFICATIONS_STREAM_MAX_AGE` | stream default | every process touching the notifications stream | JetStream retention, 5m to 168h; must be identical everywhere |
| `NOTIFICATIONS_DB_URL` | global DB (compose: `openhr_notifications`) | notifications | database of the events inbox |

## Edge (web-bff / admin-bff)

| Name | Default | Meaning |
| --- | --- | --- |
| `SESSION_VALIDATION` | `off` | web-bff validates the session cookie in Redis, refuses protected paths without one, forwards `X-Openhr-*` |
| `COOKIE_SECURE` | `true` (compose: `false`) | selects `__Host-` cookie names; must match across monolith, services and BFFs |
| `SESSION_IDLE_TTL` / `SESSION_ABSOLUTE_TTL` | 7200 / 43200 | seconds, must match the issuing service |
| `ALLOWED_ORIGINS` | none | browser origins accepted on unsafe methods (admin-bff: `ADMIN_BFF_ALLOWED_ORIGINS` in compose) |
| `ORIGIN_REQUIRED`, `HSTS_ENABLED` | `false` | origin enforcement without an Origin header; always send HSTS |
| `RATE_LIMIT_GLOBAL`, `RATE_LIMIT_FAIL_CLOSED_AUTH`, `RATE_LIMIT_FAIL_CLOSED_OTHER` | `300/1m`, `true`, `false` | see `bff.md` |
| `MAX_BODY_BYTES`, `UPSTREAM_TIMEOUT` | 10485760, 30s | request body cap and upstream response-header timeout |

## Secrets and misc

| Name | Default | Meaning |
| --- | --- | --- |
| `TOTP_ENCRYPTION_KEY` | dev default in compose | current TOTP secret key; every process builds `auth.NewTOTPCryptoKeys(key, previous)` |
| `TOTP_ENCRYPTION_KEY_PREVIOUS` | empty | comma separated retired keys, tried after the current one on decrypt (`cmd/totp-rekey`) |
| `CLAMAV_ADDR` | empty | applications: malware scanner (`clamav:3310`); empty makes uploads fail closed with 503 |
| `APP_URL` | compose: `http://localhost:3000` | the product frontend origin, the **only** source of the links in product emails (password reset `/reset-password?token=`, org invite `/invite?token=`, dashboard links). Read by `cmd/notifications`, `cmd/email-worker`, `cmd/hiring-outbox` through `mailer.AppLinkURLs`. Must be an absolute URL without query; in staging, prod and production it is required and must be https, or those processes refuse to start. Remapped local stacks set it to their own frontend origin |
| `ADMIN_APP_URL` | compose: `http://localhost:3001` | the admin console origin, the only source of the admin invite link (`/admin/accept-invite?token=`). Same validation and requirement as `APP_URL` |
| `ANGLEHR_<REGION>_R2_BUCKET` | `openhr-<region>` | per-region bucket the regional router requires, also on services that store no files |
| `AUTH_MODE` (scripts) | `cookie` | `scripts/e2e-auth.sh`: `cookie` or `bearer` |

## Public job link (`PUBLIC_SITE_URL`)

The publish dialog's share link and the candidate privacy-notice link are built from `PUBLIC_SITE_URL`, falling
back to `PUBLIC_API_URL`. Set it to the origin that serves `/j/{public_id}` (the web BFF in the split, or the
careers site), for example `https://tryopenhr.com`. Left empty, links point at the API origin.
