-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.users
    ADD COLUMN IF NOT EXISTS business_type_id UUID,
    ADD COLUMN IF NOT EXISTS industry_id      UUID,
    ADD COLUMN IF NOT EXISTS employee_count   INTEGER;

-- Fresh databases already have this constraint from 000001_regional.sql.
-- Older databases still need it when adding the individual business fields.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'accounts.users'::regclass
          AND conname = 'accounts_users_employee_count_check'
    ) THEN
        ALTER TABLE accounts.users
            ADD CONSTRAINT accounts_users_employee_count_check
                CHECK (employee_count IS NULL OR (employee_count >= 1 AND employee_count <= 10000));
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE accounts.users
    DROP CONSTRAINT IF EXISTS accounts_users_employee_count_check,
    DROP COLUMN IF EXISTS business_type_id,
    DROP COLUMN IF EXISTS industry_id,
    DROP COLUMN IF EXISTS employee_count;
-- +goose StatementEnd
