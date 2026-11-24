# Hardening

Cross-cutting changes made while splitting the monolith. Each section says what changed, why, and how to operate it.

## 1. TOTP encryption key

**What.** TOTP secrets (`accounts.users.totp_secret`, regional databases) are AES-GCM encrypted under
`TOTP_ENCRYPTION_KEY` (`internal/auth.TOTPCrypto`). Resolution order in `pkg/config`: `TOTP_ENCRYPTION_KEY`, then
`JWT_SECRET` (the key secrets were originally encrypted with), then, in development only, the built-in
`dev-insecure-jwt-secret-change-me`.

**Production guard.** With `APP_ENV` of `production`, `prod` or `staging` (`config.HardenedEnv`) `config.Load` fails when
there is no key, when the key is the development default, or when it is shorter than 16 characters. Falling back to
`JWT_SECRET` still works there (so existing enrolments keep decrypting) but logs a warning; move to an explicit key.
Anything else (development, test, unset) keeps the development default. Note: `compose.split.yml` sets a development
`JWT_SECRET`; a local stack started with `APP_ENV=staging` will therefore refuse to start until a real
`TOTP_ENCRYPTION_KEY` is given.

**Rotation.** Keys are a list: `TOTP_ENCRYPTION_KEY` (current, used for every new encryption) and
`TOTP_ENCRYPTION_KEY_PREVIOUS` (comma separated retired keys, tried after the current one on decrypt).

1. Generate a key: `openssl rand -base64 32`.
2. Set `TOTP_ENCRYPTION_KEY=<new>` and `TOTP_ENCRYPTION_KEY_PREVIOUS=<old>` (the old value is the old
   `TOTP_ENCRYPTION_KEY`, or the old `JWT_SECRET` if it was relying on the fallback). Deploy every service that decrypts
   secrets. Nothing breaks: old secrets still open, new ones are written under the new key.
3. Rotation has no re-encrypt job: old secrets stay readable through `TOTP_ENCRYPTION_KEY_PREVIOUS` until users re-enrol,
   so keep the previous key for as long as old secrets exist.

## 2. Request logging

**What.** `pkg/logger.RequestLogger` (also used by both BFFs) logs bodies only at debug level, and now:

- never logs request or response bodies for auth and onboarding paths (`logger.SensitivePath`: anything containing
  `/auth`, `/onboarding`, `/totp`, `/login`, `/signup`, `/password`, `/invite`, `/verify`, `/session`, `/token`,
  `/oauth`, `/mfa`, `/otp`), at any level;
- redacts JSON keys containing password, secret, token, authorization, cookie, csrf, api key, otp/totp, credential,
  session, signature, plus whole-key matches for `code`, `pin`, verification and recovery codes. `country_code` and
  similar are not hidden;
- redacts form bodies by key, and falls back to pattern redaction when the body is not parseable JSON, for example
  because the 4 KiB capture cut it mid-value (previously a truncated body was logged unredacted);
- masks JWTs and `Bearer`/`Basic` credentials anywhere in text;
- masks sensitive query parameters and the token segment of invite, token, reset, verify and confirm URLs;
- captures the request body with a tee limited to 4 KiB instead of reading the whole body into memory first.

`logger.RedactHeaders` is exported for code that wants to log headers; nothing logs headers today.

**BFF access log.** `internal/bff` only uses `logger.RequestLogger`, so it inherits all of the above. It logs no headers
and no bodies of its own, and its rate limiter peeks at the JSON `email` of auth bodies (hashed, never logged).

**Operate.** Keep `LOG_LEVEL=info` in production; debug is for development. If a new route carries secrets under a path
not matched above, add a marker to `sensitivePathMarkers` in `pkg/logger/middleware.go`.

## 3. Notifications

**Retention.** The stream that carries email requests holds recipient addresses and one-time codes. A reference design
(event carries an id, the service fetches the code) was not done: it adds a synchronous dependency and a store with its
own expiry, which is neither small nor clearly safer. Instead `NOTIFICATIONS_STREAM_MAX_AGE` (Go duration, 5m to 168h,
default `24h`) sets the stream's max age. Pick a value longer than the longest outage of the notifications service you
want to survive without losing mail: events older than the limit are dropped by the broker unsent. One-time codes expire
in minutes, so `1h` to `2h` is reasonable. The limit is applied whenever any process connects, so the server,
`kyb-sweep`, `hiring-outbox` and the notifications service must all run with the same value (the local compose file
passes it to `server` and `notifications`). An invalid value is ignored with a warning.

**Own database.** `NOTIFICATIONS_DB_URL` points the service at its own database. `db/migrations/notifications` holds
`events_inbox` only, and `db/migrations/migrate-all.sh` applies it when the variable is set (production's migrate task
is unchanged until you set it). The local stack defaults to `openhr_notifications`; set `NOTIFICATIONS_DB_URL=` (empty)
to fall back to the global database. Moving a live environment, including copying processed ids, is in
`notifications.md`.

**email-worker retirement.** `make email-queue-depth` reports what is left in the Fluvio email queue (exit 0 when
empty). The steps are in `notifications.md`.

## 4. BFF hardening

- **`TRUSTED_PROXY_CIDRS`** (default: none, deliberately). With none, the client IP is the socket peer and
  `X-Forwarded-For` is ignored, which is safe but, behind a load balancer, puts every user in one rate-limit bucket.
  Set it to exactly the addresses of your proxies (ALB subnets, ingress pods), never `0.0.0.0/0`: a trusted hop lets
  its `X-Forwarded-For` choose the client IP.
- **`ORIGIN_REQUIRED`** (default `false`). `true` rejects unsafe requests (POST, PUT, PATCH, DELETE) that carry no
  `Origin` header, which closes CSRF through non-browser-looking requests. Turn it on in production when only browsers
  call the BFF; server-to-server callers must then send an allow-listed `Origin`. Keep `ALLOWED_ORIGINS` set to the
  exact frontend origins.
- **Startup warning.** Outside development (`internal/bff/warnings.go`, `Config.Warnings`) the BFF logs one warning for
  each of: `TRUSTED_PROXY_CIDRS` unset, `ALLOWED_ORIGINS` unset, `ORIGIN_REQUIRED=false`. They never block startup.

## 5. CI and boundaries

**Workflow `.github/workflows/go.yml`** (pull requests, pushes to `staging` and `production`, manual). It is read-only
and independent of the deploy workflows (`container-images.yml`, `migrate.yml`).

- `test` job: Go from `go.mod` (`go 1.26.0`, via `go-version-file`), a Postgres 16 service container and a NATS
  JetStream container (started with `docker run` because service containers cannot pass `-js`), exporting
  `EVENTS_PG_URL`, `JOBMODEL_PG_URL` and `EVENTS_NATS_URL`. Steps: `go mod tidy` is a no-op, `go build`, `go vet`, the
  boundary tests (`TestNoCloudSDKs` in `pkg/events`, `internal/hiring/appjobs` imports test) run first and uncached,
  `go test -race ./...`, and a final run of the database and broker packages that fails if any test was skipped.
- `proto` job: `buf lint`, then `make proto` and a check that `gen/` has no changes or new files.
- Not in CI yet: golangci-lint (see below) and the OpenAPI check (`make swagger-check`).

**Depguard.** `.golangci.yml` now carries service-boundary rules next to the existing cloud-SDK ones: the shared library
(`pkg/**`) imports no service; BFFs import no domain code; notifications is a leaf; the applications side
(`internal/hiring/app*`, `apply`, `reapply`, `retention`, `uploads`, `internal/sensitive`) imports nothing hiring owns,
which mirrors the Go test; `internal/applicationsvc` and `cmd/applications` import no identity or org handler code; hiring
does not import org or identity packages; org does not import hiring. depguard can only deny by path pattern, so these
are denylists, not an architecture checker, and a deny of `internal/app` is impossible because prefix matching would
also hit `internal/applicationsvc`. Known gap: `internal/hiring/hiringhttp` imports applications packages today, so the
reverse rule is not listed.

**Verification.** The prebuilt golangci-lint release binaries panic on this toolchain; building golangci-lint v2.15.0 from
source with the module's Go (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`) works. Running
it exercised the rules: they fire on injected violations in `pkg/`, `internal/bff/` and `internal/hiring/`, and found two
real problems in the config, now fixed: prefix matching made `internal/admin` deny `internal/adminbff` (carved back out with
`list-mode: lax` plus an allow entry), and `internal/bff/identity_test.go` deliberately imports monolith handlers to check
routing (tests are exempt from `bff-no-domain`). The `lint` job in `go.yml` gates on `--enable-only depguard`; the other
linters report only.

## 6. Hermetic tests

`internal/app` `TestBusNeeded` now clears `APPLICATIONS_EVENTS` (and sets `HIRING_EVENTS` per case) with `t.Setenv`, so
it passes whatever the developer's shell exports.

## 7. Service-to-service gRPC authentication

**What.** Every gRPC listener and every gRPC client goes through `pkg/rpc` (`rpc.ServerOptions`, `rpc.Dial`). `GRPC_AUTH`
selects the mode; the default `off` keeps existing stacks unchanged.

| Variable | Meaning |
| --- | --- |
| `GRPC_AUTH` | `off` (default), `token` or `mtls`. An unknown value or an incomplete setup panics at start-up: a service asked to authenticate never comes up open |
| `GRPC_SERVICE_NAME` | this process's name (falls back to `SERVICE_NAME`, then the binary name). Caller name on outgoing calls, audience it accepts in token mode |
| `GRPC_AUTH_SECRET` / `_FILE` | token mode: shared HMAC secret, at least 16 bytes |
| `GRPC_TLS_CERT`, `GRPC_TLS_KEY`, `GRPC_TLS_CA` (each also `_FILE`) | mtls mode: PEM contents or files. TLS 1.3, both sides verified against the CA |

- **mtls** (primary): the caller is the client certificate's common name, so issue each service a certificate whose CN is its
  service name and whose DNS SANs include the name peers dial (`org`, `org.svc.cluster.local`, ...). Plaintext and token-only
  clients are refused at the handshake.
- **token** (fallback, no PKI): each call carries `authorization: Bearer <HS256 token>` with `iss` = caller, `aud` = callee
  service, `iat`, `exp` (60 s, 10 s skew). A token for another audience, an expired one or a wrong signature gives
  `Unauthenticated`. There is no replay protection inside the TTL and the secret is shared by all services: use mtls where
  you can. Token mode runs over plaintext, so use it only on a trusted network.
- **Allow-lists** (`rpc.AllowList`, enforced in both modes, `PermissionDenied`): `OrgService.ResolveMembership` hiring,
  applications, identity; `GetPublishReadiness` hiring, applications, identity; `AdminOrgService.ApplyKybDecision` identity;
  `AdminIdentityService.EnqueueVerification` org; `ApplicationsService.GetApplicantCounts` hiring. `server` (the monolith) is
  included wherever it makes the same call while a service is not extracted. Other methods accept any authenticated caller.
  Jobs and tools are callers too: set `GRPC_SERVICE_NAME` (and for mtls give them a certificate) to call a restricted method.
- **Local compose.** `make local-grpc-certs` (`scripts/gen-grpc-certs.sh`, `cmd/tools/grpccerts`) writes a throwaway CA and one
  certificate per service to `deploy/local/certs` (gitignored); each gRPC service mounts its directory at `/run/grpc`. Then
  `GRPC_AUTH=mtls make local-up PROFILES=...`. The all-remote recipe in `local-dev.md` is verified with `GRPC_AUTH=mtls`.
  Probes in `cmd/tools` need the same variables (and a certificate whose CN they are allowed to use); without credentials
  they are refused, which is also the quickest check that auth is on.
- **Production.** Mount certificates from your platform (cert-manager, ACM PCA, Vault, a service mesh) as files and point the
  `_FILE` variables at them. Rotation: replace the files and restart; the CA bundle may hold old and new roots during a roll.

## 8. gRPC client resilience

All clients use `rpc.Dial`: target `dns:///host:port`, DNS re-resolution after a failed connection at most every 2 s
(`dns.SetMinResolutionInterval`; grpc-go's default of 30 s kept a recreated peer unreachable for about half a minute),
reconnect backoff 0.2 s to 3 s, keepalive pings (servers permit them), and a default deadline of 5 s (`ORG_GRPC_TIMEOUT`
for org) when the caller sets none. A service config retries **only safe methods** (`Get*`, `List*`, `BatchGet*`, `Resolve*`,
`Validate*`, `Lookup*`, `Check*`, `Has*`, or proto `idempotency_level` NO_SIDE_EFFECTS/IDEMPOTENT) on `UNAVAILABLE`, up to 4
attempts with 0.2 s to 2 s exponential backoff, and makes them wait for the channel to be ready (bounded by the deadline).
Mutating calls (`Submit*`, `Publish*`, `Apply*`, `ChangeStatus`, ...) fail fast and are never retried; add an idempotency key
first if one ever needs to be. Side effect of wait-for-ready: a TLS misconfiguration on a safe method shows up as
`DeadlineExceeded` after the deadline instead of an immediate error; read the server log.

Live check (all-remote stack, `GRPC_AUTH=mtls`): `docker compose up -d --force-recreate --no-deps applications` while
`GET /jobs` through the BFF ran every 0.5 s (hiring reads applicant counters from applications over gRPC): 101 of 101 requests
returned 200, the slowest took 1.05 s (the call that waited for the new container), the rest took under 100 ms.

## 10. Lint in CI

`go.yml` has a `lint` job that installs golangci-lint with `go install` (compiled by the Go version of `go.mod`, which avoids
the prebuilt binary's toolchain panic), runs it with `.golangci.yml`, prints the finding count and per-linter counts in the
job summary, and never fails the build (`continue-on-error`) until the findings are triaged.
