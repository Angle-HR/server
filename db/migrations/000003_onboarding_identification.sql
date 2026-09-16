-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.organizations
    ADD COLUMN IF NOT EXISTS identification_payload JSONB;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE accounts.organizations
    DROP COLUMN IF EXISTS identification_payload;
-- +goose StatementEnd
