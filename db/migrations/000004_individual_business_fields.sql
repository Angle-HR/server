-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.users
    ADD COLUMN IF NOT EXISTS business_type_id UUID,
    ADD COLUMN IF NOT EXISTS industry_id      UUID,
    ADD COLUMN IF NOT EXISTS employee_count   INTEGER;

ALTER TABLE accounts.users
    ADD CONSTRAINT accounts_users_employee_count_check
        CHECK (employee_count IS NULL OR (employee_count >= 1 AND employee_count <= 10000));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE accounts.users
    DROP CONSTRAINT IF EXISTS accounts_users_employee_count_check,
    DROP COLUMN IF EXISTS business_type_id,
    DROP COLUMN IF EXISTS industry_id,
    DROP COLUMN IF EXISTS employee_count;
-- +goose StatementEnd
