# OpenHR Phase 2 handoff: data protection, gates and publish

Branch: create it yourself from `staging`, then commit. No logs. Do not push.

## What was built

- **Publish and gates.** `internal/hiring/gates` is the compliance gate engine. `internal/hiring/draft/publish.go` holds the publish checks, status transitions and compliance log. Publish checks cover KYB verified, DPA accepted, privacy contact set, required details, open market, lawful basis (plus LIA for legitimate interests), retention minimum, special-category declarations, automated-screening prerequisites, and screening-rule validity. Gate confirmations go stale when the gate version changes. The form is frozen into `form_versions` on publish, and the job is registered in the global registry.
- **Lifecycle and bulk.** `internal/hiring/lifecycle` covers submit, withdraw, publish, pause, resume, close, reopen, archive and to-draft. `Expire` is system-only. Bulk actions are pause, close, archive and to-draft, capped at 100 jobs. Export is CSV (capped at 5000 rows) with a formula-injection guard.
- **Permissions step.** Job members (hiring team) with full or limited changes. HR 2 can only add viewers on its own jobs. Members gain visibility only. The rbac renames are in `000008_rbac_phase2_permissions.sql`.
- **Disqualification rules.** `internal/hiring/screening`. Rules flag applicants but never reject them. Rules need screening enabled, and editing them clears the DPIA scope confirmation.
- **Company setup.** DPA acceptance, privacy contact, DPIA record.

## Routes (all under the authenticated hiring group)

- `POST /jobs/bulk`
- `GET /jobs/export`
- `GET /jobs/{id}/publish-check`
- `POST /jobs/{id}/publish`
- `POST /jobs/{id}/pause`, `resume`, `close`, `reopen`, `archive`, `to-draft`, `withdraw`
- `POST /jobs/{id}/duplicate`
- `GET /jobs/{id}/compliance`
- `PUT /jobs/{id}/compliance/{gate_id}`
- `GET` and `PUT /jobs/{id}/members`
- `GET` and `PUT /jobs/{id}/disqualification-rules`
- `GET` and `POST /organization/agreements`
- `PUT /organization/privacy-contact`
- `PUT /organization/dpia`

Blocked publishes return HTTP 422, code `PUBLISH_BLOCKED`, with `blocking`, `warnings` and `gates` in the error details. Bad transitions return 409.

## Verified here, and what is not

Verified in a scratch module with real pgx and chi: `go vet` and `go test` pass for `./internal/hiring/...` and `./internal/rbac`. That includes the new HTTP tests (`publish_test.go`) and the test that checks the Go permission matrix against the SQL seeds.

Not verified:
- Migrations `000011` (regional) and global `000008`, and the SQL in `phase2.go`, have not been run against Postgres.
- `make lint`, `make swagger` and `internal/app` compile, because the sandbox could not fetch go-redis dependencies.

**On your machine:**
1. Run `make swagger`.
2. Run `go vet ./... && make lint && go test -race ./...`.
3. Apply regional migration 000011 and global_registry 000008.

I did not touch your `go.mod` or `go.sum`.

## Fixed along the way

Phase 1 bug: saving the form never advanced the step or revision in the database. It is fixed in `phase2.go` (`progressSQL`).

## Decisions I need from you

1. HR 2 lost `job.collaborator.add` and has `job.collaborator.add_limited` instead. Confirm this is intended.
2. Disqualification rules overlap with per-question knockouts. They are flag-only until counsel reviews them.
3. `CurrentDPAVersion = "2026-10-draft"` is a placeholder.
4. `memberRetentionMonths = 36` is a placeholder.
5. The job is registered in the global registry before the regional commit. A failed commit can leave an orphan registry row, which a later publish overwrites safely.

## Deferred

- Bulk undo
- Re-running gates when a published job is edited
- Wording-check list
- Public `/j/{public_id}` page
- Share Kit
- Sitemap
- Auto-close and expire task
- Jobs-list counts
- Export formats beyond CSV
- Member invite, role and ownership endpoints
