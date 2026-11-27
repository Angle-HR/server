-- Attachments on candidate emails (ATS plan, phase 3, second slice). A person uploads a file to an application, it is
-- scanned for malware and kept in the regional bucket, and up to three of them are attached to one message. The file
-- lives under the application (ON DELETE CASCADE) and retention removes the object with the application.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE hiring.app_message_attachments (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL,
    job_id         UUID        NOT NULL,
    application_id UUID        NOT NULL REFERENCES hiring.applications (id) ON DELETE CASCADE,
    message_id     UUID        REFERENCES hiring.app_messages (id) ON DELETE CASCADE,
    uploaded_by    UUID,
    filename       TEXT        NOT NULL,
    content_type   TEXT        NOT NULL,
    size           BIGINT      NOT NULL,
    sha256         TEXT        NOT NULL,
    object_key     TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_message_attachments_filename_check CHECK (char_length(filename) BETWEEN 1 AND 150),
    CONSTRAINT app_message_attachments_size_check CHECK (size BETWEEN 1 AND 5242880)
);
CREATE INDEX app_message_attachments_message_idx ON hiring.app_message_attachments (message_id) WHERE message_id IS NOT NULL;
CREATE INDEX app_message_attachments_application_idx ON hiring.app_message_attachments (tenant_id, application_id, created_at);

ALTER TABLE hiring.app_message_attachments ENABLE ROW LEVEL SECURITY;
ALTER TABLE hiring.app_message_attachments FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON hiring.app_message_attachments;
CREATE POLICY tenant_isolation ON hiring.app_message_attachments
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hiring.app_message_attachments;
-- +goose StatementEnd
