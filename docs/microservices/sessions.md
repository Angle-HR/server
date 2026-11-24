# Sessions

OpenHR authenticates browsers with opaque, server-side sessions in Redis. JWT bearer tokens (access, refresh, MFA) no longer exist anywhere in the product or admin APIs. Code: `internal/session` (store, cookies, CSRF, Origin guard), `internal/auth` (middleware and context accessors).

## Model

- Session id: 256 bits from `crypto/rand`, base64url. Only its SHA-256 is stored (as the Redis key), so a Redis dump yields no usable cookie.
- Value (JSON): `user_id`, `region`, `stage` (`full` or `mfa`), `mfa` (verified yes/no), `created_at`, `last_seen`, `ua_hash` (stored, not enforced), `csrf` (the synchronizer secret).
- Index: `<ns>:u:<user_id>` is a set of the user's session hashes, used to revoke all sessions at once.
- Namespaces: `sess` (product), `sess_pre` (product MFA pre-auth), `admin_sess`, `admin_sess_pre`. Product and admin never share keys or cookie names; a product session replayed under the admin cookie name is rejected.
- Timeouts: sliding idle (`SESSION_IDLE_TTL`, default 2h) and absolute (`SESSION_ABSOLUTE_TTL`, default 12h). The Redis TTL is `min(idle, time left to absolute)`. The sliding update uses `SET ... XX` so it can never resurrect a session revoked meanwhile. Rotation keeps `created_at`, so rotating does not extend the absolute limit.
- Fail closed: any Redis error on validation is a 500, never an authenticated request.

## Cookies

| Purpose | Secure mode (`COOKIE_SECURE=true`, default) | Local http (`COOKIE_SECURE=false`) |
|---|---|---|
| Product session | `__Host-openhr_session` | `openhr_session` |
| Product MFA pre-auth | `__Host-openhr_preauth` | `openhr_preauth` |
| Admin session | `__Host-openhr_admin_session` | `openhr_admin_session` |
| Admin pre-auth | `__Host-openhr_admin_preauth` | `openhr_admin_preauth` |

Attributes: `HttpOnly`, `SameSite=Lax`, `Path=/`, no `Domain`, `Secure` in secure mode. The session cookie is persistent for the absolute lifetime (the server enforces the real limits); pre-auth lasts 5 minutes.

## CSRF and Origin

- Synchronizer token bound to the session. It is returned as `csrf_token` in the JSON body of login, verify-email, login/otp/verify, login/totp, accept-invite and `GET /auth/me` (and the admin equivalents). There is no CSRF cookie. Clients call `/auth/me` on page load to obtain it.
- Every POST/PUT/PATCH/DELETE on an authenticated route must send `X-CSRF-Token`; compared in constant time; mismatch or absence is 403 `CSRF_INVALID`. GET/HEAD/OPTIONS are exempt.
- Public unauthenticated endpoints (login, signup, otp, forgot/reset, public apply, logout) need no token.
- `ALLOWED_ORIGINS` (defaults to `CORS_ALLOWED_ORIGINS`): on unsafe methods under `/api/v1`, if the request has an `Origin` (or else `Referer`) it must be on the list or equal the API's own host, else 403 `FORBIDDEN`. A request with neither header (non-browser client) passes this check but still needs the CSRF token on authenticated routes. CORS responds with `Access-Control-Allow-Credentials: true` for allow-listed origins; browsers must use `credentials: "include"`.

## Flows

- Login (password or OTP), verify-email, accept-invite: any session id presented with the request is revoked, a new session is created, cookie set, body returns `{data:{onboarding, user, csrf_token}}`.
- TOTP users: password/OTP success sets the pre-auth cookie and returns `{data:{totp_required:true, expires_in:300}}`. `POST /auth/login/totp` with `{code}` requires that cookie. A wrong code leaves it alive (the existing TOTP lockout bounds guesses); a right code spends it atomically (`GETDEL`, single use) and issues the full session (`mfa=true`) under a fresh id. A pre-auth value never authenticates as a session.
- Admin TOTP: for an admin with TOTP enabled `POST /admin/auth/login` sets the admin pre-auth cookie (namespace `admin_sess_pre`, 5 minutes, single use) and returns `{totp_required:true, expires_in:300}` without a session; `POST /admin/auth/login/totp` with `{code}` spends it and starts the admin session with `mfa=true`. A session started by password only has `mfa=false`; with `ADMIN_TOTP_REQUIRED=on` such a session can only reach the enrolment routes (403 `TOTP_ENROLLMENT_REQUIRED`). Confirming enrolment, self-disable and a superadmin reset revoke the admin's sessions. See identity.md.
- Region migration: onboarding steps that move an account from the `global` holding region rotate the session (new id, new CSRF token, region updated). The response carries `session: {csrf_token, region}`.
- Logout: `POST /auth/logout` (and `/admin/auth/logout`) revokes the session server-side and clears the cookies. Idempotent; works without a session.
- Revoke all sessions of a user: password reset, TOTP disable (the caller is signed out too), admin staff deactivation.
- Refresh: removed. `POST /auth/refresh` and `/admin/auth/refresh` return 410 `SESSION_REFRESH_REMOVED` for one release; the sliding idle timeout replaces them.
- Missing/expired/revoked session: 401 `UNAUTHORIZED`.

## Contract changes (clients)

| Before | Now |
|---|---|
| `Authorization: Bearer <access_token>` | cookie, `credentials: "include"` |
| `access_token`, `refresh_token`, `expires_in` in bodies | none; `csrf_token` in body |
| `mfa_token` in login response and `/login/totp` body | pre-auth cookie; `/login/totp` body is `{code}` |
| `POST /auth/refresh` | removed (410) |
| onboarding `tokens` field after region change | `session: {csrf_token, region}`, new cookie |
| unsafe requests | must send `X-CSRF-Token` |

## Configuration

`SESSION_IDLE_TTL` (s, 7200), `SESSION_ABSOLUTE_TTL` (s, 43200, must be >= idle), `COOKIE_SECURE` (true), `ALLOWED_ORIGINS`. Removed: `JWT_SECRET`, `JWT_ACCESS_TTL`, `JWT_REFRESH_TTL` (`JWT_SECRET` is still read as a legacy fallback for `TOTP_ENCRYPTION_KEY` so existing TOTP enrolments keep decrypting).

## Reuse by a BFF

```go
type Validator interface {
    Validate(ctx context.Context, cookieValue string) (Identity, error)
}
```

`session.Manager.Validator()` (backed by `*session.Store`) returns it. `Identity` has `UserID`, `Region`, `Stage`, `MFA`, `CSRFToken`, `CreatedAt`, `LastSeen`, `Key`. A BFF that points at the same Redis and the same namespace (`sess` / `admin_sess`) and cookie name can own the browser session, check CSRF with `session.CheckCSRF`, and forward `UserID`/`Region` to services as gRPC metadata. Services behind it then stop reading cookies at all; `auth.UserFromContext` is the single accessor the handlers use, so moving the validation boundary does not touch handler code.

## Operations notes

- The public hiring apply endpoints live under `/api/v1` and are subject to the Origin check when a browser sends an Origin. Embedding the apply form on a third-party origin requires adding that origin to `ALLOWED_ORIGINS`.
- Redis is now the source of truth for who is logged in. Losing it logs everyone out; it does not grant access.
- Not done: per-user session count cap, session listing UI, binding to user-agent/IP (the UA hash is stored for future use).
