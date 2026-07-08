CREATE SCHEMA IF NOT EXISTS accounts;

CREATE TABLE accounts.users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    email_verified_at TIMESTAMPTZ,
    onboarding_completed_at TIMESTAMPTZ,
    account_type TEXT,
    first_name TEXT,
    last_name TEXT,
    legal_full_name TEXT,
    country_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT accounts_users_account_type_check
        CHECK (account_type IS NULL OR account_type IN ('individual', 'business'))
);

CREATE INDEX accounts_users_email_idx ON accounts.users (email)
    WHERE deleted_at IS NULL;

CREATE TRIGGER accounts_users_set_updated_at
    BEFORE UPDATE ON accounts.users
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE accounts.organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id UUID NOT NULL REFERENCES accounts.users (id),
    legal_name TEXT NOT NULL,
    company_role_id UUID NOT NULL,
    business_type_id UUID,
    industry_id UUID,
    employee_count INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT accounts_organizations_employee_count_check
        CHECK (employee_count IS NULL OR (employee_count >= 1 AND employee_count <= 10000))
);

CREATE UNIQUE INDEX accounts_organizations_owner_idx ON accounts.organizations (owner_user_id);

CREATE TRIGGER accounts_organizations_set_updated_at
    BEFORE UPDATE ON accounts.organizations
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE accounts.addresses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES accounts.users (id),
    organization_id UUID REFERENCES accounts.organizations (id),
    country_id UUID NOT NULL,
    entry_mode TEXT NOT NULL,
    line_1 TEXT NOT NULL,
    line_2 TEXT,
    city TEXT NOT NULL,
    state_or_county TEXT NOT NULL,
    post_code TEXT NOT NULL,
    formatted_address TEXT,
    verification_status TEXT NOT NULL DEFAULT 'unverified',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT accounts_addresses_entry_mode_check
        CHECK (entry_mode IN ('search', 'manual')),
    CONSTRAINT accounts_addresses_verification_status_check
        CHECK (verification_status IN ('unverified', 'verified', 'failed'))
);

CREATE UNIQUE INDEX accounts_addresses_user_idx ON accounts.addresses (user_id);

CREATE TRIGGER accounts_addresses_set_updated_at
    BEFORE UPDATE ON accounts.addresses
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE accounts.onboarding_progress (
    user_id UUID PRIMARY KEY REFERENCES accounts.users (id),
    current_step TEXT NOT NULL,
    completed_steps TEXT[] NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER accounts_onboarding_progress_set_updated_at
    BEFORE UPDATE ON accounts.onboarding_progress
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
