# End-to-end report

Date: 8 October 2026

The Docker stack was started from a clean volume (`docker compose up --build -d`) and exercised over HTTP against `http://localhost:8080`. Mail was read from Mailhog at `http://localhost:8025`. The stack is still running.

The failures below were fixed and rechecked on a new business account (`fix-biz-1791456848@example.com`). Profile, address verify, address save, compliance, and complete all succeeded, the account moved to `uk`, a new job returned `ETag: "1"`, and a preflight that asks for `If-Match` now allows it.

## Environment

| Service | Result |
| --- | --- |
| postgres, redis, minio, mailhog | Healthy |
| migrate | Exited 0. All five regional databases at goose version 10, including `000010_hiring_drafts.sql`. Global database at version 7, including `000007_market_warnings.sql`. |
| server | Healthy. `GET /api/v1/countries` is the healthcheck. |
| email-worker | Up. 11 messages landed in Mailhog. |
| fluvio-ui | Healthy on port 5173. |

`COMPANIES_HOUSE_API_KEY` is unset, so a UK company is queued for a person to review (tier 2). That matches the server startup comment.

## Result

The live product path works for auth, individual onboarding, the one-shot business submit, hiring drafts, company verification, the admin console, and email.

The stepped business onboarding wizard does not. `PUT /api/v1/onboarding/profile` with `account_type: business` returns **500**, so that account never reaches a region and cannot open hiring or verification. Hiring and verification were then run on a company created through `POST /api/v1/onboarding/business`, and those modules passed.

## Stepped business onboarding

Account `e2e-biz-1791456107@example.com` signed up, verified, and stayed in region `global`, which is correct until a country is known.

| Step | HTTP | What came back |
| --- | --- | --- |
| `GET /onboarding/status` | 200 | `in_progress`, next step `profile` |
| `PUT /onboarding/profile` (business) | **500** | `INTERNAL_ERROR`. Request `de838b361c3c/DmJ5QwfIB0-000077` |
| `POST /onboarding/address/search` | 200 | Empty suggestion list. The server uses the passthrough searcher. |
| `POST /onboarding/address/verify` | **500** | `INTERNAL_ERROR`. Request `de838b361c3c/DmJ5QwfIB0-000079` |
| `PUT /onboarding/address` | **500** | `INTERNAL_ERROR`. Request `de838b361c3c/DmJ5QwfIB0-000080` |
| `PUT /onboarding/compliance` | 400 | `complete the profile step before compliance` |
| `POST /onboarding/complete` | 400 | `onboarding_step_incomplete` |
| `GET /auth/me` after those calls | 200 | `region` is still `global` |

`accounts.onboarding_progress` does not exist in `openhr_global`. The holding table is `accounts.pending_onboarding_progress`. `advanceProgress` in `internal/handler/product_onboarding_handlers.go` always runs the regional progress queries, so the business profile write fails while the account is still in the holding database. The pending-aware helpers in `internal/handler/region_pending.go` are not used on that path.

Address verify fails on its own for the same kind of account. `verifyAddress` calls `Router.DB(reg)`, and `global` is not one of the regional pools, so the call is an unknown region and becomes a 500. Address search does not touch that pool, which is why it returned 200.

## What passed

### Platform and reference data

- `GET /openapi.json` and the Scalar docs at `/` return 200.
- Fluvio UI at `http://localhost:5173/` returns 200.
- `GET /api/v1/countries` includes United Kingdom (`a1b2c3d4-e5f6-4789-a012-3456789abcde`, region `uk`).
- Public onboarding catalogs return 200: company roles, business types, industries, and identification requirements for the UK (`registration_number`). A missing `country_id` returns 400.
- CORS allows `http://localhost:3000` and `https://app.tryopenhr.com`. A preflight that asks to send `If-Match` is rejected: allowed headers are only `Authorization` and `Content-Type`. A browser saving a job with `If-Match` will fail the preflight. The same preflight without `If-Match` succeeds.

`CatalogHandler` (`/industries`, `/hiring-tools`, `/hiring-frustrations`, `/roles`, `/team-sizes`, `/business-types`) is not mounted in `internal/app/app.go`. Those paths return 404. The same rows are available through the admin catalog API.

### Waitlist

- `POST /api/v1/waitlist` returns 201 with `{"message":"You're on the list!","region":"uk"}`. The id is not in that body.
- The same email returns 409. A bad email returns 400.
- Mailhog received “You're on the OpenHR waitlist”.
- Admin `GET /admin/waitlist`, `GET /admin/waitlist/{uuid}`, and `PATCH` with `wants_early_access: true` all return 200.

### Auth

Exercised on several fresh accounts:

- Signup, the 6-digit code in Redis (`verify:{session}`), verify-email, `/auth/me`, password login, refresh.
- Logout revokes the refresh token. The next refresh returns 401.
- Passwordless login: request, Redis code, verify.
- TOTP: enroll, confirm, password login returns `totp_required` plus `mfa_token`, `POST /auth/login/totp`, then disable.
- Forgot-password always returns 200. The reset link arrived in Mailhog, reset succeeded, and login worked with the new password.

### Individual onboarding

`PUT /onboarding/profile` with a UK country moved the account from `global` to `uk` and returned new tokens. Compliance and complete returned the expected success codes. After that, `GET /jobs` and `GET /organization/verification` return 404 `no organization for this account`.

`POST /onboarding/individual` on a second account returned 201 and new tokens.

### One-shot business onboarding

`POST /onboarding/business` for One Shot Ltd returned 201, placed the owner in `uk`, and `GET /hiring/catalog` returned 200. This is the company used for hiring, verification, and invites below.

### Hiring

Against One Shot Ltd, as the owner:

- Catalog lists the UK market as open and Nigeria as closed. Time zones returned 313 entries. Skills search returned a list (the catalog is empty, so the list is empty).
- Department “Engineering” was created. A blank name returns `VALIDATION_ERROR`.
- Settings start with `automated_screening_enabled: false`. The owner’s `PUT /hiring/settings` returns 403 `FORBIDDEN`.
- Create draft `Cook` returned 201, job code `JB-1`, status `draft`. The `ETag` on create is `"2"`, because the insert starts at revision 1 and the same transaction’s update adds one. Patching with that `ETag` renamed the job to Chef and returned `"3"`. Sending `If-Match: "1"` returned 409 `CONFLICT`.
- An unknown field, a non-UUID id, an incomplete details body (6 missing fields), a closed market (`NG`), a bad list status, a bad template kind, and a “current salary” question were all rejected as validation or not-found, matching the case.
- Save-and-continue with title, department, UK, travel, visa, full-time, and a GBP range moved `current_step` to `application_form`. Markets, the application form, and preview succeeded. Preview shows `One Shot Ltd` and does not include knockout rules.
- A job-details template was saved, a case-insensitive duplicate name was rejected, the list returned it, and delete returned 200.
- A second job with the same title and UK market came back with a duplicate of `JB-1`. Dismiss, list (2 drafts), and delete succeeded. Delete is a soft delete: `GET` returns 404 and `deleted_at` is set.

An invited member receives 403 on list and create. There is no HTTP API that assigns the `legal` role. After inserting `legal` into `accounts.organization_member_roles` for that member, `PUT /hiring/settings` returned 200 and the owner’s subsequent `GET` showed screening on.

### Company verification

- Before submit, status is `not_started`.
- `POST /organization/verification` for GB returned `pending`, display `pending_review`, tier 2, `can_publish: false`.
- Retry while that review is open returned 409 `CONFLICT`.
- Mailhog received “Your company verification is in review”.
- The bootstrap admin saw the company on `GET /admin/verification`, opened the detail, and `POST .../review` with `{"decision":"approve"}` returned `verified`.
- The owner’s status is now `verified` with `can_publish: true`.

### Admin

Bootstrap login `admin@anglehr.local` / `changeme123` works, as do `/admin/auth/me` and refresh.

These returned 200: waitlist, users, industries catalog, Fluvio jobs, staff, roles, permissions, audit logs, verification queue.

Also exercised:

- Create an industry and patch it inactive.
- Unknown catalog type returns 404.
- Create a role with `verification:review` and `audit:read`.
- Invite that role to a new staff email. The admin invite arrived, preview and accept worked, the new staff member can read audit logs, and `GET /admin/users` returns 403.

### Email

Subjects received in Mailhog:

- Verify your Open HR email (4)
- You're on the OpenHR waitlist
- Welcome to Open HR
- Your Open HR sign-in code
- Reset your Open HR password
- You're invited to Open HR Admin
- You're invited to Open HR
- Your company verification is in review

The organization invite link is `http://localhost:8080/invite/join?token=...`. Preview and accept with that token return 200. The email body says the invite expires in `259200` seconds (the raw TTL) rather than a duration a person would read.

## Follow-ups

1. Business profile progress must be written to `accounts.pending_onboarding_progress` while the account is still in `global`.
2. Address verify must use the global connection for a `global` account, the same way the rest of onboarding does through `resolvePool`.
3. Add `If-Match` to the CORS allowed headers, or job patches from the browser will not pass the preflight.
4. The first `ETag` a client sees on create is `"2"`. Anything that assumes `"1"` will get a 409.
