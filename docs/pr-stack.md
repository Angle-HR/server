# PR stack for the service split

`split/integration` (90ish commits on `staging`) is cut into 11 stacked branches plus one docs branch. Each branch is
based on the previous one (`pr/01` on `staging`), adds one layer, and passes `go build ./... && go vet ./... && go test
./...` on its own (tests needing Postgres or NATS skip without their env vars). The tree of `pr/11-local-dev-stack` is
identical to the tree of `split/integration`. `pr/12-stack-docs` adds only this file and `scripts/check-pr-stack.sh`.

Verify with `scripts/check-pr-stack.sh` (checks out each branch in a throwaway worktree and runs build, vet, test), and
with `git rev-parse pr/11-local-dev-stack^{tree} split/integration^{tree}`.

## Review order

Review and merge top to bottom; every PR targets the previous branch, and is retargeted to `staging` once the one below
it merges.

| # | Branch | Base | Size vs base (non-test, non-gen, non-spec lines) | Shortstat vs base |
|---|--------|------|-----|-----|
| 01 | `pr/01-shared-libs` | `staging` | 6.4k | 110 files, +21838 -203 (mostly generated protobuf) |
| 02 | `pr/02-waitlist-removal` | 01 | 1.3k | 20 files, +276 -1645 |
| 03 | `pr/03-sessions` | 02 | 8.7k (see note) | 126 files, +10169 -2159 |
| 04 | `pr/04-bff` | 03 | 1.9k | 23 files, +2920 |
| 05 | `pr/05-notifications` | 04 | 0.8k | 14 files, +857 -6 |
| 06 | `pr/06-applications` | 05 | 8.6k (see note) | 136 files, +11138 -1268 |
| 07 | `pr/07-hiring` | 06 | 1.8k | 17 files, +2100 -2 |
| 08 | `pr/08-org` | 07 | 1.4k | 9 files, +1448 |
| 09 | `pr/09-identity` | 08 | 1.2k | 8 files, +1209 |
| 10 | `pr/10-hardening` | 09 | 1.1k (+ regenerated swagger spec, about 14k lines) | 15 files, +7690 -7333 |
| 11 | `pr/11-local-dev-stack` | 10 | 2.8k | 23 files, +2747 -19 |
| 12 | `pr/12-stack-docs` | 11 | 0.3k | this file and the check script |

Layers over the ~8k line target:

- 03 (8.7k): the handler files move from `internal/handler` to `internal/identityhttp` and `internal/orghttp` in the same
  change that replaces JWT with sessions, so every moved file counts twice (delete + add). The session change cannot be
  reviewed apart from the handlers it rewrites: the old handlers do not compile against the new `internal/auth`. The BFF
  (about 2k) was split off as 04 to keep this one down.
- 06 (8.6k): the hiring package renames (`hiringstore` to `appstore`, `hiringhttp` to `apphttp`), `dbrouter` blob storage,
  and the applications service land together because `hiringhttp`, `apply`, `uploads` and the stores import each other.
  About 1.1k is the job read model (`applicationsvc/jobmodel`), which could be reviewed separately.
- 01 (6.4k non-generated) also carries 17k lines of generated protobuf Go and tests.

## What each branch does

### 01 `pr/01-shared-libs`

Shared libraries and contracts, consumed by nothing yet: `pkg/events` (outbox, inbox, NATS, in-memory bus, test suite),
`pkg/rpc` (dial, interceptors, caller context, gRPC auth with mTLS or token, dev CA), `pkg/rbac`, `pkg/formschema` (moved
from `internal/hiring/{questions,screening}`, importers updated), `pkg/platform/{blob,secrets}`, `proto/` and `gen/`,
the outbox/inbox migrations (`000018`, global `000011`), `go.mod`/`buf` tooling, `make/proto.mk`, `cmd/tools/grpccerts`.

### 02 `pr/02-waitlist-removal` (BREAKING)

Removes the public waitlist signup, the admin waitlist routes, the confirmation email, waitlist SQL builders and the
`waitlist:*` permissions (migration global `000015`). Shared helpers that lived in `waitlist.go` move to
`handler/common.go` and `handler/admin_paging.go`. Reviewable on its own.

### 03 `pr/03-sessions`

Cookie sessions replace JWT: `internal/session` (Redis store, CSRF, origin guard), `internal/auth`, config and logging
changes. Auth, onboarding, company-verification and admin identity handlers move into `internal/identityhttp` and
`internal/orghttp` (still mounted by `cmd/server`), with their supporting packages (`userdir`, `verifyqueue`,
`identityevents`, `orgevents`, `orgstore`, `kyb`, `mailer` link helpers) and migrations (global `000012`, `000014`,
`000016`, `000017`, regional `000023`, `000024`). Docs: `docs/microservices/sessions.md`.

### 04 `pr/04-bff`

`cmd/web-bff`, `cmd/admin-bff`, `internal/bff`, `internal/adminbff`: session and CSRF termination, rate limit and reverse
proxy for browsers. Docs: `docs/microservices/bff.md`.

### 05 `pr/05-notifications`

`cmd/notifications` and `internal/notifications`, the `NOTIFICATIONS_TRANSPORT=events` email enqueuer in `internal/app`,
`cmd/kyb-sweep` and `cmd/email-worker` changes, the notifications inbox migration and `migrate-all.sh`.

### 06 `pr/06-applications`

Applications split from the hiring monolith: `internal/hiring/{appstore,apphttp,appjobs,appevents,jobsource,
callerresolver,applicantcounts,jobevents,orgclient}`, changes to `hiringstore`, `hiringhttp`, `draft`, `apply`, `uploads`
and friends, `internal/applicationsvc` (with `jobmodel` and `companydeletion`), `cmd/applications`, backfill, verify and
probe tools, blob-based uploads in `dbrouter` (replaces the R2 minio client), migrations `000019`-`000022` and global
`000013`. Docs: `applications.md`, `applications-jobmodel.md`.

### 07 `pr/07-hiring`

`cmd/hiring` and `internal/hiringsvc` (HTTP and gRPC, org lookups through `orgclient`), `cmd/hiring-expire`,
`cmd/hiring-jobs-backfill`, `hiringprobe`, `orgfake`, `orgprobe`.

### 08 `pr/08-org`

`cmd/org` and `internal/orgsvc` (HTTP and gRPC over `orghttp`/`orgstore`), `cmd/org-backfill-individuals`. Docs: `org.md`,
`individual-accounts.md`.

### 09 `pr/09-identity`

`cmd/identity` and `internal/identitysvc`, `identityprobe`, `totpcode`. Docs: `identity.md`.

### 10 `pr/10-hardening`

`cmd/totp-rekey` (TOTP key rotation), CI workflow, depguard rules (`.golangci.yml`), `make totp-rekey-dry-run` and
`email-queue-depth`, swagger regeneration for the whole series, removal of `internal/rbac` (now `pkg/rbac`). Docs:
`hardening.md`, `platform-ports.md`. The gRPC auth, resilience and log redaction code itself lives in layers 01 and 03
(`pkg/rpc`, `pkg/logger`) because its callers need it from the start; this layer carries the tooling around it.

### 11 `pr/11-local-dev-stack`

`deploy/local` compose stack, `make/local.mk` and make targets, `scripts/e2e-*` and smoke scripts, Dockerfile and
docker-compose changes, `.env.example`, `.gitignore`, CHANGELOG, `docs/microservices/{README,BOUNDARIES,local-dev,
switches}.md`.

## Files with intermediate versions

These files appear in a stripped or earlier form in a lower branch so that it builds, and reach their final form in the
branch named in the last column.

| File | Intermediate state | Final in |
|------|--------------------|----------|
| `pkg/config/config.go` (+ test) | `staging` version in 01 (final drops the JWT fields that `internal/app` still reads) | 03 |
| `Makefile` | 01: only `include make/proto.mk`; 10: adds `totp-rekey-dry-run`, `email-queue-depth` | 11 |
| `internal/handler/{admin.go,admin_doc_types.go,doc_types.go}` | 02: `staging` version with waitlist code removed, still JWT-based | 03 |
| `internal/mailer/{mailer.go,mailer_test.go}` | 02: waitlist removed only | 03 |
| `internal/handler/testhelpers_test.go` | 02: helpers lifted out of the deleted `waitlist_test.go` | 03 |
| `internal/app/app.go` | 03: no applicant counts, no hiring remote switch, plain Fluvio email queue; 05: adds email transport selection | 06 |
| `internal/app/notifications.go` (+ test) | 05: event gates for org and identity only | 06 |
| `internal/app/candidate.go`, `internal/dbrouter/*` | `staging` versions (minio/R2) until 06 | 06 |
| `internal/docs/spec/*` | stale (still lists waitlist and bearer auth) in 01-09 | 10 |
| `.env.example`, `.gitignore`, `CHANGELOG.md`, `Dockerfile`, `docker-compose.yml` | `staging` versions | 11 |

Other things to know:

- Migration numbers are not contiguous in the middle of the stack (global `000015` lands in 02, before `000012`/`000014`
  in 03). Fresh databases are fine; an environment that applied some of them must apply the full stack in order.
- `internal/rbac` is deleted in 10 although its replacement `pkg/rbac` lands in 01 (nothing imports it after 03).

## Suggested PR titles and descriptions

Open each PR against the branch below it (01 against `staging`).

### 01 Shared libraries, platform ports and gRPC contracts

Adds the building blocks of the service split without changing behaviour: the events library (transactional outbox,
inbox, NATS and in-memory buses), gRPC helpers with mTLS/token auth, RBAC and form-schema packages (the last moved out
of `internal/hiring`), blob and secrets ports, protobuf sources with generated Go, the events migrations and buf/make
tooling. Review `pkg/events` and `proto/` first; `gen/` is generated.

Test plan: `go build ./... && go vet ./... && go test ./...`.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 02 BREAKING: remove the waitlist

Removes the waitlist signup and admin routes, email template, SQL builders and permissions. Clients calling
`POST /waitlist` or `/admin/waitlist` get 404 after this. A migration deletes the `waitlist:read`/`waitlist:write`
permissions from roles. The swagger spec is regenerated in 10.

Test plan: tests pass; confirm no frontend still calls the waitlist endpoints before merging.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 03 Cookie sessions instead of JWT; identity and org HTTP handlers split out

Authentication becomes an HttpOnly session cookie with a CSRF token and an origin guard on unsafe methods; bearer tokens,
refresh tokens and the revocation list are gone (BREAKING for API clients). The handlers that use sessions move to
`internal/identityhttp` and `internal/orghttp`, still served by `cmd/server`, so they can later run as separate services.
Large because of file moves; use `git diff -M` and review `internal/session` and `internal/auth` first.

Test plan: tests pass; log in, onboard and use the admin console against a local stack with a browser.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 04 Add the web and admin BFF

Two edge services that terminate the browser session, enforce CSRF and rate limits, and proxy to the backends with the
caller identity attached. Not wired into the compose stack until 11.

Test plan: unit tests in `internal/bff` and `internal/adminbff`.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 05 Notifications service and events email transport

`cmd/notifications` consumes email events from the bus and sends them. `NOTIFICATIONS_TRANSPORT=events` switches
producers from the Fluvio queue to the outbox; the default is unchanged.

Test plan: unit tests; with NATS, run the notifications inbox migration and send one email through the events transport.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 06 Extract the applications service

Moves applications (apply, uploads, retention, reviews) behind their own store, HTTP and gRPC surface and adds the job read
model, domain events and the company-deletion projector. Uploads use the blob port, so S3-compatible and Azure storage
both work. Schema changes drop cross-service foreign keys. `cmd/server` still serves everything by default.

Test plan: unit tests; with Postgres, run migrations and the applications e2e (11).

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 07 Hiring service

`cmd/hiring` serves job drafts and publishing over HTTP and gRPC, reading company data from the org service and
publishing job events. Adds the expire and backfill jobs and the dev probes.

Test plan: unit tests; run the hiring probe against a local stack.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 08 Org service

`cmd/org` serves countries, onboarding, company verification, memberships and company deletion over HTTP and gRPC, and
publishes org events. Individual accounts get a personal org; `cmd/org-backfill-individuals` creates them for existing
users (idempotent).

Test plan: unit tests; run the backfill with `-dry-run` first.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 09 Identity service

`cmd/identity` serves auth, admin staff/roles, admin TOTP and verification review over HTTP and gRPC and publishes
identity events.

Test plan: unit tests; identity probe and the auth e2e (11).

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 10 Hardening: TOTP key rotation, CI, depguard, swagger

`cmd/totp-rekey` re-encrypts TOTP secrets after a key rotation (run `make totp-rekey-dry-run` first), CI workflow,
depguard rules between service packages, regenerated swagger spec, removal of the old `internal/rbac`.

Test plan: CI green; `make totp-rekey-dry-run` against a staging copy.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 11 Local split-service dev stack and e2e scripts

Compose stack with one container per service plus BFFs, Postgres, NATS, Redis and MinIO, make targets, e2e and smoke
scripts, env and Docker updates, the microservices docs and the changelog. Tree of this branch equals `split/integration`.

Test plan: `make local-up && make local-e2e`.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

### 12 Stack documentation and check script

Adds `docs/pr-stack.md` and `scripts/check-pr-stack.sh`. Not intended to merge to `staging`; drop it from the last PR if you
do not want it in the history.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
