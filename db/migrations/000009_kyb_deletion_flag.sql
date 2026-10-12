-- Company verification (KYB): flag accounts left unresolved for 30 days for a reviewed deletion job.
-- The flag never deletes anything on its own (KYB V2 section 5.4).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.organization_verifications
    ADD COLUMN IF NOT EXISTS deletion_flagged_at TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE accounts.organization_verifications
    DROP COLUMN IF EXISTS deletion_flagged_at;
-- +goose StatementEnd
