-- Adds a global holding area for accounts that have signed up (and may be fully
-- verified) but haven't yet told us which country they're in. New signups now
-- land here instead of a real regional database; onboarding migrates them into
-- their real region's accounts.users/organizations/onboarding_progress once a
-- country is known. See internal/handler/region_pending.go.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE accounts.pending_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    email_verified_at TIMESTAMPTZ,
    account_type TEXT,
    legal_full_name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT pending_users_account_type_check
        CHECK (account_type IS NULL OR account_type IN ('individual', 'business'))
);

CREATE INDEX pending_users_email_idx ON accounts.pending_users (email)
    WHERE deleted_at IS NULL;

CREATE TRIGGER pending_users_set_updated_at
    BEFORE UPDATE ON accounts.pending_users
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- Holds the business-profile fields (legal name + role) a business account
-- gives at the "profile" onboarding step, before the later "address" step
-- supplies a country and triggers migration into a real region.
CREATE TABLE accounts.pending_organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id UUID NOT NULL REFERENCES accounts.pending_users (id),
    legal_name TEXT NOT NULL,
    company_role_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX pending_organizations_owner_idx ON accounts.pending_organizations (owner_user_id);

CREATE TRIGGER pending_organizations_set_updated_at
    BEFORE UPDATE ON accounts.pending_organizations
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE accounts.pending_onboarding_progress (
    user_id UUID PRIMARY KEY REFERENCES accounts.pending_users (id),
    current_step TEXT NOT NULL,
    completed_steps TEXT[] NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER pending_onboarding_progress_set_updated_at
    BEFORE UPDATE ON accounts.pending_onboarding_progress
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

ALTER TABLE auth.users_registry
    DROP CONSTRAINT users_registry_region_check,
    ADD CONSTRAINT users_registry_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia', 'global'));

ALTER TABLE auth.users_registry
    DROP CONSTRAINT users_registry_region_source_check,
    ADD CONSTRAINT users_registry_region_source_check
        CHECK (region_source IN ('explicit', 'inferred', 'jwt', 'subdomain', 'db', 'ip', 'pending'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE auth.users_registry
    DROP CONSTRAINT users_registry_region_source_check,
    ADD CONSTRAINT users_registry_region_source_check
        CHECK (region_source IN ('explicit', 'inferred', 'jwt', 'subdomain', 'db', 'ip'));

ALTER TABLE auth.users_registry
    DROP CONSTRAINT users_registry_region_check,
    ADD CONSTRAINT users_registry_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'));

DROP TRIGGER IF EXISTS pending_onboarding_progress_set_updated_at ON accounts.pending_onboarding_progress;
DROP TABLE IF EXISTS accounts.pending_onboarding_progress;
DROP TRIGGER IF EXISTS pending_organizations_set_updated_at ON accounts.pending_organizations;
DROP TABLE IF EXISTS accounts.pending_organizations;
DROP TRIGGER IF EXISTS pending_users_set_updated_at ON accounts.pending_users;
DROP TABLE IF EXISTS accounts.pending_users;
-- +goose StatementEnd
