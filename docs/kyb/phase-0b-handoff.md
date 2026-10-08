# KYB phase 0b: handoff

Company verification (KYB V2) for OpenHR. Branch: `kyb-phase-0b` (not pushed). Last updated 5 Oct 2026.
The KYB V2 document is the source of truth; where Figma disagrees, the documents win.

## Design in one paragraph

Tier 1 countries have a free official API and are checked immediately; Tier 2 countries go to a manual
portal check by an operator (status `pending`). Stored statuses: `not_started | pending | verified | failed`.
Failure reasons: `number_not_found | name_mismatch | address_mismatch | inactive_entity | wrong_country`.
Publishing a job needs `verified`; drafting is always allowed. `inactive_entity` is a hard block (no retry),
`address_mismatch` is a soft warning (user confirms address type), `number_not_found` and `name_mismatch`
use the light retry form (name and number only).

## Done

| Area | Where |
|---|---|
| Domain logic: statuses, reasons, display badge, submit / resubmit / confirm name / confirm address / change country / review, nudge and deletion timing | `internal/kyb/status.go`, `service.go` (+ tests) |
| Name and address matching | `internal/kyb/match.go` (+ tests) |
| Verifier interface and per-country registry (no verifier = Tier 2) | `internal/kyb/verifier.go` |
| UK verifier (Companies House) | `internal/kyb/companieshouse.go` (+ tests, mocked HTTP) |
| Migrations: tables, events (append-only), global queue, submitted address and nudge count | `db/migrations/000005_kyb.sql`, `000006_kyb_followups.sql`, `db/migrations/global_registry/000005_verification_queue.sql` |
| pgx Store (regional) and Queue (global) | `internal/kyb/kybstore/` (+ pgxmock tests) |
| Owner endpoints under `/api/v1/organization/verification`: GET status, POST submit, `/retry`, `/confirm-name`, `/confirm-address`, `/change-country` | `internal/handler/kyb.go` (+ `kyb_test.go`) |
| Error code `SERVICE_UNAVAILABLE` (503) | `pkg/apperror/errors.go` |
| Config `COMPANIES_HOUSE_API_KEY`; UK verifier registered only when set (otherwise UK is manual review) | `pkg/config/config.go`, `internal/app/app.go` (`newKYBRegistry`), `.env.example` |

Verified in the sandbox: gofmt, `go vet`, `go test -race` for `internal/kyb/...` and `internal/handler`, and
golangci-lint on the new code (only goconst noise from a newer linter than the project's).

## Added 5 Oct (second session, written without a working Go toolchain)

Compiled and tested in a scratch module (pass): `internal/kyb` (incl. new sweep and lifecycle tests) and `internal/onboarding` format checker.
gofmt-clean but NOT compiled or tested (need pgx, fluvio and other modules): `internal/kyb/kybstore`, `kybnotify`, `internal/handler`, `internal/mailer`, `internal/app`, `cmd/kyb-sweep`.

1. Admin review queue: `internal/handler/admin_verification.go` (+ test). GET `/admin/verification`, GET `/admin/verification/{organizationID}`, POST `/admin/verification/{organizationID}/review` (`{"decision":"approve|reject","reason":...}`). Permission `verification:review` (const in `internal/admin/permissions.go`, seeded for superadmin by `db/migrations/global_registry/000006_verification_review_permission.sql`). Audited.
2. Emails: templates `kyb_failed`, `kyb_review_queued`, `kyb_nudge_1`, `kyb_nudge_2` (mailer, link goes to `{APP_URL}/dashboard`; confirm the real frontend route). `internal/kyb/kybnotify` queues them; handlers take a per-region `NotifierFor` factory (wired in `internal/app`). Sweep: `kyb.Sweeper` + `cmd/kyb-sweep` (one-shot, run hourly from a scheduler; not added to Dockerfile/CI). 30-day case only sets `deletion_flagged_at` (migration `000007_kyb_deletion_flag.sql`), deletes nothing.
3. Ownership transfer: `Service.OwnershipTransferred` (`internal/kyb/lifecycle.go`). Verified/failed go back to `not_started`; pending left alone. Hook point: call after the new owner is saved.
4. Publish gate: `kyb.RequirePublish` / `RequirePublishFor` (`ErrNotVerified`, mapped to 403 in `kybError`). Hook point: job-publish handler.
5. `onboarding.RegistrationNumberFormatOK` set as `KYBHandler.Formats` (GB, NG, DE, KE only; others pass).
6. `activeStatuses` checked against Companies House enumerations: correct.

## Still to do

- Run `gofmt`, `go vet`, `go test -race ./...`, lint, `make security` on a machine with module access (item 7).
- Run `make swagger` (new admin endpoints add annotations) and migrate: regional 000007, global 000006.

## Open items needing the owner (Owen)

- Companies House API key (answered "not yet"). Everything is built and tested against mocks; a real UK lookup needs it.
- Run `make swagger`: swagger annotations are written but `internal/docs/spec` is stale, so `make swagger-check`
  will fail until regenerated (the sandbox cannot fetch the generator).
- Run `make migrate` / `make migrate-global` for the new migrations.
- Re-run `make check`, `make test` and `make security` on his machine (sandbox used a newer golangci-lint).
- Owen commits himself. Never commit the `*.log` files or `.cursor/debug-b69c38.log`. Do not push.

## Product decisions in force (5 Oct)

KYB happens before job creation; US launch first; Kenya retention 12 months; pay transparency is a hard block.
See the open-questions file for the rest.

## Working notes for an agent in the sandbox

- Go modules cannot be fetched from go.dev / proxy.golang.org / golang.org / gopkg.in / go.uber.org (blocked);
  github.com works. Previous sessions used a Go toolchain in `$HOME/gotc`, golangci-lint in `$HOME/gl`,
  mirror clones in `$HOME/mods`, and a scratch copy of the repo at `$HOME/work/server` with a `go.mod` that has
  replace directives. Edit in the real repo, rsync `internal pkg cmd` to the scratch copy (never `go.mod`/`go.sum`),
  then build and test there. These paths live on the user's linked computer and may need rebuilding.
- Each shell call needs: `export PATH=$HOME/gotc/bin:$HOME/gl:$PATH GOROOT=$HOME/gotc GOPROXY=off GOSUMDB=off GOFLAGS=`
  and `</dev/null` for golangci-lint.
- Conventions: use `besteffort.Log` for ignorable errors; keep functions under the cyclomatic/cognitive limits
  (extract helpers); no magic numbers; handler tests use pgxmock with `dbrouter.NewWithPools`.
- Do not start new code without the owner's go-ahead.
