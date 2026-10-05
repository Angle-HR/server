-- Regional hiring schema (job creation, application form builder, compliance state).
-- Applied to each regional database. Global catalogs live in global_registry/000003_hiring.sql.

-- +goose Up
-- +goose StatementBegin
CREATE SCHEMA IF NOT EXISTS hiring;

-- Accounts additions: terms/DPA acceptance and multi-role membership.
CREATE TABLE IF NOT EXISTS accounts.organization_agreements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    version TEXT NOT NULL,
    accepted_by UUID NOT NULL REFERENCES accounts.users (id),
    accepted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT organization_agreements_kind_check CHECK (kind IN ('terms_dpa')),
    CONSTRAINT organization_agreements_unique UNIQUE (organization_id, kind, version)
);

CREATE TABLE IF NOT EXISTS accounts.organization_member_roles (
    member_id UUID NOT NULL REFERENCES accounts.organization_members (id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    assigned_by UUID REFERENCES accounts.users (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (member_id, role),
    CONSTRAINT organization_member_roles_role_check
        CHECK (role IN ('founder', 'hr_1', 'hr_2', 'line_manager', 'payroll', 'employee', 'legal'))
);

CREATE TABLE hiring.org_counters (
    tenant_id UUID PRIMARY KEY REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    next_job_number INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE hiring.departments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT hiring_departments_unique UNIQUE (tenant_id, name)
);

CREATE TABLE hiring.job_postings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES accounts.users (id),
    job_number INTEGER NOT NULL,
    public_id TEXT UNIQUE,
    status TEXT NOT NULL DEFAULT 'draft',
    current_step TEXT NOT NULL DEFAULT 'details',
    completed_sections TEXT[] NOT NULL DEFAULT '{}',
    revision INTEGER NOT NULL DEFAULT 1,
    title TEXT,
    department_id UUID REFERENCES hiring.departments (id),
    closing_date DATE,
    location_mode TEXT,
    location_text TEXT,
    use_company_address BOOLEAN NOT NULL DEFAULT FALSE,
    workplace_type TEXT,
    travel_frequency TEXT,
    visa_sponsorship TEXT,
    description_html TEXT,
    description_text TEXT,
    description_sections JSONB NOT NULL DEFAULT '{}'::jsonb,
    industry_id UUID,
    custom_industry TEXT,
    employment_type TEXT,
    seniority_level_id UUID,
    experience_range_id UUID,
    pay_type TEXT,
    pay_min BIGINT,
    pay_max BIGINT,
    pay_currency TEXT,
    pay_period TEXT,
    pay_visible BOOLEAN NOT NULL DEFAULT TRUE,
    show_on_career_page BOOLEAN NOT NULL DEFAULT TRUE,
    lawful_basis TEXT,
    lia_reference TEXT,
    retention_months SMALLINT NOT NULL DEFAULT 6,
    assessment_url TEXT,
    published_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT job_postings_org_number_unique UNIQUE (tenant_id, job_number),
    CONSTRAINT job_postings_status_check
        CHECK (status IN ('draft', 'pending_approval', 'published', 'paused', 'closed', 'archived', 'expired')),
    CONSTRAINT job_postings_location_mode_check
        CHECK (location_mode IS NULL OR location_mode IN ('anywhere', 'specific_area', 'specific_timezone')),
    CONSTRAINT job_postings_workplace_type_check
        CHECK (workplace_type IS NULL OR workplace_type IN ('onsite', 'hybrid', 'remote')),
    CONSTRAINT job_postings_employment_type_check
        CHECK (employment_type IS NULL OR employment_type IN ('full_time', 'part_time', 'contract', 'internship')),
    CONSTRAINT job_postings_pay_type_check
        CHECK (pay_type IS NULL OR pay_type IN ('exact', 'range')),
    CONSTRAINT job_postings_pay_period_check
        CHECK (pay_period IS NULL OR pay_period IN ('hour', 'month', 'year')),
    CONSTRAINT job_postings_pay_currency_check
        CHECK (pay_currency IS NULL OR pay_currency IN ('GBP', 'EUR', 'USD', 'NGN', 'KES', 'SGD', 'INR', 'PHP', 'AED')),
    CONSTRAINT job_postings_pay_range_check
        CHECK (pay_min IS NULL OR pay_max IS NULL OR pay_min <= pay_max),
    CONSTRAINT job_postings_lawful_basis_check
        CHECK (lawful_basis IS NULL OR lawful_basis IN ('contract', 'legitimate_interests', 'consent', 'legal_obligation')),
    CONSTRAINT job_postings_retention_check CHECK (retention_months IN (3, 6, 12)),
    CONSTRAINT job_postings_title_length_check CHECK (title IS NULL OR char_length(title) <= 70)
);

CREATE INDEX job_postings_org_status_idx ON hiring.job_postings (tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX job_postings_search_idx ON hiring.job_postings USING GIN (
    (setweight(to_tsvector('simple', coalesce(title, '')), 'A'))
);

CREATE TRIGGER hiring_job_postings_set_updated_at
    BEFORE UPDATE ON hiring.job_postings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE hiring.job_markets (
    job_id UUID NOT NULL REFERENCES hiring.job_postings (id) ON DELETE CASCADE,
    market_code TEXT NOT NULL,
    subdivision TEXT NOT NULL DEFAULT '',
    city TEXT NOT NULL DEFAULT '',
    timezone TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (job_id, market_code, subdivision, city, timezone)
);

CREATE TABLE hiring.job_skills (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id UUID NOT NULL REFERENCES hiring.job_postings (id) ON DELETE CASCADE,
    skill_id UUID,
    custom_label TEXT,
    CONSTRAINT job_skills_one_source_check CHECK ((skill_id IS NULL) <> (custom_label IS NULL))
);

CREATE TABLE hiring.application_forms (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id UUID NOT NULL UNIQUE REFERENCES hiring.job_postings (id) ON DELETE CASCADE,
    revision INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE hiring.form_questions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    form_id UUID NOT NULL REFERENCES hiring.application_forms (id) ON DELETE CASCADE,
    section TEXT NOT NULL,
    position INTEGER NOT NULL,
    type TEXT NOT NULL,
    label TEXT NOT NULL,
    description TEXT,
    helper_text TEXT,
    required BOOLEAN NOT NULL DEFAULT FALSE,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    system_key TEXT,
    locked BOOLEAN NOT NULL DEFAULT FALSE,
    knockout JSONB,
    special_category TEXT,
    override_reason TEXT,
    override_by UUID REFERENCES accounts.users (id),
    override_at TIMESTAMPTZ,
    CONSTRAINT form_questions_section_check
        CHECK (section IN ('personal_information', 'profile', 'eligibility', 'screening')),
    CONSTRAINT form_questions_type_check CHECK (type IN (
        'short_text', 'long_text', 'number', 'email', 'phone_number',
        'single_choice', 'multiple_choice', 'checkbox', 'dropdown', 'linear_scale',
        'image_upload', 'file_upload', 'link', 'date', 'time', 'autofill_resume')),
    CONSTRAINT form_questions_system_key_check CHECK (system_key IS NULL OR system_key IN
        ('full_name', 'email', 'location', 'cv', 'autofill_resume')),
    CONSTRAINT form_questions_special_category_check CHECK (special_category IS NULL OR special_category IN
        ('health_disability', 'race_ethnicity', 'religion_belief', 'biometric', 'criminal_records'))
);

CREATE INDEX form_questions_form_idx ON hiring.form_questions (form_id, position);
CREATE UNIQUE INDEX form_questions_system_key_idx ON hiring.form_questions (form_id, system_key) WHERE system_key IS NOT NULL;

CREATE TABLE hiring.form_versions (
    form_id UUID NOT NULL REFERENCES hiring.application_forms (id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    schema JSONB NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (form_id, version)
);

CREATE TABLE hiring.special_category_declarations (
    job_id UUID NOT NULL REFERENCES hiring.job_postings (id) ON DELETE CASCADE,
    category TEXT NOT NULL,
    legal_condition TEXT NOT NULL,
    purpose TEXT NOT NULL,
    declared_by UUID NOT NULL REFERENCES accounts.users (id),
    declared_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, category),
    CONSTRAINT special_category_declarations_category_check CHECK (category IN
        ('health_disability', 'race_ethnicity', 'religion_belief', 'biometric', 'criminal_records'))
);

CREATE TABLE hiring.job_members (
    job_id UUID NOT NULL REFERENCES hiring.job_postings (id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES accounts.users (id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    PRIMARY KEY (job_id, user_id),
    CONSTRAINT job_members_role_check CHECK (role IN ('hiring_manager', 'recruiter', 'interviewer', 'viewer'))
);

CREATE TABLE hiring.templates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    payload JSONB NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT templates_kind_check CHECK (kind IN ('job_details', 'application_form'))
);

CREATE UNIQUE INDEX templates_one_default_idx ON hiring.templates (tenant_id, kind) WHERE is_default;

CREATE TABLE hiring.job_channels (
    job_id UUID NOT NULL REFERENCES hiring.job_postings (id) ON DELETE CASCADE,
    channel TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    external_id TEXT,
    last_synced_at TIMESTAMPTZ,
    last_error TEXT,
    PRIMARY KEY (job_id, channel)
);

CREATE TABLE hiring.disqualification_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id UUID NOT NULL REFERENCES hiring.job_postings (id) ON DELETE CASCADE,
    field TEXT NOT NULL,
    operator TEXT NOT NULL,
    value JSONB NOT NULL,
    reason TEXT NOT NULL
);

CREATE TABLE hiring.job_gate_confirmations (
    job_id UUID NOT NULL REFERENCES hiring.job_postings (id) ON DELETE CASCADE,
    gate_id TEXT NOT NULL,
    gate_version INTEGER NOT NULL,
    confirmed BOOLEAN NOT NULL,
    confirmed_by UUID REFERENCES accounts.users (id),
    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, gate_id)
);

CREATE TABLE hiring.audit_events (
    id BIGSERIAL PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES hiring.job_postings (id) ON DELETE CASCADE,
    actor_id UUID REFERENCES accounts.users (id),
    action TEXT NOT NULL,
    diff JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_job_idx ON hiring.audit_events (job_id, created_at);

-- Append-only compliance log (PRD 3.9). No FK to job_postings so entries outlive a job.
CREATE TABLE hiring.compliance_log (
    id BIGSERIAL PRIMARY KEY,
    job_id UUID NOT NULL,
    gate_id TEXT NOT NULL,
    severity TEXT NOT NULL,
    event TEXT NOT NULL,
    actor_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT compliance_log_severity_check CHECK (severity IN ('required', 'advisory', 'informational'))
);

CREATE INDEX compliance_log_job_idx ON hiring.compliance_log (job_id, created_at);

CREATE OR REPLACE FUNCTION hiring.reject_log_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'hiring.compliance_log is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER compliance_log_append_only
    BEFORE UPDATE OR DELETE ON hiring.compliance_log
    FOR EACH ROW EXECUTE FUNCTION hiring.reject_log_mutation();

CREATE TRIGGER compliance_log_no_truncate
    BEFORE TRUNCATE ON hiring.compliance_log
    FOR EACH STATEMENT EXECUTE FUNCTION hiring.reject_log_mutation();
-- Tenant isolation (BE-25). The tenant is the organization. Child tables carry tenant_id and a composite
-- foreign key, so a child row can never point at another tenant's parent. Row-level security is keyed on the
-- session setting app.tenant_id; when it is unset no rows are visible (fail closed).
ALTER TABLE hiring.job_postings ADD CONSTRAINT job_postings_id_tenant_unique UNIQUE (id, tenant_id);
ALTER TABLE hiring.application_forms ADD COLUMN tenant_id UUID NOT NULL;
ALTER TABLE hiring.application_forms ADD CONSTRAINT application_forms_tenant_fk
    FOREIGN KEY (job_id, tenant_id) REFERENCES hiring.job_postings (id, tenant_id) ON DELETE CASCADE;
ALTER TABLE hiring.application_forms ADD CONSTRAINT application_forms_id_tenant_unique UNIQUE (id, tenant_id);

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['job_markets', 'job_skills', 'job_members', 'special_category_declarations',
        'job_channels', 'disqualification_rules', 'job_gate_confirmations', 'audit_events'] LOOP
        EXECUTE format('ALTER TABLE hiring.%I ADD COLUMN tenant_id UUID NOT NULL', t);
        EXECUTE format('ALTER TABLE hiring.%I ADD CONSTRAINT %I FOREIGN KEY (job_id, tenant_id)
            REFERENCES hiring.job_postings (id, tenant_id) ON DELETE CASCADE', t, t || '_tenant_fk');
    END LOOP;
    FOREACH t IN ARRAY ARRAY['form_questions', 'form_versions'] LOOP
        EXECUTE format('ALTER TABLE hiring.%I ADD COLUMN tenant_id UUID NOT NULL', t);
        EXECUTE format('ALTER TABLE hiring.%I ADD CONSTRAINT %I FOREIGN KEY (form_id, tenant_id)
            REFERENCES hiring.application_forms (id, tenant_id) ON DELETE CASCADE', t, t || '_tenant_fk');
    END LOOP;
END $$;

-- The compliance log has no foreign key so entries outlive a job; it still belongs to a tenant.
ALTER TABLE hiring.compliance_log ADD COLUMN tenant_id UUID NOT NULL;

-- Personal-data metadata (BE-11). Candidate and application tables added later must carry these too.
-- retention_until has no default: the value comes from the retention schedule (PL-04), not the database.
ALTER TABLE hiring.job_members
    ADD COLUMN retention_until TIMESTAMPTZ NOT NULL,
    ADD COLUMN lawful_basis TEXT NOT NULL,
    ADD COLUMN collection_purpose TEXT NOT NULL,
    ADD COLUMN allowed_downstream_purposes TEXT[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT job_members_lawful_basis_check
        CHECK (lawful_basis IN ('contract', 'legitimate_interests', 'consent', 'legal_obligation'));
ALTER TABLE hiring.special_category_declarations
    ADD COLUMN retention_until TIMESTAMPTZ NOT NULL,
    ADD COLUMN lawful_basis TEXT NOT NULL,
    ADD COLUMN collection_purpose TEXT NOT NULL,
    ADD COLUMN allowed_downstream_purposes TEXT[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT special_category_declarations_lawful_basis_check
        CHECK (lawful_basis IN ('contract', 'legitimate_interests', 'consent', 'legal_obligation'));

-- The application role must not be able to alter the log, whatever the triggers say.
REVOKE UPDATE, DELETE, TRUNCATE ON hiring.compliance_log FROM PUBLIC;

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['org_counters', 'departments', 'job_postings', 'job_markets', 'job_skills',
        'application_forms', 'form_questions', 'form_versions', 'special_category_declarations', 'job_members',
        'templates', 'job_channels', 'disqualification_rules', 'job_gate_confirmations', 'audit_events',
        'compliance_log'] LOOP
        EXECUTE format('ALTER TABLE hiring.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE hiring.%I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON hiring.%I
            USING (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)
            WITH CHECK (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)', t);
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP SCHEMA IF EXISTS hiring CASCADE;
DROP TABLE IF EXISTS accounts.organization_member_roles;
DROP TABLE IF EXISTS accounts.organization_agreements;
-- +goose StatementEnd
