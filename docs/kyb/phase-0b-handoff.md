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

## To do, in order

1. **Admin review queue** (`internal/handler`, new file e.g. `admin_verification.go`).
   - List pending items from `admin.verification_queue` (ids, region, country only).
   - Item detail: read the record from the organization's regional DB (`dbrouter.DB(region)`) so the operator
     can compare against the government portal.
   - Approve / reject calling `kyb.Service.Review`; rejection needs a valid failure reason.
   - New RBAC permission (suggest `verification:review`; see `internal/rbac/matrix.go` and
     `db/migrations/global_registry/000004_rbac_role_permissions.sql` for how permissions are seeded).
   - Admin audit entry via `AdminHandler.audit`. Routes go in `AdminHandler.RegisterProtectedRoutes`.
   - Note: `Service.Review` runs against one region's Store; build it per request from the queue item's region.
2. **Emails and worker jobs.**
   - Templates for `kyb_failed` (names the specific reason), `kyb_review_queued`, `kyb_nudge_1` (+2 days),
     `kyb_nudge_2` (+7 days, mentions support). See `internal/mailer` (`selectTemplate`) and `internal/worker`.
   - A `kyb.Notifier` implementation using the mailer/queue; set it on `KYBHandler.Notifier` and the admin service.
   - Scheduled job using `kyb.NextNotice` and `kyb.DeletionDue`; increment `notices_sent` after each nudge.
     The 30-day case only flags the account for a reviewed deletion job; it must not delete.
3. **Ownership transfer re-verification.** When the owner changes, the new owner's organization must be verified
   again (status back to `not_started`/`pending`, event recorded). Ownership-transfer code is not in this repo yet;
   build the function and tests and note the hook point.
4. **Publish gate.** One function, e.g. `kyb.RequirePublish(status)`, returning an error unless `verified`
   (`Status.CanPublish` exists). The job-creation code is not in this repo yet; build and test the check and
   leave the hook for the job-publish handler. Drafting must never be blocked.
5. **Wire `kyb.FormatChecker`** to `onboarding.ValidateIdentification` (`internal/onboarding/identification_requirements.go`)
   and set it on `KYBHandler.Formats`, so a number in the wrong format for the chosen country fails as `wrong_country`.
6. **Verify Companies House `activeStatuses`** in `companieshouse.go` against the current API documentation.
7. **Final verification**: whole-repo `go test -race ./...`, lint, `make security` (gosec, govulncheck).

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
