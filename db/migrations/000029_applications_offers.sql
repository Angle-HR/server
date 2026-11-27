-- Offers and signing, phase 5 (ATS plan): offer templates, offers with our own e-signature, and the audit trail that
-- backs a signature. Owned by the applications service (app_ prefix).
--
--   hiring.app_offer_templates  the company's reusable offer letters (plain text with placeholders)
--   hiring.app_offers           one offer to a candidate. body is the letter as it was sent and never changes after
--                               that; doc_hash is its SHA-256 together with the other terms, so what was signed can be
--                               proved. The link token is never stored, only its SHA-256.
--   hiring.app_offer_events     the audit trail of an offer: created, sent, viewed, signed, declined, expired,
--                               withdrawn, with time, IP address and user agent of the person acting
--   hiring.app_messages.secret  a secret a message carries until it is sent (the offer link's token), cleared after
--   hiring.app_messages.job_title  the job's title when the message was written
--
-- Everything goes with its application (ON DELETE CASCADE), so retention and erasure remove it.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE hiring.app_offer_templates (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID        NOT NULL,
    name       TEXT        NOT NULL,
    body       TEXT        NOT NULL,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_offer_templates_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 60),
    CONSTRAINT app_offer_templates_body_check CHECK (char_length(btrim(body)) BETWEEN 1 AND 20000)
);
CREATE UNIQUE INDEX app_offer_templates_name_idx ON hiring.app_offer_templates (tenant_id, lower(name));

CREATE TABLE hiring.app_offers (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL,
    job_id         UUID        NOT NULL,
    application_id UUID        NOT NULL REFERENCES hiring.applications (id) ON DELETE CASCADE,
    created_by     UUID,
    sent_by        UUID,
    template_id    UUID,
    title          TEXT        NOT NULL,
    body           TEXT        NOT NULL,
    compensation   TEXT        NOT NULL DEFAULT '',
    start_date     DATE,
    status         TEXT        NOT NULL DEFAULT 'draft',
    expires_at     TIMESTAMPTZ,
    token_hash     TEXT        NOT NULL DEFAULT '',
    sent_at        TIMESTAMPTZ,
    viewed_at      TIMESTAMPTZ,
    signed_at      TIMESTAMPTZ,
    declined_at    TIMESTAMPTZ,
    decline_reason TEXT        NOT NULL DEFAULT '',
    signed_name    TEXT        NOT NULL DEFAULT '',
    doc_hash       TEXT        NOT NULL DEFAULT '',
    pdf_key        TEXT        NOT NULL DEFAULT '',
    sequence       INTEGER     NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_offers_status_check CHECK (status IN ('draft', 'sent', 'viewed', 'signed', 'declined', 'expired', 'withdrawn')),
    CONSTRAINT app_offers_title_check CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    CONSTRAINT app_offers_body_check CHECK (char_length(btrim(body)) BETWEEN 1 AND 30000),
    CONSTRAINT app_offers_compensation_check CHECK (char_length(compensation) <= 200),
    CONSTRAINT app_offers_decline_check CHECK (char_length(decline_reason) <= 1000)
);
-- A candidate has one live offer at a time.
CREATE UNIQUE INDEX app_offers_live_idx ON hiring.app_offers (application_id) WHERE status IN ('draft', 'sent', 'viewed');
CREATE UNIQUE INDEX app_offers_token_idx ON hiring.app_offers (token_hash) WHERE token_hash <> '';
CREATE INDEX app_offers_application_idx ON hiring.app_offers (tenant_id, application_id, created_at DESC);

CREATE TABLE hiring.app_offer_events (
    id             BIGSERIAL   PRIMARY KEY,
    tenant_id      UUID        NOT NULL,
    offer_id       UUID        NOT NULL REFERENCES hiring.app_offers (id) ON DELETE CASCADE,
    application_id UUID        NOT NULL,
    actor_id       UUID,
    type           TEXT        NOT NULL,
    ip             TEXT        NOT NULL DEFAULT '',
    user_agent     TEXT        NOT NULL DEFAULT '',
    data           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX app_offer_events_offer_idx ON hiring.app_offer_events (tenant_id, offer_id, id);

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['app_offer_templates', 'app_offers', 'app_offer_events'] LOOP
        EXECUTE format('ALTER TABLE hiring.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE hiring.%I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON hiring.%I', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON hiring.%I
            USING (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)
            WITH CHECK (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)', t);
    END LOOP;
END $$;

ALTER TABLE hiring.app_messages ADD COLUMN secret TEXT NOT NULL DEFAULT '';
-- the job's title as it was when the message was written, for the footer (the sender reads no job tables)
ALTER TABLE hiring.app_messages ADD COLUMN job_title TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE hiring.app_messages DROP COLUMN IF EXISTS job_title;
ALTER TABLE hiring.app_messages DROP COLUMN IF EXISTS secret;
DROP TABLE IF EXISTS hiring.app_offer_events;
DROP TABLE IF EXISTS hiring.app_offers;
DROP TABLE IF EXISTS hiring.app_offer_templates;
-- +goose StatementEnd
