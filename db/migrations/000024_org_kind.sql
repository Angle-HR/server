-- Org: personal organizations. An individual account that finishes onboarding gets an implicit organization of kind
-- 'individual' (see docs/microservices/individual-accounts.md). Every existing row is a business.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.organizations
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'business';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'accounts.organizations'::regclass AND conname = 'accounts_organizations_kind_check'
    ) THEN
        ALTER TABLE accounts.organizations
            ADD CONSTRAINT accounts_organizations_kind_check CHECK (kind IN ('business', 'individual'));
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE accounts.organizations
    DROP CONSTRAINT IF EXISTS accounts_organizations_kind_check,
    DROP COLUMN IF EXISTS kind;
-- +goose StatementEnd
