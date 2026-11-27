# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added
- Interviews and compare (ATS plan, phase 4). Under `/jobs/{id}/applications/{app_id}`: `GET`/`POST /interviews`, `PATCH /interviews/{interview_id}`, `POST /interviews/{interview_id}/cancel` and `/complete`; `GET /jobs/{id}/applications/compare?ids=a,b` (2 to 4 applications side by side: answers by question, files, tags, interviews; no special category answers, review flags or status notes); `GET /hiring/interviews/mine` (a person's schedule). Scheduling, rescheduling or cancelling can email the candidate a calendar event (`invite` / `notify`); the mailer sends multipart email with a `text/calendar` part when a candidate message carries an event (new `ical` package). Migration `000028`. See `docs/ATS_BACKEND_PLAN.md` and `scripts/e2e-interviews.sh`.
- Candidate email (ATS plan, phase 3). Under `/hiring`: `GET`/`POST /email-templates`, `PUT`/`DELETE /email-templates/{template_id}` (`job.manage_any` writes; plain text with `{{candidate_name}}`, `{{job_title}}`, `{{company}}`, `{{sender_name}}`) and `GET`/`PUT /email-settings` (reply-to address). Under `/jobs/{id}/applications/{app_id}`: `GET`/`POST /emails` (send now or at `schedule_at`; no recipient field, it always goes to the application's candidate; 10 per candidate and 500 per company in 24 hours), `POST /emails/preview` and `DELETE /emails/{message_id}` (cancel a scheduled one). A `message` object on the status, review, stage-move and bulk stage-move requests emails the candidate with a hire or a rejection instead of the standard email. Migration `000027`; `hiring-outbox` also sends due messages; mailer type `candidate_message` with a `Reply-To` header. Attachments and Brevo delivery status are still to do. See `docs/ATS_BACKEND_PLAN.md` and `scripts/e2e-email.sh`.
- Application pipeline (ATS plan, phase 2). Every job has stages (Applied, Shortlisted, Interview, Offer, Hired, Disqualified, plus up to 14 custom ones; migration `000026` backfills existing applications). Endpoints under `/jobs/{id}`: `GET`/`POST /stages`, `PATCH`/`DELETE /stages/{stage_id}`, `PUT /stages/order`, `POST /applications/{app_id}/stage`, `POST /applications/bulk/stage`, notes (`GET`/`POST /applications/{app_id}/notes`, `PATCH`/`DELETE .../notes/{note_id}`), `PUT /applications/{app_id}/tags`, `PUT`/`DELETE /applications/{app_id}/follow` and `GET /applications/{app_id}/activity`; under `/hiring`: `GET`/`POST /tags`, `DELETE /tags/{tag_id}` and `GET /disqualification-reasons`. Disqualifying takes a reason (also optional on the status and review endpoints). The application list gains `stage_counts`, the `stage`, `tag` and `followed` filters and per-row stage, follow, note count and tags. New event `applications.application.stage_changed.v1`. The web BFF routes the new paths to the applications service. See `docs/ATS_BACKEND_PLAN.md` and `scripts/e2e-pipeline.sh`.
- Public careers page API. Org: `GET`/`PUT /organization/profile` (careers page address, display name, about, website, logo; Founder and HR 1 write through the new `org.profile.edit` permission) and `GET /organization/profile/slug-check`. The address (slug) is chosen by the owner, validated and kept unique across regions in the global `org.company_slugs` table (migrations `000025`, `global_registry/000021` and `000022`). Hiring: `GET /public/companies/{slug}` returns the profile and the company's open jobs (published, shown on the career page, inside the closing date) with team, workplace, employment type, location and title filters and cursor paging. `OrgService.Company` carries the profile fields. The web BFF routes both. See `docs/ATS_BACKEND_PLAN.md` and `scripts/e2e-careers.sh`.
- Region-migration saga (`REGION_MOVE=saga`, default `inline`): org drives the move of an onboarding account out of the global holding area through `IdentityService.PrepareRegionMove` / `CommitRegionMove` / new `AbortRegionMove` over gRPC, with persisted state (`accounts.region_move_sagas`, migration `global_registry/000018`, which also drops the cross-service foreign keys from the pending organization and progress tables), idempotent steps, compensation before the commit and a resumer. `OrgService.CompleteRegionOnboarding` is implemented (no tokens: sessions rotate). New events `org.region.moved.v1`, `org.company.reactivated.v1` (`AdminOrgService.ReactivateCompany`, `POST /admin/companies/{id}/reactivate`) and `identity.staff.deactivated.v1`; consumers behind `APPLICATIONS_ORG_EVENTS`, `HIRING_ORG_EVENTS` and `IDENTITY_EVENTS`. See `docs/microservices/org.md` and `scripts/e2e-region-saga.sh`.
- Individual accounts can hire: finishing individual onboarding creates a personal organization (`accounts.organizations.kind = 'individual'`), reviewed through the admin verification queue (`case_type: individual`). The compliance step asks individuals for the industry only; `POST /onboarding/complete` returns `organization`; `GET /organization/verification` returns `kind`. `cmd/org-backfill-individuals` migrates existing accounts. `org.*` events carry `company_kind`. See `docs/microservices/individual-accounts.md`.

### Changed (small fixes)
- `waitlist:read` and `waitlist:write` are deleted from `admin.permissions` and every role (migration `global_registry/000015`).
- Admin audit rows store the bare client IP; older `ip:port` rows are rewritten (migration `global_registry/000016`, IPv6 aware).
- `APP_URL` and `ADMIN_APP_URL` are the only source of email link origins; `cmd/notifications`, `cmd/email-worker` and `cmd/hiring-outbox` refuse to start without them (https) in staging and production.

### Removed (BREAKING: waitlist)
- The waitlist feature is removed: `POST /api/v1/waitlist`, the `/api/v1/admin/waitlist*` routes, the `waitlist:read` / `waitlist:write` permission constants, the waitlist confirmation email and its query builders. The `waitlist` database schema stays because the onboarding catalogs live there, and no waitlist data is dropped.

### Changed
- Email requests can leave the monolith as events (`NOTIFICATIONS_TRANSPORT=events`), consumed by the new `cmd/notifications` service. The default is still the Fluvio queue. See `docs/microservices/notifications.md`.
- The Go toolchain is 1.26 (Dockerfiles use `golang:1.26-alpine`).

### Changed (BREAKING: authentication)
- JWT bearer authentication is gone for product and admin APIs. Authentication is now an opaque server-side session in Redis carried by an HttpOnly cookie (`__Host-openhr_session`; `openhr_session` when `COOKIE_SECURE=false`). Admin uses a separate cookie (`__Host-openhr_admin_session`) and Redis namespace. See `docs/microservices/sessions.md`.
- `login`, `verify-email`, `login/otp/verify`, `login/totp`, `accept-invite` and `/admin/auth/login|accept-invite` return `{data:{onboarding?, user?, csrf_token}}` plus `Set-Cookie`. No `access_token`/`refresh_token`/`mfa_token`/`expires_in` in bodies. `GET /auth/me` (and `/admin/auth/me`) also returns `csrf_token`.
- Every POST/PUT/PATCH/DELETE on an authenticated route requires `X-CSRF-Token` (403 `CSRF_INVALID`); unsafe requests whose `Origin`/`Referer` is not in `ALLOWED_ORIGINS` get 403. CORS now allows credentials.
- TOTP login: password/OTP login for a TOTP user sets a single-use 5 minute pre-auth cookie (`openhr_preauth`) instead of returning `mfa_token`; `POST /auth/login/totp` takes only `{code}`.
- Onboarding steps that migrate an account between regions now rotate the session; the `tokens` response field is replaced by `session: {csrf_token, region}`.
- Session ids rotate on login, MFA completion and region change. Password reset, TOTP disable and admin staff deactivation revoke all of the user's sessions.
- `POST /auth/refresh` and `/admin/auth/refresh` return 410 `SESSION_REFRESH_REMOVED` (removed next release). `POST /admin/auth/logout` added.
- Config: removed `JWT_SECRET`, `JWT_ACCESS_TTL`, `JWT_REFRESH_TTL`; added `SESSION_IDLE_TTL` (7200), `SESSION_ABSOLUTE_TTL` (43200), `COOKIE_SECURE` (true), `ALLOWED_ORIGINS` (defaults to `CORS_ALLOWED_ORIGINS`). `JWT_SECRET` is still read as a legacy fallback for `TOTP_ENCRYPTION_KEY` only.
- New `internal/session` package with a `session.Validator` interface so a BFF can reuse the same store.

### Added
- Product auth session APIs: `GET /auth/me`, `POST /auth/logout` (refresh `jti` denylist + per-user session revoke in Redis).
- Password reset: `POST /auth/forgot-password`, `POST /auth/reset-password` with email template.
- Passwordless login OTP: `POST /auth/login/otp/request`, `POST /auth/login/otp/verify` (separate from signup verification).
- TOTP MFA: enroll/confirm/disable plus `POST /auth/login/totp` MFA challenge after password or OTP login.
- Product organization invites: `GET /auth/invite/{token}`, `POST /auth/accept-invite`, `POST /organizations/invites` with membership tables (`000002_auth_features`).
- Non-prod passthrough address verifier via `ADDRESS_VERIFY_MODE=passthrough`.
- Product onboarding API: auth (`/auth/signup`, verify, login, refresh), onboarding steps (`/onboarding/profile`, `/address`, `/business`, `/status`, `/complete`), and product reference catalog endpoints.
- Accounts schema (`000002_accounts`) in regional databases and onboarding catalog migration (`000002_onboarding_catalogs`) in the global registry.
- JWT auth middleware, Redis-backed email verification (6-digit OTP), and Fluvio email jobs (`email_verification`, `onboarding_complete`).
- Email templates for verification and onboarding completion.
- E2E script: `scripts/e2e-onboarding.sh`.

### Changed
- Auth audit fixes (Phase 0): OTP attempts are counted with an atomic Redis `INCR` (5-attempt cap, constant-time code compare, single-use consume) and login-OTP verification is locked per email; `PATCH /auth/signup` shares the resend cooldowns (429 `retry_after_seconds`) and checks the verified-user registry; `POST /auth/forgot-password` has a per-email cooldown with the same generic 200; new passwords (signup, reset, product and admin accept-invite) must be 8-128 chars with a letter and a digit (`VALIDATION_ERROR`, `details.fields[].path = "password"`); login failures for unknown emails now count toward lockout and spend a dummy bcrypt compare; `POST /auth/login/otp/request` always returns 200 (decoy session for unknown or unverified emails); signing up again with an unverified pending email replaces its password and starts a fresh verification (201) instead of 409.
- Staging test findings (employer flow): country codes are one format everywhere. `internal/countrycode` accepts ISO 3166-1 alpha-2 and the `UK` alias (stored as `GB`) on apply, KYB submit and KYB change-country, and refuses any unassigned code with `country_code` named in `details.fields`.
- Applying with an `Idempotency-Key` is answered before validation: an identical retry returns the original success even when its upload token is spent, and the same key with a different body returns 422 `IDEMPOTENCY_KEY_REUSED`. A SHA-256 request fingerprint is stored with the application (`000017_hiring_idempotency_fingerprint`).
- Apply and upload rate limits: `429` now carries a real `Retry-After` (time left in the window) and the code `TOO_MANY_REQUESTS`. Invalid requests count against a 15 minute flood limit only, never against the hour's quota for valid applications.
- `If-Match` is required on every job write (publish, lifecycle actions, rules, members, gates, details, markets, form, delete). A missing header returns 428 `PRECONDITION_REQUIRED`.
- Validation errors share one shape, `details.fields[].path`: waitlist/auth/onboarding validators (was `field`), malformed or unknown-field request bodies (was a bare "invalid request body"), and KYB input.
- Object storage now uses Cloudflare R2 instead of self-hosted MinIO. Environment variables are renamed from `ANGLEHR_*_MINIO_*` to `ANGLEHR_*_R2_*`. `DBRouter.MinIO()` is now `DBRouter.R2()`. R2 credentials are shared via `R2_ACCESS_KEY` and `R2_SECRET_KEY`; `R2_ENDPOINT` is the default S3 API host with optional per-region `ANGLEHR_*_R2_ENDPOINT` overrides.
- Email verification OTP expiry increased from 60 seconds to 5 minutes (300 seconds).
- Removed redundant Markdown API docs under `docs/`; API reference is OpenAPI only (Scalar at `/`).
- Canonical onboarding docs: stepped PUT flow; OpenAPI marks POST `/onboarding/individual` and POST `/onboarding/business` as deprecated.

### Deprecated
- `POST /onboarding/individual` and `POST /onboarding/business` one-shot endpoints (prefer stepped PUT flow).

### Removed
- In-cluster MinIO StatefulSets, `minio_setup` docker-compose service, and `minio-setup` Kubernetes Job. Startup no longer checks or creates R2 buckets.

### Fixed
### Security

---

## [0.1.0] - YYYY-MM-DD

### Added
- Initial project scaffold.
- Core feature: [describe].
- `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, and community health files.

---

<!-- CHANGELOG GUIDE

Each release entry should follow this structure:

## [X.Y.Z] - YYYY-MM-DD

### Added
- New features or capabilities introduced in this release.

### Changed
- Changes to existing functionality that are backwards-compatible.

### Deprecated
- Features that still work but are planned for removal in a future release.

### Removed
- Features or functionality removed in this release.

### Fixed
- Bug fixes. Reference issue numbers where applicable, e.g. "Fixed crash on empty input (#42)."

### Security
- Patches for vulnerabilities. Reference CVE IDs where applicable.

VERSIONING RULES (Semantic Versioning):
  MAJOR (X) — breaking changes incompatible with previous versions.
  MINOR (Y) — new backwards-compatible functionality.
  PATCH (Z) — backwards-compatible bug fixes.

TIPS:
  - Write entries for humans, not machines. "Fixed token expiry crash" > "Patched null pointer in auth.go:147."
  - Link issue/PR numbers: "Added batch export support (#88, #91)."
  - Keep Unreleased updated as you merge PRs; cut a release by moving it to a dated version.

-->

[Unreleased]: https://github.com/Angle-HR/server/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Angle-HR/server/releases/tag/v0.1.0
