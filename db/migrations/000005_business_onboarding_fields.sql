-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.organizations
    ADD COLUMN IF NOT EXISTS country_id UUID,
    ADD COLUMN IF NOT EXISTS bin_number TEXT,
    ADD COLUMN IF NOT EXISTS business_registered_address TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE accounts.organizations
    DROP COLUMN IF EXISTS country_id,
    DROP COLUMN IF EXISTS bin_number,
    DROP COLUMN IF EXISTS business_registered_address;
-- +goose StatementEnd
