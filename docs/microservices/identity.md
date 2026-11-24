# Identity service

Third extracted service (after notifications and applications). It owns people and sessions: product signup, email
verification, login (password, OTP, TOTP), password reset, logout, org-invite acceptance, `/auth/me`, the session store
writes, and the platform admin console's own auth, staff, roles, audit log, product user list/disable and the KYB
review queue.

```
browser -> web-bff --(identity paths)--> cmd/identity --> global + regional DBs (users, registry, admin.*)
              |                              |  outbox -> NATS: identity.* events, notifications.email.requested.v1
              |--(everything else)--> monolith (IDENTITY_REMOTE=on: no identity routes)
              `-- SESSION_VALIDATION=on: cookie checked against the same Redis, X-Openhr-* forwarded
```

Code: `cmd/identity`, `internal/identitysvc` (host: HTTP router, gRPC, outbox relays), `internal/identityhttp` (the
handlers, mounted by both the monolith and the service through `identityhttp.Mount`), `internal/userdir` (registry and
user directory, shared by HTTP and gRPC), `internal/verifyqueue` (review queue), `internal/identityevents` (event
contract).

## Switches

| Variable | Where | Meaning |
|---|---|---|
| `IDENTITY_REMOTE=on` | monolith | stop serving `/auth/*`, `POST /organizations/invites` and the identity half of `/admin` (admin catalogs and jobs stay) |
| `UPSTREAM_IDENTITY_URL` | web-bff | send `bff.IdentityPath` to the service; empty keeps everything on the monolith |
| `SESSION_VALIDATION=on` | web-bff | validate the session cookie in Redis at the edge (default off); also reads `COOKIE_SECURE`, `SESSION_IDLE_TTL`, `SESSION_ABSOLUTE_TTL` like the monolith |
| `IDENTITY_EVENTS=on` | identity (and monolith while it serves identity) | emit the events below through the outbox (needs `NATS_URL`) |
| `ORG_APPLY=db\|grpc` | identity / monolith | how a KYB review decision reaches org: `db` writes org's regional tables (interim), `grpc` calls `AdminOrgService.ApplyKybDecision` at `ORG_GRPC_ADDR` |
| `ORG_MEMBERSHIP=db\|grpc` | identity / monolith | `/auth/me` roles and permissions from the accounts tables (`db`) or `OrgService.ResolveMembership` (`grpc`, 503 when org is down) |
| `ORG_ONBOARDING=db\|grpc` | identity / monolith | onboarding progress (`/auth/me`, login) and the admin user detail's progress and company name from the accounts tables or org (`GetOnboardingProgress`, `GetCompanyByOwner`) |
| `NATS_URL` | identity | required: mail requests always leave through the outbox (notifications events transport) |

The service reads the monolith's configuration for sessions, cookies, CORS/origins, databases, admin bootstrap and
`TOTP_ENCRYPTION_KEY` (the base still calls `auth.NewTOTPCrypto`; switch to `NewTOTPCryptoKeys` when the hardening
branch is merged).

Local: `IDENTITY_REMOTE=on IDENTITY_EVENTS=on UPSTREAM_IDENTITY_URL=http://identity:8080 SESSION_VALIDATION=on make
local-up PROFILES="identity web-bff"`, then `BASE=http://localhost:8081/api/v1 AUTH_MODE=cookie scripts/e2e-auth.sh`
and `scripts/e2e-identity.sh`.

## Admin TOTP

Second factor for the admin console, built on the product TOTP pieces (`auth.TOTPCrypto` with `TOTP_ENCRYPTION_KEY` /
`TOTP_ENCRYPTION_KEY_PREVIOUS`, `auth.LoginLockout`); `cmd/totp-rekey` also re-encrypts `admin.users` (via
`DB_URL_GLOBAL`). Columns `admin.users.totp_secret` (encrypted) and `totp_enabled_at` come from global migration
`000012_admin_totp.sql`. There are no recovery codes (the product TOTP has none either): the recovery path is a
superadmin reset.

| Route | Auth | Behaviour |
|---|---|---|
| `POST /admin/auth/login` | public | password right and TOTP enabled: `{totp_required:true, expires_in:300}` plus the single-use `openhr_admin_preauth` cookie, **no session**; TOTP not enabled: session as before, with `totp_enrollment_required:true` when `ADMIN_TOTP_REQUIRED` is on |
| `POST /admin/auth/login/totp` `{code}` | pre-auth cookie | right code: pre-auth spent (single use), session issued with the MFA flag, `csrf_token` in the body; wrong: 400 `invalid_verification_code`, pre-auth stays; locked: 429 `too_many_attempts` |
| `POST /admin/auth/totp/enroll` | admin session | new secret + `otpauth_url` (stored encrypted, inactive); 409 when enabled; repeatable until confirmed |
| `POST /admin/auth/totp/confirm` `{code}` | admin session | enables TOTP, revokes all the admin's sessions and returns a fresh one (new CSRF token) |
| `POST /admin/auth/totp/disable` `{password,code}` | admin session | self-disable; 403 while `ADMIN_TOTP_REQUIRED` is on; signs the admin out everywhere |
| `POST /admin/staff/{id}/totp/reset` | `admins:write` **and** role `superadmin`, not for yourself | clears the secret, revokes the target's sessions, lifts their lockout |

`/admin/auth/me` and `GET /admin/staff` expose `totp_enabled` (`me` also `totp_enrollment_required`).

Guess limiting: five wrong codes in five minutes lock that admin's TOTP checks for fifteen minutes (Redis prefix
`admin_totp`, separate from the product `totp`; pre-auth state lives in the `admin_sess_pre` namespace, separate from
the product `sess_pre`). Audit actions (`admin.audit_logs`): `admin.totp.enroll_start`, `admin.totp.enroll`,
`admin.totp.verify`, `admin.totp.fail` (meta `locked`), `admin.totp.disable`, `admin.totp.reset`.

`ADMIN_TOTP_REQUIRED=true|false`: unset means true in production, prod and staging and false elsewhere; an explicit
false there fails config validation. When on, a session without the MFA flag (password-only, invite acceptance) gets
403 `TOTP_ENROLLMENT_REQUIRED` on every admin route except `/admin/auth/me`, `/admin/auth/totp/enroll` and
`/admin/auth/totp/confirm`, and gRPC `ValidateAdminToken` rejects it. The monolith (admin catalogs and jobs) applies the
same gate through the shared admin middleware, so set the variable on both processes. The BFF edge only checks that a
session exists; the MFA gate is enforced by the services. Admin BFF: set `UPSTREAM_IDENTITY_URL` on it too when
`IDENTITY_REMOTE=on` (its paths are `bff.IdentityPath`; rate-limit class `ADMIN_LOGIN_TOTP` covers `login/totp`).
Proof: `scripts/e2e-admin-totp.sh` (uses `cmd/tools/totpcode` as the authenticator).

## Signup/OTP code attempts

`POST /auth/verify-email` and `POST /auth/login/otp/verify`: each code allows 5 wrong guesses (counter per live
verification session, which is one per email and purpose; stored with the code's lifetime). A wrong code answers 400
`invalid_verification_code` with `details.attempts_remaining`; after the fifth, every further try (even the right code)
answers 429 `too_many_attempts` with `Retry-After` and `details.retry_after_seconds`. `POST /auth/resend-verification`
issues a fresh code and resets the budget.

## Audit log read path (`GET /admin/audit-logs`)

Staff audit trail (`admin.audit_logs`, written by every admin mutation), permission `audit:read`; the admin BFF routes
`/admin/audit-logs` to identity (`bff.IdentityPath`). Newest first (ties by id). Filters: `actor_id`, `action`,
`resource_type`, `resource_id` (the entity) and `from` (inclusive) / `to` (exclusive), RFC 3339 or `YYYY-MM-DD` (a date
`to` covers that day). Paging: `limit` (default 50, max 100) and `offset`; `meta.has_more` says whether a next page
exists. Each row is `{id, actor_id, actor_email, action, resource_type, resource_id, meta, ip, created_at}`. `ip` is
returned exactly as stored: the writer stores the bare address (`admin.NormalizeIP`, migration 000016 cleaned old rows)
and nothing is rewritten on read. The company side of the trail, job and application events, is `GET /jobs/audit` in
hiring.

## Routing at the edge

`bff.IdentityPath`: `/auth/*`, `/organizations/invites`, `/admin/auth/*`, `/admin/{users,staff,roles,permissions,
audit-logs,verification}[/...]`. `internal/bff/identity_test.go` walks the real identity routes and asserts each one
matches, matches no applications path, and that `/admin/catalogs`, `/admin/jobs` stay on the monolith.

## Session validation at the BFF

`bff.RedisValidator` reuses `internal/session` (same Redis, namespaces `sess` / `admin_sess`, same cookie names). Default
deny: only `/auth/*` except `me` and `totp/*`, `/countries`, `/public/*`, the four public `/onboarding/*` catalogs and
`/admin/auth/{login,refresh,logout,accept-invite,invite/*}` are public. Everything else needs a full session of the
right kind (`/admin/*` the admin cookie), else 401; a Redis failure is 503. A valid session forwards
`X-Openhr-Subject` (user id), `X-Openhr-Region`, `X-Openhr-Actor` (`user`|`admin`), `X-Openhr-Roles`; inbound
`X-Openhr-*` are always stripped. The upstream still authenticates and checks CSRF itself, so a mistake here cannot grant
access. Validation slides the idle window like any request.

## gRPC (`GRPC_ADDR`, default :9090, no transport auth: keep it internal)

Implemented: `IdentityService.ValidateAccessToken` and `AdminIdentityService`/`IdentityService.ValidateAdminToken` (the
"token" is the session cookie value; the contract names are kept), `GetUser`, `BatchGetUsers` (max 500, one query per
region), `LookupRegion`; `AdminIdentityService.ListUsers`, `SetUserActive` (also revokes the user's sessions),
`InviteStaff` (role id or slug; acting admin from the call context), `ListAdminRoles`, `HasAdminPermission`,
`ListVerificationQueue`, `ReviewVerification`, `EnqueueVerification`.

`IdentityService.PrepareRegionMove`, `CommitRegionMove`, `AbortRegionMove` are identity's half of the region-migration
saga that org drives (see [org.md](org.md), `internal/regionsaga.IdentitySide`; callers allowed: `org`, `server`):
prepare copies the pending user into the target region, commit repoints `users_registry` and deletes the pending user
(the point of no return, idempotent, no tokens: sessions rotate), abort removes the copied user while the registry still
says `global`. `FailedPrecondition` means no retry helps (user not pending, not prepared, already committed);
`Unavailable` means retry. Org also gets `AdminOrgService.ReactivateCompany` through `POST /admin/companies/{id}/reactivate`.

Unimplemented (codes.Unimplemented with a reason): `CreateOrgInvite`, `AcceptOrgInvite`
(contract has no company context / session result; served over HTTP).

## Events

Written to the outbox of the database holding the change, same transaction, partition key = subject id, ids and region
only (never emails, names or codes). Off unless `IDENTITY_EVENTS=on`. Stream `IDENTITY`.

| Type | When | Data |
|---|---|---|
| `identity.user.registered.v1` | signup creates the pending user (global DB) | `user_id`, `region` (`global`), `registered_at` |
| `identity.user.email_verified.v1` | email verified (outbox of the user's database) | `user_id`, `region`, `verified_at` |
| `identity.user.deactivated.v1` | an operator disables the account (admin PATCH or `SetUserActive`) | `user_id`, `region`, `by_admin_id`, `deactivated_at` |
| `identity.staff.invited.v1` | a staff invite is created (not on resend; global DB) | `staff_id`, `invited_by`, `region` (`global`), `invited_at` |
| `identity.staff.deactivated.v1` | a staff member goes from active to inactive (`PATCH /admin/staff/{id}`; only that transition, same transaction, global DB) | `staff_id`, `region` (`global`), `deactivated_at` |

Consumer: with `IDENTITY_EVENTS=on` the service also subscribes to `identity.staff.deactivated.>` (inbox consumer
`identity-staff`) and revokes the member's admin sessions again; the PATCH revokes inline too, the event repeats it until
it succeeds.

Not emitted yet: user reactivation, user region moves (org emits `org.region.moved.v1` when the saga finishes).

## KYB review queue

`admin.verification_queue` belongs to this service (`internal/verifyqueue`; `kybstore.Queue` is an alias so org's enqueue
keeps compiling). `POST /admin/verification/{id}/review` and gRPC `ReviewVerification`: org applies the decision through
the `OrgReview` interface (`ORG_APPLY`), then identity closes the queue item. If closing fails after org applied the
decision, the item stays open and a retry gets 409 from org (same stuck state as before the split; no compensation yet).
With `ORG_APPLY=grpc` `GET /admin/verification/{id}` answers 503: the contract has no RPC for a case's submitted details.

## Interim pieces

- `OrgDB` writes org's regional tables and `internal/identityhttp/region_sql.go` and signup/login read `accounts.*`
  directly: replaced by org calls later. Company roles and permissions on `/auth/me` (`ORG_MEMBERSHIP=grpc`) and the
  onboarding progress and company name of `/auth/me` and the admin user detail (`ORG_ONBOARDING=grpc`) already go
  through org (`OrgService.ResolveMembership`, `GetOnboardingProgress`, `AdminOrgService.GetCompanyByOwner`; client in
  `internal/identityhttp/org_reads.go`, bounded by `ORG_GRPC_TIMEOUT`). With `db` (default) the old reads remain. In
  `grpc` mode there is no database fallback: `/auth/me` answers 503 `SERVICE_UNAVAILABLE` while org is unreachable, so a
  client never sees a signed-in user with silently empty roles. A user without a company, or an account still in the
  global holding region, gets empty roles as before. One difference: `GetCompanyByOwner` ignores deleted companies,
  the old query did not.
- `POST /organizations/invites` (create invite) lives in `auth_invite.go` next to acceptance and is served here, although
  BOUNDARIES lists org invites under org; the proto also places `CreateOrgInvite` in `IdentityService`.
- The region-move helpers stay in `internal/handler` (onboarding); `internal/handler/account_user.go` holds copies of the
  `accountUser` struct and small helpers both halves need.
- No `/readyz` check of Redis or NATS beyond the global database.

## Individual review cases

The KYB queue also carries the identity review of personal organizations (`case_type: "individual"` on `GET /admin/verification`, the
detail and `ListVerificationQueue`); the reviewer approves or rejects with the same `POST /admin/verification/{organizationID}/review`.
`EnqueueVerification` takes an optional `case_type` (`business` default). See [individual-accounts.md](individual-accounts.md).
