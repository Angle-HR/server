# Local development stack (Docker)

Docker is the only environment the microservice split is tested in for now. Compose project name: `openhr-local`.

## Prerequisites

- Docker Desktop (or Engine) with Compose v2.20+, `make`, `curl`, `jq`.
- 8 GB RAM is enough: the base stack idles at about 300 MB. See "Resources" below.
- Frontends (`web` profile) expect `../client` and `../admin` next to the server checkout. From a git worktree set
  `CLIENT_DIR` and `ADMIN_DIR`.

## Commands

| Command | What it does |
| --- | --- |
| `make local-up` | Build and start the base stack (postgres x6 DBs, redis, minio, migrate, server, mailhog, email-worker, fluvio-ui, nats) and wait until healthy |
| `make local-grpc-certs` | Throwaway dev CA and per-service certificates in `deploy/local/certs` (gitignored) for `GRPC_AUTH=mtls` |
| `make local-up PROFILES="identity org"` | Also build and run those service profiles. Others: `hiring applications notifications web-bff admin-bff`, `split` (all seven), `web`, `clamav` |
| `make local-smoke` | `scripts/smoke-split.sh`: health of every running service; profiles that are off show SKIP |
| `make local-e2e` | `scripts/e2e-auth.sh` (AUTH_MODE=cookie by default, `AUTH_MODE=bearer` for the old mode) then `scripts/e2e-onboarding.sh` |
| `make local-e2e-all` | On ONE FRESH stack (`make local-reset` first): `e2e-auth`, `e2e-apply`, `e2e-identity`, `e2e-couplings` (deletes the company `e2e-apply` made), `e2e-admin-totp` (last: its lockout step blocks the bootstrap admin for 15 minutes). With `ADMIN_TOTP_REQUIRED=true` the first admin login (`e2e-apply`) enrols the bootstrap admin and the later scripts complete TOTP with `cmd/tools/totpcode` (`scripts/lib/e2e-common.sh`) |
| `make local-logs [SERVICE=server]` | Follow logs |
| `make local-down` | Stop everything (keeps volumes) |
| `make local-reset` | Stop and delete volumes (all databases, minio, nats) |

The same stack without make: `docker compose -f docker-compose.yml -f deploy/local/compose.split.yml --profile split up -d --build`.
The plain `docker compose up --build` still brings up the monolith stack without nats.

## Ports

| Port | Service | Notes |
| --- | --- | --- |
| 8080 | server (monolith) | `/healthz`; API under `/api/v1` |
| 8081 | web-bff | profile `web-bff` / `split` |
| 8082 | admin-bff | profile `admin-bff` / `split` |
| 8101-8105 | identity, org, hiring, applications, notifications | HTTP health only; gRPC (9090) is internal |
| 3000 / 3001 | client / admin frontends | profile `web` |
| 5432 | postgres | user `openhr`, password `change-me`, DBs `openhr_{global,uk,us,africa,eu,asia,notifications}` |
| 6379 | redis | |
| 4222 / 8222 | nats client / monitoring | JetStream on; `http://localhost:8222/healthz`, `/jsz` |
| 9000 / 9001 | minio API / console | `minioadmin` / `minioadmin` |
| 1025 / 8025 | mailhog SMTP / UI+API | |
| 5173 | fluvio-ui | |
| 3310 | clamav | profile `clamav` |

All ports are overridable (`SERVER_PORT`, `IDENTITY_PORT`, `NATS_PORT`, ...).

## Service conventions

- HTTP on container port `8080`: `/healthz` (liveness, no dependencies) and `/readyz` (readiness, checks dependencies).
  The smoke test treats a 404 on `/readyz` as "not implemented yet", not a failure.
- gRPC on container port `9090`, reachable only on the `anglehr_net` network as `<service>:9090`. Peer addresses are
  injected as `IDENTITY_GRPC_ADDR`, `ORG_GRPC_ADDR`, ... `NOTIFICATIONS_GRPC_ADDR`.
- Env injected into every service: `HTTP_ADDR`, `GRPC_ADDR`, `NATS_URL=nats://nats:4222`, `REDIS_URL`, the six database
  DSNs, `R2_*` for minio, `JWT_SECRET`, `SERVICE_NAME`.
- Container runs as uid 10001, image is about 37 MB, `HEALTHCHECK` probes `/healthz`.

## Adding a new service

1. Create `cmd/<service>/main.go` honouring the conventions above.
2. In `deploy/local/compose.split.yml` the service block already exists for the seven planned names. For a new name copy
   one of them: change the profile, `args.SERVICE`, image name and host port. Add the name to `scripts/smoke-split.sh`.
3. `make local-up PROFILES="<service>"`, then `make local-smoke`.
4. Build only (no compose): `docker build -f deploy/local/Dockerfile.service --build-arg SERVICE=<service> .`
   If `cmd/<service>` is missing the build fails fast with a clear message; `up` without that profile is unaffected.

## Running e2e

```
make local-up
make local-e2e                      # cookie mode (default)
AUTH_MODE=bearer make local-e2e     # legacy bearer mode
```

`scripts/e2e-auth.sh`: signup, 6-digit code read from the mailhog HTTP API, verify-email, login, `/auth/me`, business
onboarding, job draft create and publish-check, logout. `AUTH_MODE=bearer` sends `Authorization: Bearer`. `AUTH_MODE=cookie`
keeps a cookie jar, requires the login response to set `openhr_session` and return `data.csrf_token`, and sends it as
`X-CSRF-Token` on unsafe methods. The job flow stops at the first missing prerequisite (set `STRICT_JOBS=1` to fail
instead). Publishing itself needs a verified company (KYB), accepted DPA and complete details and is not attempted.
Note: after logout a bearer access token stays valid until expiry; the script only warns.

`scripts/e2e-apply.sh` exercises the public apply path end to end: company signup and onboarding, KYB submitted for a
country without an automated registry (so it goes to manual review), approved through the admin API with the bootstrap
admin, DPA and privacy contact, a published job, an anonymous application (with an idempotent retry and a repeat), the
company listing and opening it, `received -> in_review -> rejected`, and the confirmation and status emails in mailhog.
The applicant emails sit in `hiring.email_outbox`; the script drains them by running `go run ./cmd/hiring-outbox`
against the stack. It needs the notifications service (the default `make local-up`). Point it at another stack with
`BASE`, `MAILHOG`, `PG_PORT`, `SMTP_PORT` and `MINIO_API_PORT`. File uploads are not covered: the stack has no malware
scanner, and the script only checks that uploads fail closed (503).

## The applications service

`make local-up PROFILES=applications` runs `cmd/applications` next to the monolith (host port `APPLICATIONS_PORT`, default 8104;
gRPC `applications:9090` on the network). It serves the same application routes as the monolith (`/api/v1/public/jobs/{id}/applications`,
`/uploads`, `/jobs/{id}/applications*`, `/hiring/applicant-settings`) and accepts the monolith's session cookie: same Redis, same cookie
names, same CSRF and origin rules. Env: `APPLICATIONS_JOBSOURCE=db|events` (default `db`), `APPLICATIONS_EVENTS=on|off` (default off;
publishes `applications.application.submitted.v1` and `status_changed.v1` through the regional outbox, relayed to `NATS_URL`).
Company and roles are still read from the `accounts.*` tables (interim `internal/hiring/callerresolver`, until org serves them over gRPC).

Run the apply e2e with the application side on the service: `BASE_APPS=http://localhost:8104/api/v1 scripts/e2e-apply.sh`.

## The identity service

`make local-up PROFILES="identity web-bff"` with `IDENTITY_REMOTE=on UPSTREAM_IDENTITY_URL=http://identity:8080
SESSION_VALIDATION=on IDENTITY_EVENTS=on` runs the auth, session and admin-identity routes in `cmd/identity` behind the
BFF (details: `docs/microservices/identity.md`). Checks: `BASE=http://localhost:8081/api/v1 AUTH_MODE=cookie
scripts/e2e-auth.sh`, `BASE=... scripts/e2e-identity.sh` (edge refusal, admin cookie, staff invite, events) and
`scripts/e2e-apply.sh` (KYB approved through the identity service). `go run ./cmd/tools/identityprobe` prints the
IDENTITY stream and calls the gRPC read RPCs (run it on the compose network: gRPC is not published).

`scripts/e2e-admin-totp.sh` (through the admin BFF, `ADMIN_BASE=http://localhost:8082/api/v1`; run the stack with
`IDENTITY_REMOTE=on UPSTREAM_IDENTITY_URL=http://identity:8080` so the admin BFF reaches the identity service, and
optionally `ADMIN_TOTP_REQUIRED=true`) enrols the bootstrap admin, so it needs a fresh stack (or a reset of that admin).

## Resources (8 GB machine)

Measured idle with the base stack: postgres 125 MB, minio 83 MB, mailhog 17 MB, redis 10 MB, server/email-worker/nats/fluvio-ui
under 15 MB each. Each service and the frontends have `mem_limit` (256 MB / 512 MB). Go builds peak at 1-2 GB, the Next.js
build more, so build frontends one at a time if memory is tight. ClamAV needs about 1.2 GB and 1-2 minutes to load
signatures, so it is behind the `clamav` profile. Do not run `docker system prune`; use `make local-reset`.

## Parallel stacks

A second stack next to `openhr-local` needs its own project name, network and host ports, for example
`COMPOSE_PROJECT_NAME=openhr-x LOCAL_NETWORK=openhr-x_net POSTGRES_PORT=15432 REDIS_PORT=16379 MINIO_API_PORT=19000
MINIO_CONSOLE_PORT=19001 SERVER_PORT=18080 MAILHOG_SMTP_PORT=11025 MAILHOG_UI_PORT=18025 FLUVIO_UI_PORT=15173
NATS_PORT=14222 NATS_MONITOR_PORT=18222 NOTIFICATIONS_PORT=18105`, then
`BASE=http://localhost:18080/api/v1 MAILHOG=http://localhost:18025 AUTH_MODE=cookie scripts/e2e-auth.sh`. Email links (password reset, invites) point at `APP_URL` (default `http://localhost:3000`) and `ADMIN_APP_URL` (default
`http://localhost:3001`): set them to the frontend origins of the stack you test. Tear down with
`down -v` under the same variables.

## Known issues

- `minio/minio` and `minio/mc` are no longer pullable from Docker Hub or quay.io; compose uses
  `cgr.dev/chainguard/minio:latest` (override with `MINIO_IMAGE`). The image has no curl, so the healthcheck uses bash `/dev/tcp`.
- `scripts/e2e-onboarding.sh` does not check statuses and keeps using the pre-address token, so its compliance step fails
  with 400 yet it prints SUCCESS. `e2e-auth.sh` switches to the tokens returned by the address step.

## The hiring service

`make local-up PROFILES=hiring` runs `cmd/hiring` (host port `HIRING_PORT`, default 8103; gRPC `hiring:9090`). See
`docs/microservices/hiring.md` for `HIRING_REMOTE`, `UPSTREAM_HIRING_URL`, `ORG_READINESS|MEMBERSHIP|INDUSTRIES=db|grpc`
and `HIRING_EVENTS`. Parallel stacks: set `LOCAL_IMAGE_PREFIX` (and `COMPOSE_PROJECT_NAME`, `LOCAL_NETWORK`) so image tags
such as `openhr-local/web-bff:dev` are not overwritten by another stack's build. Until org-svc exists,
`go run ./cmd/tools/orgfake` serves the OrgService calls hiring makes (point `ORG_GRPC_ADDR` at it, for example
`host.docker.internal:9091` with `ORGFAKE_ADDR=:9091`).

## The org service

`make local-up PROFILES=org` runs `cmd/org` (host port `ORG_PORT`, default 8102; gRPC `org:9090`). With
`ORG_REMOTE=on UPSTREAM_ORG_URL=http://org:8080` and the `web-bff` profile the org paths are served by it; see
`docs/microservices/org.md` for switches, the gRPC surface and events. Use `CALLER_RESOLVER=grpc` on the applications
service to resolve companies through it.

## Every service remote (verified combination)

All eight profiles, every route owned by its service, gRPC between the services, events on, the BFF validating sessions:

```
IDENTITY_REMOTE=on ORG_REMOTE=on HIRING_REMOTE=on APPLICATIONS_REMOTE=on
IDENTITY_EVENTS=on ORG_EVENTS=on HIRING_EVENTS=on SERVER_HIRING_EVENTS=off APPLICATIONS_EVENTS=on
APPLICATIONS_JOBSOURCE=events CALLER_RESOLVER=grpc APPLICANT_COUNTS=grpc
ORG_READINESS=grpc ORG_MEMBERSHIP=grpc ORG_APPLY=grpc IDENTITY_QUEUE=grpc SESSION_VALIDATION=on
ORG_TENANTS=grpc ORG_NOTICE=grpc ORG_COMPANY=grpc IDENTITY_USERS=grpc
APPLICATIONS_ORG_EVENTS=on HIRING_ORG_EVENTS=on
UPSTREAM_APPLICATIONS_URL=http://applications:8080 UPSTREAM_HIRING_URL=http://hiring:8080
UPSTREAM_ORG_URL=http://org:8080 UPSTREAM_IDENTITY_URL=http://identity:8080
make local-up PROFILES="applications hiring org identity web-bff admin-bff"
BASE=http://localhost:8081/api/v1 make local-e2e-all
# couplings checks (second member, Anywhere job, org/identity data, company deletion chain); needs the clamav profile
STATE_FILE=/tmp/state.env UPLOADS=on scripts/e2e-apply.sh && STATE_FILE=/tmp/state.env scripts/e2e-couplings.sh
```

Notes: `ORG_INDUSTRIES` stays `db` (not needed for the scripts). `scripts/e2e-identity.sh` takes `DIRECT` and `NATS_MON` for remapped ports. `scripts/e2e-couplings.sh` reads the applications service's `/internal/jobmodel-stats` (`APPS_HTTP`, default port 8104) and asserts no hiring-table fallback; its second member gets the `line_manager` role through one test-only SQL insert (company roles have no HTTP API). The identity route `DELETE /admin/companies/{id}` needs `ORG_APPLY=grpc`. With `ORG_APPLY=grpc`,
`GET /admin/verification/{id}` reads the case details from identity's own database access to org's regional tables,
because `AdminOrgService` has no case-details RPC; the review decision itself goes over gRPC. Postgres runs with
`max_connections=400` in this stack: every service opens a pool per region. Parallel stack: use
`COMPOSE_PROJECT_NAME`, `LOCAL_NETWORK`, `LOCAL_IMAGE_PREFIX` and remap every `*_PORT`. Tear down with `down -v`.
`UPLOADS=on scripts/e2e-apply.sh` needs the `clamav` profile and `CLAMAV_ADDR=clamav:3310` (arm64:
`CLAMAV_IMAGE=clamav/clamav-debian:stable`).

Service-to-service gRPC authentication on top of that recipe: `make local-grpc-certs`, then add `GRPC_AUTH=mtls` to the
variables (default `off`; `token` needs no certificates). Verified: `e2e-auth`, `e2e-apply` and `e2e-identity` pass with
mtls, and a gRPC call without credentials is refused. Recreating a peer (`docker compose up -d --force-recreate applications`)
while traffic runs recovers within seconds (hardening.md section 8). Parallel stack: the cert directory can be shared, or set `GRPC_CERT_DIR`.

## Region-migration saga e2e

`scripts/e2e-region-saga.sh` on the every-service-remote stack above with `REGION_MOVE=saga REGION_MOVE_RESUME_INTERVAL=2s`
added (and the `notifications` profile; ports remapped for a private project, see below). It checks the happy path (saga
row, registry, pending rows, regional rows, `org.region.moved.v1` in the outbox and on the ORG stream), idempotent
repeats, identity stopped mid-move (stalled saga, compensation, then success), crash recovery by the resumer, company
delete and reactivate (applications tombstone, `hiring-org`), and `identity.staff.deactivated.v1` with its consumer.
It stops and starts the stack's identity container (`IDENTITY_CONTAINER`, default `<project>-identity-1`).

## e2e scripts on a second stack

`scripts/e2e-*.sh` default their URLs from the compose port variables (`SERVER_PORT`, `WEB_BFF_PORT`, `ADMIN_BFF_PORT`,
`APPLICATIONS_PORT`, `MAILHOG_UI_PORT`, `MAILHOG_SMTP_PORT`, `NATS_MONITOR_PORT`, `POSTGRES_PORT`, `MINIO_API_PORT`) and fall back to the
openhr-local ports only when `COMPOSE_PROJECT_NAME` is unset or `openhr-local`; with another project name a missing port is a
fatal error. For the five-script run on the all-remote stack add `RATE_LIMIT_PUBLIC_APPLY=1000/1m RATE_LIMIT_LOGIN_EMAIL=1000/1m
RATE_LIMIT_LOGIN_TOTP_EMAIL=1000/1m RATE_LIMIT_ADMIN_LOGIN=1000/1m RATE_LIMIT_ADMIN_LOGIN_EMAIL=1000/1m
RATE_LIMIT_ADMIN_LOGIN_TOTP=1000/1m`, `APPLICATIONS_JOBFALLBACK=off ORG_COMPANY=grpc IDENTITY_USERS=grpc ORG_NOTICE=grpc ORG_TENANTS=grpc
APPLICATIONS_ORG_EVENTS=on HIRING_ORG_EVENTS=on` and the `notifications` profile (the scripts wait for the events-fed job read model).
