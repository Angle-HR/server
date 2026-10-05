-- Global hiring registry, markets, compliance gate catalog, catalogs and RBAC role matrix.
-- Holds no personal data: only ids, regions and reference data.

-- +goose Up
-- +goose StatementBegin
CREATE SCHEMA IF NOT EXISTS hiring;
CREATE SCHEMA IF NOT EXISTS rbac;

CREATE TABLE hiring.job_registry (
    public_id TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    organization_id UUID NOT NULL,
    status TEXT NOT NULL,
    published_at TIMESTAMPTZ,
    valid_through DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT job_registry_region_check CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia')),
    CONSTRAINT job_registry_status_check
        CHECK (status IN ('published', 'paused', 'closed', 'archived', 'expired'))
);

CREATE INDEX job_registry_open_idx ON hiring.job_registry (published_at) WHERE status = 'published';

CREATE TRIGGER hiring_job_registry_set_updated_at
    BEFORE UPDATE ON hiring.job_registry
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE hiring.application_index (
    application_id UUID PRIMARY KEY,
    job_public_id TEXT NOT NULL REFERENCES hiring.job_registry (public_id),
    region TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'received',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT application_index_region_check CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'))
);

CREATE INDEX application_index_job_idx ON hiring.application_index (job_public_id, region);

CREATE TABLE hiring.markets (
    code TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    storage_region TEXT,
    storage_bucket_override TEXT,
    is_open BOOLEAN NOT NULL DEFAULT FALSE,
    min_retention_months SMALLINT,
    hide_sexual_orientation BOOLEAN NOT NULL DEFAULT FALSE,
    selection_warnings JSONB NOT NULL DEFAULT '[]'::jsonb,
    sort_order INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT markets_region_check CHECK (storage_region IS NULL OR storage_region IN ('uk', 'us', 'africa', 'eu', 'asia')),
    CONSTRAINT markets_open_needs_region_check CHECK (NOT is_open OR storage_region IS NOT NULL),
    CONSTRAINT markets_min_retention_check CHECK (min_retention_months IS NULL OR min_retention_months IN (3, 6, 12))
);

-- Closed until Open HR's own prerequisites (hiring.platform_prerequisites) are done.
-- NG, KE and ZA have no storage region: the residency decision (BE-01a) is still open, so they cannot open.
INSERT INTO hiring.markets (code, name, storage_region, is_open, min_retention_months, hide_sexual_orientation, sort_order) VALUES
    ('UK', 'United Kingdom', 'uk',     TRUE,  NULL, FALSE, 1),
    ('US', 'United States',  'us',     TRUE,  NULL, FALSE, 2),
    ('EU', 'European Union', 'eu',     FALSE, NULL, FALSE, 3),
    ('NG', 'Nigeria',        NULL, FALSE, NULL, TRUE,  4),
    ('KE', 'Kenya',          NULL, FALSE, 12,   TRUE,  5),
    ('SG', 'Singapore',      'asia',   FALSE, NULL, FALSE, 6),
    ('IN', 'India',          'asia',   FALSE, NULL, FALSE, 7),
    ('AE', 'United Arab Emirates', 'asia', FALSE, NULL, TRUE, 8),
    ('PH', 'Philippines',    'asia',   FALSE, NULL, FALSE, 9),
    ('ZA', 'South Africa',   NULL, FALSE, NULL, FALSE, 10);

CREATE TABLE hiring.compliance_gates (
    id TEXT PRIMARY KEY,
    market_code TEXT REFERENCES hiring.markets (code),
    platform BOOLEAN NOT NULL DEFAULT FALSE,
    severity TEXT NOT NULL,
    requirement TEXT NOT NULL,
    legal_basis TEXT,
    trigger JSONB NOT NULL DEFAULT '{"type":"always"}'::jsonb,
    version INTEGER NOT NULL DEFAULT 1,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT compliance_gates_severity_check CHECK (severity IN ('required', 'advisory', 'informational')),
    CONSTRAINT compliance_gates_scope_check CHECK ((market_code IS NOT NULL) <> platform)
);

-- Seeded from the plan's US gate set and the pay transparency / Kenya retention decisions.
-- Gate wording is a starting point for counsel review. The remaining PRD section 4 gates
-- (uk-lawful, uk-scd, eu-rep and so on) still need to be added as a follow-up migration.
INSERT INTO hiring.compliance_gates (id, market_code, severity, requirement, legal_basis, trigger) VALUES
    ('eu-pay-trans', 'EU', 'required', 'Pay range is shown in the posting.', 'EU Pay Transparency Directive', '{"type":"always"}'),
    ('ke-retention', 'KE', 'required', 'Rejected applicant data is kept for 12 months.', 'Kenya Data Protection Act s.50', '{"type":"always"}'),
    ('us-pay-trans', 'US', 'required', 'Pay range is shown in the posting.', 'State and city pay transparency laws', '{"type":"subdivision_in","values":["CA","CO","IL","NY","WA","NYC"]}'),
    ('us-salary-history', 'US', 'required', 'No questions about current or past pay.', 'State and city salary history bans', '{"type":"always"}'),
    ('us-ccpa', 'US', 'required', 'Applicant notice at collection is provided.', 'CCPA as amended by CPRA', '{"type":"subdivision_in","values":["CA"]}'),
    ('us-fair-chance', 'US', 'required', 'No criminal history questions before a conditional offer.', 'Fair chance laws', '{"type":"special_category","value":"criminal_records","subdivision_in":["CA"]}'),
    ('us-eeo', 'US', 'advisory', 'Self-ID questions are voluntary, stored apart from the application and hidden from reviewers.', 'EEO reporting', '{"type":"special_category_any","values":["race_ethnicity","health_disability"]}'),
    ('us-aedt', 'US', 'advisory', 'Automated screening for NYC jobs needs a bias audit and candidate notice.', 'NYC Local Law 144', '{"type":"automated_screening","subdivision_in":["NYC"]}');

CREATE TABLE hiring.platform_prerequisites (
    market_code TEXT NOT NULL REFERENCES hiring.markets (code),
    key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    reference_number TEXT,
    confirmed_at TIMESTAMPTZ,
    PRIMARY KEY (market_code, key),
    CONSTRAINT platform_prerequisites_status_check CHECK (status IN ('pending', 'in_progress', 'done'))
);

INSERT INTO hiring.platform_prerequisites (market_code, key) VALUES
    ('EU', 'art27_representative'),
    ('NG', 'ndpc_registration'),
    ('NG', 'legal_consultant'),
    ('KE', 'odpc_registration'),
    ('KE', 'legal_consultant'),
    ('SG', 'dpo_appointed'),
    ('IN', 'dpdp_readiness'),
    ('AE', 'regime_review');

CREATE TABLE hiring.seniority_levels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE
);

INSERT INTO hiring.seniority_levels (slug, name, sort_order) VALUES
    ('entry', 'Entry', 1), ('mid', 'Mid', 2), ('senior', 'Senior', 3), ('lead', 'Lead', 4), ('director', 'Director', 5);

CREATE TABLE hiring.experience_ranges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    min_years SMALLINT NOT NULL,
    max_years SMALLINT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE
);

INSERT INTO hiring.experience_ranges (slug, name, min_years, max_years, sort_order) VALUES
    ('0-1', '0 to 1 years', 0, 1, 1), ('1-3', '1 to 3 years', 1, 3, 2), ('3-5', '3 to 5 years', 3, 5, 3),
    ('5-10', '5 to 10 years', 5, 10, 4), ('10-plus', '10+ years', 10, NULL, 5);

CREATE TABLE hiring.skills (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE INDEX skills_name_idx ON hiring.skills (lower(name) text_pattern_ops);

-- Default role -> permission matrix (see the Roles and permissions doc). Seeded separately.
CREATE TABLE rbac.roles (
    slug TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

INSERT INTO rbac.roles (slug, name) VALUES
    ('founder', 'Founder'), ('hr_1', 'HR 1'), ('hr_2', 'HR 2'),
    ('line_manager', 'Line Manager'), ('payroll', 'Payroll'), ('employee', 'Employee'), ('legal', 'Legal');

CREATE TABLE rbac.role_permissions (
    role TEXT NOT NULL REFERENCES rbac.roles (slug) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    PRIMARY KEY (role, permission)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP SCHEMA IF EXISTS rbac CASCADE;
DROP SCHEMA IF EXISTS hiring CASCADE;
-- +goose StatementEnd
