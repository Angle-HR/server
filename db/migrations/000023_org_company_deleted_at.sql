-- Org: soft delete of a company (AdminOrgService.DeleteCompany). A deleted company stops resolving, listing and
-- answering readiness; the row is kept so the deletion stays idempotent and auditable. Its data in other services is
-- removed by their consumers of org.company.deleted.v1.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.organizations ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE accounts.organizations DROP COLUMN IF EXISTS deleted_at;
-- +goose StatementEnd
