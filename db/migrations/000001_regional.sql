-- Regional schema for anglehr_uk, anglehr_us, anglehr_africa, anglehr_eu, and anglehr_asia.
-- migrate.sh creates the target database from the bootstrap block below before applying goose migrations.

-- @bootstrap-databases
-- CREATE DATABASE anglehr_uk OWNER anglehr;
-- CREATE DATABASE anglehr_us OWNER anglehr;
-- CREATE DATABASE anglehr_africa OWNER anglehr;
-- CREATE DATABASE anglehr_eu OWNER anglehr;
-- CREATE DATABASE anglehr_asia OWNER anglehr;
-- @bootstrap-databases-end

-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION soft_delete_row()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.deleted_at IS NULL THEN
        EXECUTE format(
            'UPDATE %I.%I SET deleted_at = now(), updated_at = now() WHERE id = $1',
            TG_TABLE_SCHEMA,
            TG_TABLE_NAME
        ) USING OLD.id;
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE SCHEMA IF NOT EXISTS waitlist;

CREATE TABLE waitlist.waitlist (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    full_name TEXT,
    email TEXT NOT NULL UNIQUE,
    country_id UUID,
    region TEXT NOT NULL,
    region_source TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    onboarding_submitted_at TIMESTAMPTZ,
    wants_early_access BOOLEAN,
    wants_user_testing BOOLEAN,
    role_id UUID,
    team_size_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT waitlist_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'))
);

CREATE INDEX waitlist_country_id_idx ON waitlist.waitlist (country_id);
CREATE INDEX waitlist_region_idx ON waitlist.waitlist (region);

CREATE TRIGGER waitlist_set_updated_at
    BEFORE UPDATE ON waitlist.waitlist
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER waitlist_soft_delete
    BEFORE DELETE ON waitlist.waitlist
    FOR EACH ROW
    EXECUTE FUNCTION soft_delete_row();

CREATE TABLE waitlist.waitlist_industries (
    waitlist_id BIGINT NOT NULL REFERENCES waitlist.waitlist (id) ON DELETE CASCADE,
    industry_id UUID NOT NULL,
    other_text TEXT,
    PRIMARY KEY (waitlist_id, industry_id)
);

CREATE INDEX waitlist_industries_waitlist_id_idx ON waitlist.waitlist_industries (waitlist_id);

CREATE TABLE waitlist.waitlist_hiring_tools (
    waitlist_id BIGINT NOT NULL REFERENCES waitlist.waitlist (id) ON DELETE CASCADE,
    hiring_tool_id UUID NOT NULL,
    other_text TEXT,
    PRIMARY KEY (waitlist_id, hiring_tool_id)
);

CREATE INDEX waitlist_hiring_tools_waitlist_id_idx ON waitlist.waitlist_hiring_tools (waitlist_id);

CREATE TABLE waitlist.waitlist_frustrations (
    waitlist_id BIGINT NOT NULL REFERENCES waitlist.waitlist (id) ON DELETE CASCADE,
    frustration_id UUID NOT NULL,
    other_text TEXT,
    PRIMARY KEY (waitlist_id, frustration_id)
);

CREATE INDEX waitlist_frustrations_waitlist_id_idx ON waitlist.waitlist_frustrations (waitlist_id);

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
    business_type_id UUID,
    industry_id UUID,
    employee_count INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT accounts_users_account_type_check
        CHECK (account_type IS NULL OR account_type IN ('individual', 'business')),
    CONSTRAINT accounts_users_employee_count_check
        CHECK (employee_count IS NULL OR (employee_count >= 1 AND employee_count <= 10000))
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
    country_id UUID,
    bin_number TEXT,
    business_registered_address TEXT,
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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS accounts_onboarding_progress_set_updated_at ON accounts.onboarding_progress;
DROP TABLE IF EXISTS accounts.onboarding_progress;
DROP TRIGGER IF EXISTS accounts_addresses_set_updated_at ON accounts.addresses;
DROP TABLE IF EXISTS accounts.addresses;
DROP TRIGGER IF EXISTS accounts_organizations_set_updated_at ON accounts.organizations;
DROP TABLE IF EXISTS accounts.organizations;
DROP TRIGGER IF EXISTS accounts_users_set_updated_at ON accounts.users;
DROP TABLE IF EXISTS accounts.users;
DROP SCHEMA IF EXISTS accounts;

DROP TRIGGER IF EXISTS waitlist_soft_delete ON waitlist.waitlist;
DROP TRIGGER IF EXISTS waitlist_set_updated_at ON waitlist.waitlist;
DROP TABLE IF EXISTS waitlist.waitlist_frustrations;
DROP TABLE IF EXISTS waitlist.waitlist_hiring_tools;
DROP TABLE IF EXISTS waitlist.waitlist_industries;
DROP TABLE IF EXISTS waitlist.waitlist;
DROP SCHEMA IF EXISTS waitlist;

DROP FUNCTION IF EXISTS soft_delete_row();
DROP FUNCTION IF EXISTS set_updated_at();
DROP EXTENSION IF EXISTS pgcrypto;
-- +goose StatementEnd
