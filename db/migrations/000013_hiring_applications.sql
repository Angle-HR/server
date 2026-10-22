-- Candidate applications (phase 3b). Regional: applied to every region. An application lives in the region of the
-- company that owns the job, never in the candidate's region.

-- +goose Up
-- +goose StatementBegin

-- One row per application. Personal data: every row carries the metadata the retention task and the privacy
-- notice need (BE-11): retention_until (no default, set by the application), lawful basis, purpose.
-- Special category answers are never stored here: they go to the restricted store (phase 3d).
CREATE TABLE hiring.applications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    job_id UUID NOT NULL,
    form_id UUID NOT NULL,
    form_version INTEGER NOT NULL,

    candidate_name TEXT NOT NULL,
    candidate_email TEXT NOT NULL,
    candidate_country CHAR(2) NOT NULL,
    candidate_market TEXT NOT NULL,

    answers JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- received: nothing needs a second look. flagged: a screening rule or knockout wants a person to look.
    -- Nothing is rejected automatically. The later pipeline statuses arrive with the hiring-team endpoints.
    status TEXT NOT NULL DEFAULT 'received',
    flags JSONB NOT NULL DEFAULT '[]'::jsonb,
    source TEXT NOT NULL DEFAULT 'direct',
    idempotency_key TEXT,

    notice_version TEXT NOT NULL,
    notice_acknowledged_at TIMESTAMPTZ NOT NULL,
    consent_given_at TIMESTAMPTZ,

    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    retention_until TIMESTAMPTZ NOT NULL,
    lawful_basis TEXT NOT NULL,
    collection_purpose TEXT NOT NULL,
    allowed_downstream_purposes TEXT[] NOT NULL DEFAULT '{}',

    CONSTRAINT applications_job_tenant_fk FOREIGN KEY (job_id, tenant_id)
        REFERENCES hiring.job_postings (id, tenant_id) ON DELETE RESTRICT,
    CONSTRAINT applications_form_version_fk FOREIGN KEY (form_id, form_version)
        REFERENCES hiring.form_versions (form_id, version),
    CONSTRAINT applications_status_check CHECK (status IN ('received', 'flagged')),
    CONSTRAINT applications_country_check CHECK (candidate_country ~ '^[A-Z]{2}$'),
    CONSTRAINT applications_market_check CHECK (candidate_market IN ('UK', 'US', 'EU', 'IN', 'NG', 'KE', 'ZA')),
    CONSTRAINT applications_source_check CHECK (source ~ '^[a-z]{1,20}$'),
    CONSTRAINT applications_answers_check CHECK (jsonb_typeof(answers) = 'object'),
    CONSTRAINT applications_flags_check CHECK (jsonb_typeof(flags) = 'array'),
    CONSTRAINT applications_lawful_basis_check
        CHECK (lawful_basis IN ('contract', 'legitimate_interests', 'consent', 'legal_obligation'))
);

-- A person applies to a job once. A repeat is ignored by the application and looks like success to the candidate.
CREATE UNIQUE INDEX applications_job_email_idx ON hiring.applications (job_id, lower(candidate_email));
CREATE UNIQUE INDEX applications_job_idempotency_idx ON hiring.applications (job_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
-- The hiring team's list, newest first (phase 3e), and the retention task's scan.
CREATE INDEX applications_job_submitted_idx ON hiring.applications (tenant_id, job_id, submitted_at DESC, id);
CREATE INDEX applications_retention_idx ON hiring.applications (retention_until);

CREATE TRIGGER hiring_applications_set_updated_at
    BEFORE UPDATE ON hiring.applications
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Applicant counters live beside the job, not on it: job_postings has an updated_at trigger, so counting on the
-- job row would move every job with a new applicant to the top of the "recently updated" list.
CREATE TABLE hiring.job_applicant_counts (
    job_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    applicant_count INTEGER NOT NULL DEFAULT 0,
    -- Applicants nobody has opened yet. Phase 3e decreases it when a team member opens an application.
    new_applicant_count INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT job_applicant_counts_job_tenant_fk FOREIGN KEY (job_id, tenant_id)
        REFERENCES hiring.job_postings (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT job_applicant_counts_check
        CHECK (applicant_count >= 0 AND new_applicant_count >= 0 AND new_applicant_count <= applicant_count)
);

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['applications', 'job_applicant_counts'] LOOP
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
DROP TABLE IF EXISTS hiring.job_applicant_counts;
DROP TABLE IF EXISTS hiring.applications;
-- +goose StatementEnd
