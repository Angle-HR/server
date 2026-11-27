# web-bff (Phase 1)

Edge service in front of the monolith API. Code: `cmd/web-bff` (entrypoint) + `internal/bff` (shared by a
`admin-bff` (`cmd/admin-bff` + `internal/adminbff`, see the last section) reuses it for the admin console.

```
browser -> web-bff :8090 -> monolith :8080 (/api/v1/*)
             |-> Redis (rate limits)
```

## What it does

Request pipeline: request id -> metrics -> JSON access log (`pkg/logger`) -> security headers -> CORS / Origin
check -> per-IP global rate limit -> per-class rate limits -> `SessionValidator` -> body limit -> reverse proxy.
`/healthz` (liveness) and `/readyz` (Redis ping + upstream `GET /healthz`) bypass everything after the headers.

- **Proxy**: paths preserved, bodies/responses streamed (`FlushInterval -1`, so SSE works; WebSocket upgrades
  are handled by `httputil`; the monolith currently uses neither). Hop-by-hop headers stripped by the stdlib.
  `Cookie`, `Set-Cookie`, `Authorization`, `X-CSRF-Token` pass untouched. `X-Forwarded-For/Host/Proto` rebuilt;
  inbound values are trusted only from `TRUSTED_PROXY_CIDRS`. Upstream `Access-Control-*` and security headers
  are dropped so the BFF's are the only ones. Upstream failure -> 502/504 error envelope.
- **Request id**: inbound `X-Request-Id` kept if `[A-Za-z0-9._-]{1,128}`, else generated; sent upstream and
  returned; appears in error `meta.request_id`.
- **Rate limits** (Redis, atomic Lua sliding-window log, time from Redis `TIME`): global per IP, plus a class
  per abuse path (prefix `/api/v1` stripped): `LOGIN`, `LOGIN_OTP_REQUEST`, `LOGIN_OTP_VERIFY`, `LOGIN_TOTP`,
  `SIGNUP`, `VERIFY_EMAIL`, `RESEND_VERIFICATION`, `FORGOT_PASSWORD`, `RESET_PASSWORD` (POST) and
  `PUBLIC_APPLY` (`POST /public/jobs/{id}/applications|uploads`), `PUBLIC_OFFER` (`/public/offers/{token}[/sign|decline|document]`, 30 per minute per IP). Classes with an email rule also key on the
  SHA-256 of the lower-cased, trimmed JSON `email` (body peeked up to 64 KiB and restored). Denied: `429`,
  `Retry-After`, `{"error":{"code":"TOO_MANY_REQUESTS",...},"meta":{"request_id"}}`.
- **Client IP**: socket peer, unless the peer is in `TRUSTED_PROXY_CIDRS`; then `X-Forwarded-For` is walked right
  to left and the first untrusted hop wins (left-side entries are attacker controlled). Behind a load balancer
  you MUST set `TRUSTED_PROXY_CIDRS` or all users share one bucket.
- **Redis outage**: `RATE_LIMIT_FAIL_CLOSED_AUTH=true` (default) answers 503 on the auth/apply classes;
  `RATE_LIMIT_FAIL_CLOSED_OTHER=false` (default) lets the global limit fail open. Both are switches.
- **CORS/Origin**: `ALLOWED_ORIGINS` allow-list with credentials (`*` is ignored). Unsafe methods carrying a
  non-allow-listed `Origin` get 403; no `Origin` passes unless `ORIGIN_REQUIRED=true`. Preflights answered at the
  edge. Headers: nosniff, `X-Frame-Options: DENY`, CSP `frame-ancestors 'none'`, Referrer-Policy, Permissions-Policy,
  HSTS when the request was TLS / trusted proxy says `X-Forwarded-Proto: https` / `HSTS_ENABLED=true`.
- **Metrics**: Prometheus text at `/metrics` on the separate `OPS_LISTEN_ADDR` (disabled when empty; never expose
  publicly). Counters `bff_http_requests_total`, `bff_ratelimit_rejected_total`, `bff_ratelimit_errors_total`,
  `bff_upstream_errors_total`. No exporter SDKs; `Metrics` is a seam for OpenTelemetry later.
- **SessionValidator** (`session.go`): `Validate(r) (*Identity, error)`; `NoopValidator` is wired today.
  `ErrUnauthenticated` -> 401, other errors -> 503. Inbound `X-Openhr-*` headers are always stripped; a returned
  identity is forwarded as `X-Openhr-Subject/Tenant/Roles` (to become gRPC metadata later).

## Configuration (env, each also as `NAME_FILE`)

| Var | Default |
|---|---|
| `UPSTREAM_API_URL` | required |
| `REDIS_URL` | required (`redis://host:6379/0`) |
| `LISTEN_ADDR` / `OPS_LISTEN_ADDR` | `:8090` / disabled |
| `API_PREFIX` | `/api/v1` |
| `ALLOWED_ORIGINS` | none (comma list) |
| `TRUSTED_PROXY_CIDRS` | none (comma list of CIDRs/IPs) |
| `MAX_BODY_BYTES` | 10485760 |
| `UPSTREAM_TIMEOUT` (response headers) | 30s |
| `READ_HEADER_TIMEOUT` / `IDLE_TIMEOUT` / `SHUTDOWN_TIMEOUT` | 10s / 90s / 20s |
| `RATE_LIMIT_GLOBAL` | `300/1m` per IP |
| `RATE_LIMIT_<CLASS>` / `RATE_LIMIT_<CLASS>_EMAIL` | see `DefaultClasses()` (e.g. `LOGIN` 20/5m per IP, 10/15m per email); `0/1m` disables |
| `RATE_LIMIT_FAIL_CLOSED_AUTH` / `_OTHER` | `true` / `false` |
| `ORIGIN_REQUIRED`, `HSTS_ENABLED` | `false` |
| `APP_ENV`, `LOG_LEVEL`, `SERVICE_NAME` | `development`, per env, `web-bff` |

Startup warnings: outside development the BFF logs a warning for each of `TRUSTED_PROXY_CIDRS` unset,
`ALLOWED_ORIGINS` unset and `ORIGIN_REQUIRED=false` (`Config.Warnings`). They do not stop the service. Guidance for
all three is in `hardening.md`.

Note: at `LOG_LEVEL=debug` (default in `development`) `pkg/logger` logs redacted request/response bodies, except for
auth and onboarding paths, whose bodies are never logged (see `hardening.md`).

## Build and compose (for the local-stack agent)

Generic Dockerfile: `docker build -f Dockerfile.service --build-arg SERVICE=web-bff -t openhr/web-bff .`
(builds `./cmd/web-bff`).

```yaml
  web-bff:
    build: { context: ., dockerfile: Dockerfile.service, args: { SERVICE: web-bff } }
    environment:
      UPSTREAM_API_URL: http://api:8080
      REDIS_URL: redis://redis:6379/0
      LISTEN_ADDR: ":8090"
      OPS_LISTEN_ADDR: ":9091"
      ALLOWED_ORIGINS: http://localhost:3000
      TRUSTED_PROXY_CIDRS: 172.16.0.0/12   # docker bridge, only if a proxy sits in front
      APP_ENV: development
    ports: ["8090:8090"]
    depends_on: [api, redis]
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8090/healthz"]
      interval: 10s
```

Point the client at `http://localhost:8090` instead of the API.

## Not done / next

No Redis-backed session validation yet; no TLS termination (expects an LB); CSP is API-only (the BFF does not
serve HTML); no distributed tracing; upstream retries/circuit breaking; per-tenant limits.

## admin-bff

`cmd/admin-bff` runs `internal/adminbff`: the same edge as above, in front of the monolith's `/api/v1/admin` space.

```
admin app -> admin-bff :8080 -> monolith :8080 (/api/v1/admin/*)
```

- **Path allow-list**: only `/api/v1/admin/*` and `/healthz`, `/readyz` are served; anything else (the product API)
  is a 404 at the edge. `UPSTREAM_APPLICATIONS_URL` is ignored.
- **Cookie**: `__Host-openhr_admin_session`, or `openhr_admin_session` when `COOKIE_SECURE=false` (must match the
  monolith); `SESSION_COOKIE_NAME` overrides. Protected admin routes without that cookie get a 401 at the edge
  (stateless presence check, the monolith remains the authority). Public: `POST auth/login`, `auth/login/totp`,
  `auth/accept-invite`, `auth/logout`, `auth/refresh`, `GET auth/invite/{token}`.
- **Origin/CSRF**: `ALLOWED_ORIGINS` is the admin app origin(s) (compose: `ADMIN_BFF_ALLOWED_ORIGINS`, default
  `http://localhost:3001,http://localhost:5174`). Unsafe requests with another `Origin` get 403; the `X-CSRF-Token`
  check stays in the monolith. CORS runs before the admin layers, so edge 401/404/429 carry CORS headers.
- **Rate limits** (Redis, `RATE_LIMIT_<CLASS>[_EMAIL]=max/window`; auth classes fail closed): `ADMIN_LOGIN` 10/5m per IP
  and 5/15m per email, `ADMIN_LOGIN_TOTP` 10/5m per IP, `ADMIN_ACCEPT_INVITE` 10/15m, `ADMIN_INVITE_PREVIEW` (GET) 30/15m.
  The product classes are off. The global per-IP limit still applies.
- **Listen address**: `:8080` by default (the compose convention; the old `:8090` default was web-bff's and made the
  container unhealthy). Compose profile `admin-bff` publishes `${ADMIN_BFF_PORT:-8082}`.
- Not done: a Redis-backed validator (`internal/session` Validator) that checks the cookie against `admin_sess`.
