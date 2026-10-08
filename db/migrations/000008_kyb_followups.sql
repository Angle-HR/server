-- Company verification (KYB): persist the submitted address (reused by the light retry form)
-- and the number of re-engagement nudges already sent for the current failure.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.organization_verifications
    ADD COLUMN IF NOT EXISTS submitted_address JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS notices_sent SMALLINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE accounts.organization_verifications
    DROP COLUMN IF EXISTS notices_sent,
    DROP COLUMN IF EXISTS submitted_address;
-- +goose StatementEnd
