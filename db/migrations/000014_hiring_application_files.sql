-- Candidate file uploads (phase 3c). Regional: applied to every region. One row per uploaded file, from the
-- moment a candidate asks for an upload URL until an application owns the file or the sweeper deletes it.
-- The file itself is in the region's object storage under u/ (staging) and f/ (scanned); the row never holds it.

-- +goose Up
-- +goose StatementBegin

CREATE TABLE hiring.application_files (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    job_id UUID NOT NULL,
    question_id TEXT NOT NULL,
    application_id UUID,

    -- The name the candidate's device gave the file (cleaned), shown to reviewers. Never used as a storage key.
    original_filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    declared_size_bytes BIGINT NOT NULL,
    size_bytes BIGINT,
    sha256 TEXT,

    -- pending: URL issued, file not yet checked. clean: size, type and malware checks passed, the scanned
    -- bytes are under the final key. claimed: an application owns it. rejected: failed a check, object removed.
    status TEXT NOT NULL DEFAULT 'pending',
    -- While nobody owns the file this is when the sweeper deletes it. Null once an application owns it.
    expires_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Personal-data metadata (BE-11). retention_until has no default: the pending upload sets it to expires_at
    -- and claiming the file replaces it with the application's own date.
    retention_until TIMESTAMPTZ NOT NULL,
    lawful_basis TEXT NOT NULL,
    collection_purpose TEXT NOT NULL,
    allowed_downstream_purposes TEXT[] NOT NULL DEFAULT '{}',

    CONSTRAINT application_files_job_tenant_fk FOREIGN KEY (job_id, tenant_id)
        REFERENCES hiring.job_postings (id, tenant_id) ON DELETE RESTRICT,
    CONSTRAINT application_files_application_fk FOREIGN KEY (application_id)
        REFERENCES hiring.applications (id) ON DELETE RESTRICT,
    CONSTRAINT application_files_status_check CHECK (status IN ('pending', 'clean', 'claimed', 'rejected')),
    CONSTRAINT application_files_owner_check CHECK (
        (status = 'claimed') = (application_id IS NOT NULL)
        AND (status = 'claimed') = (expires_at IS NULL)),
    CONSTRAINT application_files_size_check CHECK (
        declared_size_bytes > 0 AND (size_bytes IS NULL OR size_bytes > 0)),
    CONSTRAINT application_files_clean_check CHECK (
        status NOT IN ('clean', 'claimed') OR (size_bytes IS NOT NULL AND sha256 IS NOT NULL)),
    CONSTRAINT application_files_lawful_basis_check
        CHECK (lawful_basis IN ('contract', 'legitimate_interests', 'consent', 'legal_obligation'))
);

-- One file per question of an application.
CREATE UNIQUE INDEX application_files_application_question_idx
    ON hiring.application_files (application_id, question_id) WHERE application_id IS NOT NULL;
-- The hiring team's view of an application's files (phase 3e).
CREATE INDEX application_files_application_idx ON hiring.application_files (application_id) WHERE application_id IS NOT NULL;
-- The sweeper's scan: unclaimed uploads that ran out of time.
CREATE INDEX application_files_expiry_idx ON hiring.application_files (tenant_id, expires_at) WHERE expires_at IS NOT NULL;
-- The retention task's scan.
CREATE INDEX application_files_retention_idx ON hiring.application_files (retention_until);

CREATE TRIGGER hiring_application_files_set_updated_at
    BEFORE UPDATE ON hiring.application_files
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE hiring.application_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE hiring.application_files FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON hiring.application_files
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hiring.application_files;
-- +goose StatementEnd
