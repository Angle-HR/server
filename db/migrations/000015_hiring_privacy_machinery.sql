-- Privacy machinery (phase 3d). Regional: applied to every region.
--   * hiring_restricted.special_answers: special category answers, encrypted by the application before they are
--     written, in a schema of their own so database roles can be granted (or denied) access to it separately.
--   * hiring.review_tasks: one task per flagged application; a person must open the application before deciding.
--   * hiring.org_settings columns and hiring.reapply_blocks: the optional "do not reapply for 6 months" rule.
--   * hiring.email_outbox: emails to candidates and employers, kept in the region because they hold candidate
--     names and addresses. A sender command delivers them.

-- +goose Up
-- +goose StatementBegin

CREATE SCHEMA IF NOT EXISTS hiring_restricted;
REVOKE ALL ON SCHEMA hiring_restricted FROM PUBLIC;

-- The answer itself is only in ciphertext. category and legal_condition are not secret (the job declared them);
-- they say what the answer is and why it may be held.
CREATE TABLE hiring_restricted.special_answers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    application_id UUID NOT NULL REFERENCES hiring.applications (id) ON DELETE CASCADE,
    question_id TEXT NOT NULL,
    category TEXT NOT NULL,
    legal_condition TEXT NOT NULL,
    key_id TEXT NOT NULL,
    nonce BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    retention_until TIMESTAMPTZ NOT NULL,
    lawful_basis TEXT NOT NULL,
    collection_purpose TEXT NOT NULL,
    allowed_downstream_purposes TEXT[] NOT NULL DEFAULT '{}',

    CONSTRAINT special_answers_category_check CHECK (category IN
        ('health_disability', 'race_ethnicity', 'religion_belief', 'biometric', 'criminal_records')),
    CONSTRAINT special_answers_lawful_basis_check
        CHECK (lawful_basis IN ('contract', 'legitimate_interests', 'consent', 'legal_obligation')),
    CONSTRAINT special_answers_nonce_check CHECK (octet_length(nonce) = 12),
    CONSTRAINT special_answers_one_per_question UNIQUE (application_id, question_id)
);
CREATE INDEX special_answers_retention_idx ON hiring_restricted.special_answers (retention_until);

CREATE TABLE hiring.review_tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    job_id UUID NOT NULL,
    application_id UUID NOT NULL UNIQUE REFERENCES hiring.applications (id) ON DELETE CASCADE,
    -- why the application was flagged, copied so the task is readable on its own
    reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT 'open',
    -- The last person who opened the application. A decision needs the decider to be the one who opened it.
    opened_by UUID REFERENCES accounts.users (id) ON DELETE SET NULL,
    opened_at TIMESTAMPTZ,
    decision TEXT,
    decided_by UUID REFERENCES accounts.users (id) ON DELETE SET NULL,
    decided_at TIMESTAMPTZ,
    note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    retention_until TIMESTAMPTZ NOT NULL,
    lawful_basis TEXT NOT NULL,
    collection_purpose TEXT NOT NULL,
    allowed_downstream_purposes TEXT[] NOT NULL DEFAULT '{}',

    CONSTRAINT review_tasks_job_tenant_fk FOREIGN KEY (job_id, tenant_id)
        REFERENCES hiring.job_postings (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT review_tasks_status_check CHECK (status IN ('open', 'done')),
    CONSTRAINT review_tasks_decision_check CHECK (decision IS NULL OR decision IN ('proceed', 'reject')),
    CONSTRAINT review_tasks_done_check CHECK ((status = 'done') = (decision IS NOT NULL)),
    CONSTRAINT review_tasks_opened_check CHECK (decision IS NULL OR opened_at IS NOT NULL),
    CONSTRAINT review_tasks_note_check CHECK (note IS NULL OR char_length(note) <= 2000),
    CONSTRAINT review_tasks_lawful_basis_check
        CHECK (lawful_basis IN ('contract', 'legitimate_interests', 'consent', 'legal_obligation'))
);
CREATE INDEX review_tasks_open_idx ON hiring.review_tasks (tenant_id, job_id) WHERE status = 'open';
CREATE TRIGGER hiring_review_tasks_set_updated_at
    BEFORE UPDATE ON hiring.review_tasks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Company-wide candidate settings live beside the automated screening switch, in hiring.org_settings. No row
-- (or no value) means the defaults: no reapply block, and status emails on.
ALTER TABLE hiring.org_settings ADD COLUMN reapply_block_months SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE hiring.org_settings ADD COLUMN send_status_emails BOOLEAN NOT NULL DEFAULT TRUE;
-- 0 = off (the default). 6 = a rejected candidate cannot apply to any of the company's jobs for 6 months.
ALTER TABLE hiring.org_settings ADD CONSTRAINT org_settings_reapply_check CHECK (reapply_block_months IN (0, 6));

-- A keyed hash of the candidate's email (HMAC over company and address), never the address itself.
CREATE TABLE hiring.reapply_blocks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    email_hmac TEXT NOT NULL,
    blocked_until TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    retention_until TIMESTAMPTZ NOT NULL,
    lawful_basis TEXT NOT NULL,
    collection_purpose TEXT NOT NULL,
    allowed_downstream_purposes TEXT[] NOT NULL DEFAULT '{}',

    CONSTRAINT reapply_blocks_unique UNIQUE (tenant_id, email_hmac),
    CONSTRAINT reapply_blocks_hmac_check CHECK (email_hmac ~ '^[0-9a-f]{64}$'),
    CONSTRAINT reapply_blocks_lawful_basis_check
        CHECK (lawful_basis IN ('contract', 'legitimate_interests', 'consent', 'legal_obligation'))
);
CREATE INDEX reapply_blocks_retention_idx ON hiring.reapply_blocks (retention_until);

CREATE TABLE hiring.email_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    -- Deleting the application (retention) removes its unsent emails.
    application_id UUID REFERENCES hiring.applications (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    recipient TEXT NOT NULL,
    recipient_name TEXT NOT NULL DEFAULT '',
    -- what the template needs: job title, company name, notice link, counts. Never answers.
    data JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at TIMESTAMPTZ,

    retention_until TIMESTAMPTZ NOT NULL,
    lawful_basis TEXT NOT NULL,
    collection_purpose TEXT NOT NULL,
    allowed_downstream_purposes TEXT[] NOT NULL DEFAULT '{}',

    CONSTRAINT email_outbox_kind_check CHECK (kind IN
        ('application_received', 'application_rejected', 'application_hired', 'retention_notice')),
    CONSTRAINT email_outbox_status_check CHECK (status IN ('pending', 'sent', 'failed')),
    CONSTRAINT email_outbox_lawful_basis_check
        CHECK (lawful_basis IN ('contract', 'legitimate_interests', 'consent', 'legal_obligation'))
);
-- What the sender polls.
CREATE INDEX email_outbox_due_idx ON hiring.email_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX email_outbox_retention_idx ON hiring.email_outbox (retention_until);

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['hiring.review_tasks', 'hiring.reapply_blocks',
        'hiring.email_outbox', 'hiring_restricted.special_answers'] LOOP
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON %s
            USING (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)
            WITH CHECK (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)', t);
    END LOOP;
END $$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hiring.email_outbox;
DROP TABLE IF EXISTS hiring.reapply_blocks;
ALTER TABLE hiring.org_settings DROP CONSTRAINT IF EXISTS org_settings_reapply_check;
ALTER TABLE hiring.org_settings DROP COLUMN IF EXISTS send_status_emails;
ALTER TABLE hiring.org_settings DROP COLUMN IF EXISTS reapply_block_months;
DROP TABLE IF EXISTS hiring.review_tasks;
DROP TABLE IF EXISTS hiring_restricted.special_answers;
DROP SCHEMA IF EXISTS hiring_restricted;
-- +goose StatementEnd
