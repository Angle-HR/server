-- Candidate email, phase 3 (ATS plan): per-company templates, messages sent to a candidate from an application
-- (immediately or scheduled) and the company's reply-to address. Owned by the applications service (app_ prefix).
--
--   hiring.app_email_templates  reusable subject and body with placeholders ({{candidate_name}}, {{job_title}},
--                               {{company}}, {{sender_name}}); plain text, never HTML
--   hiring.app_messages         one row per email to a candidate, with the exact rendered text, who sent it, when,
--                               and how it went. The recipient is not stored: it is the application's candidate,
--                               read when the message is sent, so a message can never go anywhere else.
--   hiring.app_email_settings   the company's reply-to address (replies are not read by OpenHR)
--
-- A message goes with its application (ON DELETE CASCADE), so retention and erasure remove it.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE hiring.app_email_templates (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID        NOT NULL,
    name       TEXT        NOT NULL,
    subject    TEXT        NOT NULL,
    body       TEXT        NOT NULL,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_email_templates_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 60),
    CONSTRAINT app_email_templates_subject_check CHECK (char_length(btrim(subject)) BETWEEN 1 AND 200),
    CONSTRAINT app_email_templates_body_check CHECK (char_length(btrim(body)) BETWEEN 1 AND 10000)
);
CREATE UNIQUE INDEX app_email_templates_name_idx ON hiring.app_email_templates (tenant_id, lower(name));

CREATE TABLE hiring.app_messages (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL,
    job_id         UUID        NOT NULL,
    application_id UUID        NOT NULL REFERENCES hiring.applications (id) ON DELETE CASCADE,
    sender_id      UUID,
    template_id    UUID,
    subject        TEXT        NOT NULL,
    body           TEXT        NOT NULL,
    reply_to       TEXT        NOT NULL DEFAULT '',
    -- the company's name as it was when the message was written, for the footer
    company        TEXT        NOT NULL DEFAULT '',
    -- scheduled: waits for scheduled_at. queued: due now. sent / failed / cancelled are final.
    status         TEXT        NOT NULL DEFAULT 'queued',
    scheduled_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts       INTEGER     NOT NULL DEFAULT 0,
    last_error     TEXT        NOT NULL DEFAULT '',
    lease_until    TIMESTAMPTZ,
    sent_at        TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_messages_status_check CHECK (status IN ('scheduled', 'queued', 'sent', 'failed', 'cancelled')),
    CONSTRAINT app_messages_subject_check CHECK (char_length(btrim(subject)) BETWEEN 1 AND 200),
    CONSTRAINT app_messages_body_check CHECK (char_length(btrim(body)) BETWEEN 1 AND 12000)
);
CREATE INDEX app_messages_application_idx ON hiring.app_messages (tenant_id, application_id, created_at DESC, id DESC);
CREATE INDEX app_messages_due_idx ON hiring.app_messages (scheduled_at) WHERE status IN ('scheduled', 'queued');

CREATE TABLE hiring.app_email_settings (
    tenant_id  UUID        PRIMARY KEY,
    reply_to   TEXT        NOT NULL DEFAULT '',
    updated_by UUID,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_email_settings_reply_to_check CHECK (char_length(reply_to) <= 254)
);

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['app_email_templates', 'app_messages', 'app_email_settings'] LOOP
        EXECUTE format('ALTER TABLE hiring.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE hiring.%I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON hiring.%I', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON hiring.%I
            USING (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)
            WITH CHECK (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)', t);
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hiring.app_email_settings;
DROP TABLE IF EXISTS hiring.app_messages;
DROP TABLE IF EXISTS hiring.app_email_templates;
-- +goose StatementEnd
