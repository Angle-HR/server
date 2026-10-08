-- Job drafts (phase 1): duplicate-job keys, tighter pay and enum checks, per-user template defaults and
-- per-company hiring settings. Regional: applied to every region.

-- +goose Up
-- +goose StatementBegin

-- Comparison keys for the duplicate-job warning. Computed by the application when a job is saved.
ALTER TABLE hiring.job_postings
    ADD COLUMN title_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN location_key TEXT NOT NULL DEFAULT '';

CREATE INDEX job_postings_duplicate_idx
    ON hiring.job_postings (tenant_id, title_key, location_key)
    WHERE deleted_at IS NULL AND title_key <> '';

-- Newest first, the order of the jobs list (keyset pagination on updated_at, id).
CREATE INDEX job_postings_list_idx
    ON hiring.job_postings (tenant_id, updated_at DESC, id DESC)
    WHERE deleted_at IS NULL;

-- The launch markets use six currencies; SGD, PHP and AED left with their markets on 7 Oct.
ALTER TABLE hiring.job_postings DROP CONSTRAINT job_postings_pay_currency_check;
ALTER TABLE hiring.job_postings
    ADD CONSTRAINT job_postings_pay_currency_check
        CHECK (pay_currency IS NULL OR pay_currency IN ('GBP', 'EUR', 'USD', 'NGN', 'KES', 'INR')),
    ADD CONSTRAINT job_postings_travel_frequency_check
        CHECK (travel_frequency IS NULL OR travel_frequency IN ('none', 'occasional', 'frequent')),
    ADD CONSTRAINT job_postings_visa_sponsorship_check
        CHECK (visa_sponsorship IS NULL OR visa_sponsorship IN ('yes', 'no', 'case_by_case')),
    ADD CONSTRAINT job_postings_pay_amount_check
        CHECK ((pay_min IS NULL OR pay_min >= 0) AND (pay_max IS NULL OR pay_max >= 0));

-- A job's department must belong to the same company. The composite key stops a draft pointing at another
-- company's department; a plain foreign key would not, because foreign key checks skip row-level security.
ALTER TABLE hiring.departments ADD CONSTRAINT hiring_departments_id_tenant_unique UNIQUE (id, tenant_id);
ALTER TABLE hiring.job_postings DROP CONSTRAINT job_postings_department_id_fkey;
ALTER TABLE hiring.job_postings
    ADD CONSTRAINT job_postings_department_tenant_fk
        FOREIGN KEY (department_id, tenant_id) REFERENCES hiring.departments (id, tenant_id);

-- Skills keep the order the employer listed them in.
ALTER TABLE hiring.job_skills ADD COLUMN position SMALLINT NOT NULL DEFAULT 0;

-- Departments are unique per company regardless of case.
CREATE UNIQUE INDEX hiring_departments_name_ci_idx ON hiring.departments (tenant_id, lower(name));

-- Templates: a named template belongs to the company (user_id is null); "Keep this setup for my future
-- jobs" is a per-user default (user_id set, is_default true), at most one per user and kind.
ALTER TABLE hiring.templates
    ADD COLUMN user_id UUID REFERENCES accounts.users (id) ON DELETE CASCADE,
    ADD COLUMN created_by UUID REFERENCES accounts.users (id) ON DELETE SET NULL;
DROP INDEX hiring.templates_one_default_idx;
ALTER TABLE hiring.templates
    ADD CONSTRAINT templates_default_has_user_check CHECK (NOT is_default OR user_id IS NOT NULL);
CREATE UNIQUE INDEX templates_one_user_default_idx
    ON hiring.templates (tenant_id, user_id, kind) WHERE is_default;
CREATE UNIQUE INDEX templates_name_idx
    ON hiring.templates (tenant_id, kind, lower(name)) WHERE user_id IS NULL;

CREATE TRIGGER hiring_templates_set_updated_at
    BEFORE UPDATE ON hiring.templates
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Company-level hiring settings. Automated screening (knockout questions, auto-disqualification) is off until
-- Legal turns it on; the DPIA that must exist before a screening job publishes comes with the publish phase.
CREATE TABLE hiring.org_settings (
    tenant_id UUID PRIMARY KEY REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    automated_screening_enabled_at TIMESTAMPTZ,
    automated_screening_enabled_by UUID REFERENCES accounts.users (id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER hiring_org_settings_set_updated_at
    BEFORE UPDATE ON hiring.org_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE hiring.org_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE hiring.org_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON hiring.org_settings
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hiring.org_settings;
DROP TRIGGER IF EXISTS hiring_templates_set_updated_at ON hiring.templates;
DROP INDEX IF EXISTS hiring.templates_name_idx;
DROP INDEX IF EXISTS hiring.templates_one_user_default_idx;
ALTER TABLE hiring.templates DROP CONSTRAINT IF EXISTS templates_default_has_user_check;
ALTER TABLE hiring.templates DROP COLUMN IF EXISTS created_by, DROP COLUMN IF EXISTS user_id;
CREATE UNIQUE INDEX IF NOT EXISTS templates_one_default_idx ON hiring.templates (tenant_id, kind) WHERE is_default;
DROP INDEX IF EXISTS hiring.hiring_departments_name_ci_idx;
ALTER TABLE hiring.job_skills DROP COLUMN IF EXISTS position;
ALTER TABLE hiring.job_postings DROP CONSTRAINT IF EXISTS job_postings_department_tenant_fk;
ALTER TABLE hiring.job_postings
    ADD CONSTRAINT job_postings_department_id_fkey FOREIGN KEY (department_id) REFERENCES hiring.departments (id);
ALTER TABLE hiring.departments DROP CONSTRAINT IF EXISTS hiring_departments_id_tenant_unique;
ALTER TABLE hiring.job_postings
    DROP CONSTRAINT IF EXISTS job_postings_pay_amount_check,
    DROP CONSTRAINT IF EXISTS job_postings_visa_sponsorship_check,
    DROP CONSTRAINT IF EXISTS job_postings_travel_frequency_check,
    DROP CONSTRAINT IF EXISTS job_postings_pay_currency_check;
ALTER TABLE hiring.job_postings
    ADD CONSTRAINT job_postings_pay_currency_check
        CHECK (pay_currency IS NULL OR pay_currency IN ('GBP', 'EUR', 'USD', 'NGN', 'KES', 'SGD', 'INR', 'PHP', 'AED'));
DROP INDEX IF EXISTS hiring.job_postings_list_idx;
DROP INDEX IF EXISTS hiring.job_postings_duplicate_idx;
ALTER TABLE hiring.job_postings DROP COLUMN IF EXISTS location_key, DROP COLUMN IF EXISTS title_key;
-- +goose StatementEnd
