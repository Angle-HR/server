-- Company verification (KYB). Regional: verification details are company data and stay in the organization's region.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.organizations
    ADD COLUMN IF NOT EXISTS kyb_status TEXT NOT NULL DEFAULT 'not_started',
    ADD COLUMN IF NOT EXISTS kyb_verified_at TIMESTAMPTZ,
    ADD CONSTRAINT accounts_organizations_kyb_status_check
        CHECK (kyb_status IN ('not_started', 'pending', 'verified', 'failed'));

CREATE TABLE accounts.organization_verifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL UNIQUE REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    country_code TEXT NOT NULL,
    registration_number TEXT NOT NULL,
    identifiers JSONB NOT NULL DEFAULT '{}'::jsonb,
    legal_name_submitted TEXT NOT NULL,
    registry_name TEXT,
    registered_address TEXT,
    address_type TEXT,
    tier SMALLINT NOT NULL,
    failure_reason TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    checked_at TIMESTAMPTZ,
    verified_at TIMESTAMPTZ,
    reviewer_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT organization_verifications_tier_check CHECK (tier IN (1, 2)),
    CONSTRAINT organization_verifications_address_type_check
        CHECK (address_type IS NULL OR address_type IN ('registered', 'trading')),
    CONSTRAINT organization_verifications_failure_reason_check CHECK (failure_reason IS NULL OR failure_reason IN (
        'number_not_found', 'name_mismatch', 'address_mismatch', 'inactive_entity', 'wrong_country'))
);

CREATE TRIGGER organization_verifications_set_updated_at
    BEFORE UPDATE ON accounts.organization_verifications
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Append-only history of every check, decision and status change.
CREATE TABLE accounts.organization_verification_events (
    id BIGSERIAL PRIMARY KEY,
    organization_id UUID NOT NULL,
    event TEXT NOT NULL,
    status TEXT NOT NULL,
    failure_reason TEXT,
    actor_id UUID,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT organization_verification_events_status_check
        CHECK (status IN ('not_started', 'pending', 'verified', 'failed'))
);

CREATE INDEX organization_verification_events_org_idx
    ON accounts.organization_verification_events (organization_id, created_at);

CREATE OR REPLACE FUNCTION accounts.reject_verification_event_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'accounts.organization_verification_events is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER organization_verification_events_append_only
    BEFORE UPDATE OR DELETE ON accounts.organization_verification_events
    FOR EACH ROW EXECUTE FUNCTION accounts.reject_verification_event_mutation();

CREATE TRIGGER organization_verification_events_no_truncate
    BEFORE TRUNCATE ON accounts.organization_verification_events
    FOR EACH STATEMENT EXECUTE FUNCTION accounts.reject_verification_event_mutation();

REVOKE UPDATE, DELETE, TRUNCATE ON accounts.organization_verification_events FROM PUBLIC;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS accounts.organization_verification_events;
DROP FUNCTION IF EXISTS accounts.reject_verification_event_mutation();
DROP TABLE IF EXISTS accounts.organization_verifications;
ALTER TABLE accounts.organizations
    DROP CONSTRAINT IF EXISTS accounts_organizations_kyb_status_check,
    DROP COLUMN IF EXISTS kyb_verified_at,
    DROP COLUMN IF EXISTS kyb_status;
-- +goose StatementEnd
