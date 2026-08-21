-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.users
    ADD COLUMN IF NOT EXISTS totp_secret TEXT,
    ADD COLUMN IF NOT EXISTS totp_enabled_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS accounts.organization_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES accounts.users (id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT accounts_organization_members_role_check
        CHECK (role IN ('owner', 'member')),
    CONSTRAINT accounts_organization_members_unique UNIQUE (organization_id, user_id)
);

CREATE INDEX IF NOT EXISTS accounts_organization_members_user_idx
    ON accounts.organization_members (user_id);

CREATE TRIGGER accounts_organization_members_set_updated_at
    BEFORE UPDATE ON accounts.organization_members
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS accounts.organization_invites (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    invited_by UUID NOT NULL REFERENCES accounts.users (id),
    expires_at TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS accounts_organization_invites_org_idx
    ON accounts.organization_invites (organization_id)
    WHERE accepted_at IS NULL;

CREATE INDEX IF NOT EXISTS accounts_organization_invites_email_idx
    ON accounts.organization_invites (email)
    WHERE accepted_at IS NULL;

CREATE TRIGGER accounts_organization_invites_set_updated_at
    BEFORE UPDATE ON accounts.organization_invites
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS accounts_organization_invites_set_updated_at ON accounts.organization_invites;
DROP TABLE IF EXISTS accounts.organization_invites;
DROP TRIGGER IF EXISTS accounts_organization_members_set_updated_at ON accounts.organization_members;
DROP TABLE IF EXISTS accounts.organization_members;
ALTER TABLE accounts.users
    DROP COLUMN IF EXISTS totp_enabled_at,
    DROP COLUMN IF EXISTS totp_secret;
-- +goose StatementEnd
