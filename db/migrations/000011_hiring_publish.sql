-- Job publish and lifecycle (phase 2): company prerequisites for publishing (privacy contact, DPIA), the job's
-- DPIA scope confirmation, and stable ordering for screening rules. Regional: applied to every region.

-- +goose Up
-- +goose StatementBegin

-- Applicant notices name a contact. Publishing needs the email; the DPO is optional.
ALTER TABLE accounts.organizations
    ADD COLUMN IF NOT EXISTS privacy_contact_email TEXT,
    ADD COLUMN IF NOT EXISTS dpo_contact TEXT;

-- The company's data protection impact assessment, recorded in-app by Legal before the first screening job
-- publishes. Append-only by version: a new record supersedes the old one, and the history stays.
CREATE TABLE hiring.dpia_records (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    recorded_by UUID NOT NULL REFERENCES accounts.users (id),
    scope JSONB NOT NULL DEFAULT '{}'::jsonb,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dpia_records_version_unique UNIQUE (tenant_id, version)
);

ALTER TABLE hiring.dpia_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE hiring.dpia_records FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON hiring.dpia_records
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

-- Each job's owner confirms that its screening is inside the DPIA's scope. Saving different rules clears it.
ALTER TABLE hiring.job_postings ADD COLUMN dpia_confirmed_at TIMESTAMPTZ;

-- Screening rules flag a candidate for human review; they never reject. `field` holds the question id.
ALTER TABLE hiring.disqualification_rules
    ADD COLUMN position SMALLINT NOT NULL DEFAULT 0,
    ADD CONSTRAINT disqualification_rules_operator_check
        CHECK (operator IN ('in', 'not_in', 'lt', 'lte', 'gt', 'gte', 'eq', 'neq')),
    ADD CONSTRAINT disqualification_rules_reason_length_check CHECK (char_length(reason) BETWEEN 1 AND 300);

-- Hiring team lookups (job lists for people who were added to a job).
CREATE INDEX IF NOT EXISTS job_members_user_idx ON hiring.job_members (user_id);

-- One public id per job, assigned on first publish.
CREATE INDEX IF NOT EXISTS job_postings_public_idx ON hiring.job_postings (public_id) WHERE public_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS hiring.job_postings_public_idx;
DROP INDEX IF EXISTS hiring.job_members_user_idx;
ALTER TABLE hiring.disqualification_rules
    DROP CONSTRAINT IF EXISTS disqualification_rules_reason_length_check,
    DROP CONSTRAINT IF EXISTS disqualification_rules_operator_check,
    DROP COLUMN IF EXISTS position;
ALTER TABLE hiring.job_postings DROP COLUMN IF EXISTS dpia_confirmed_at;
DROP TABLE IF EXISTS hiring.dpia_records;
ALTER TABLE accounts.organizations DROP COLUMN IF EXISTS dpo_contact, DROP COLUMN IF EXISTS privacy_contact_email;
-- +goose StatementEnd
