-- Applications pipeline, phase 2 (ATS plan): stages per job, notes, tags, follows and the activity feed. All of it is
-- owned by the applications service, hence the "app_" prefix in the shared hiring schema (as 000020 and 000021).
--
--   hiring.app_job_stages        a job's pipeline: Applied, Shortlisted, Interview, Offer, Hired, Disqualified, plus
--                                custom stages. Seeded when first needed (and here for every existing job).
--   hiring.applications.stage_id the application's stage. NULL means "not placed yet": an open application is then in
--                                the job's Applied stage; a withdrawn one never has a stage.
--   hiring.applications.disqualify_reason  why a candidate was disqualified (rejected).
--   hiring.app_notes             internal notes on an application
--   hiring.app_tags              the company's tag catalog; hiring.app_application_tags puts tags on applications
--   hiring.app_follows           who follows (stars) an application
--   hiring.app_events            the activity timeline, written in the same transaction as every change
--
-- status stays the coarse lifecycle (received, in_review, flagged, rejected, hired, withdrawn); a stage is where the
-- candidate is inside it. Hired, Disqualified (rejected) and Withdrawn stay final (decision 1 of the plan).
--
-- Everything that belongs to an application goes with it (ON DELETE CASCADE): retention and erasure need no change.
-- Row level security keys on app.tenant_id, like every regional table.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE hiring.app_job_stages (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID        NOT NULL,
    job_id     UUID        NOT NULL,
    key        TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    kind       TEXT        NOT NULL,
    position   INTEGER     NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_job_stages_kind_check
        CHECK (kind IN ('applied', 'shortlisted', 'interview', 'offer', 'hired', 'disqualified', 'custom')),
    CONSTRAINT app_job_stages_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 40),
    CONSTRAINT app_job_stages_key_unique UNIQUE (tenant_id, job_id, key)
);
CREATE INDEX app_job_stages_job_idx ON hiring.app_job_stages (tenant_id, job_id, position);

ALTER TABLE hiring.applications
    ADD COLUMN stage_id UUID REFERENCES hiring.app_job_stages (id) ON DELETE RESTRICT,
    ADD COLUMN disqualify_reason TEXT,
    ADD CONSTRAINT applications_disqualify_reason_check
        CHECK (disqualify_reason IS NULL OR (status = 'rejected' AND char_length(disqualify_reason) <= 40));
CREATE INDEX applications_stage_idx ON hiring.applications (tenant_id, job_id, stage_id);

CREATE TABLE hiring.app_notes (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL,
    application_id UUID        NOT NULL REFERENCES hiring.applications (id) ON DELETE CASCADE,
    author_id      UUID,
    body           TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_notes_body_check CHECK (char_length(btrim(body)) BETWEEN 1 AND 4000)
);
CREATE INDEX app_notes_application_idx ON hiring.app_notes (tenant_id, application_id, created_at DESC, id DESC);

CREATE TABLE hiring.app_tags (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID        NOT NULL,
    name       TEXT        NOT NULL,
    color      TEXT        NOT NULL DEFAULT 'gray',
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_tags_name_check CHECK (char_length(btrim(name)) BETWEEN 1 AND 30),
    CONSTRAINT app_tags_color_check CHECK (color IN ('gray', 'red', 'orange', 'yellow', 'green', 'teal', 'blue', 'purple', 'pink'))
);
CREATE UNIQUE INDEX app_tags_name_idx ON hiring.app_tags (tenant_id, lower(name));

CREATE TABLE hiring.app_application_tags (
    application_id UUID        NOT NULL REFERENCES hiring.applications (id) ON DELETE CASCADE,
    tag_id         UUID        NOT NULL REFERENCES hiring.app_tags (id) ON DELETE CASCADE,
    tenant_id      UUID        NOT NULL,
    added_by       UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, tag_id)
);
CREATE INDEX app_application_tags_tag_idx ON hiring.app_application_tags (tenant_id, tag_id);

CREATE TABLE hiring.app_follows (
    application_id UUID        NOT NULL REFERENCES hiring.applications (id) ON DELETE CASCADE,
    user_id        UUID        NOT NULL,
    tenant_id      UUID        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, user_id)
);

-- The timeline holds ids and short labels, never note text or candidate details.
CREATE TABLE hiring.app_events (
    id             BIGSERIAL   PRIMARY KEY,
    tenant_id      UUID        NOT NULL,
    job_id         UUID        NOT NULL,
    application_id UUID        NOT NULL REFERENCES hiring.applications (id) ON DELETE CASCADE,
    actor_id       UUID,
    type           TEXT        NOT NULL,
    data           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX app_events_application_idx ON hiring.app_events (tenant_id, application_id, id DESC);

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['app_job_stages', 'app_notes', 'app_tags', 'app_application_tags', 'app_follows', 'app_events'] LOOP
        EXECUTE format('ALTER TABLE hiring.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE hiring.%I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON hiring.%I', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON hiring.%I
            USING (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)
            WITH CHECK (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)', t);
    END LOOP;
END $$;

-- Backfill, one company at a time with its tenant set, so it works whether or not the migrating role bypasses row
-- level security: every job that has applications gets the default stages, and each application is placed (hired and
-- rejected into their stages, open ones into Applied; withdrawn ones stay unplaced).
DO $$
DECLARE
    org UUID;
BEGIN
    FOR org IN SELECT id FROM accounts.organizations LOOP
        PERFORM set_config('app.tenant_id', org::text, true);
        INSERT INTO hiring.app_job_stages (tenant_id, job_id, key, name, kind, position)
        SELECT j.job_id_tenant, j.job_id, d.key, d.name, d.key, d.pos
        FROM (SELECT DISTINCT tenant_id AS job_id_tenant, job_id FROM hiring.applications WHERE tenant_id = org) j
        CROSS JOIN (VALUES ('applied', 'Applied', 0), ('shortlisted', 'Shortlisted', 1), ('interview', 'Interview', 2),
                           ('offer', 'Offer', 3), ('hired', 'Hired', 4), ('disqualified', 'Disqualified', 5)) AS d(key, name, pos);
        UPDATE hiring.applications a SET stage_id = s.id
        FROM hiring.app_job_stages s
        WHERE a.tenant_id = org AND s.tenant_id = a.tenant_id AND s.job_id = a.job_id
          AND s.kind = CASE a.status WHEN 'hired' THEN 'hired' WHEN 'rejected' THEN 'disqualified' ELSE 'applied' END
          AND a.status <> 'withdrawn';
    END LOOP;
    PERFORM set_config('app.tenant_id', '', true);
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hiring.app_events;
DROP TABLE IF EXISTS hiring.app_follows;
DROP TABLE IF EXISTS hiring.app_application_tags;
DROP TABLE IF EXISTS hiring.app_tags;
DROP TABLE IF EXISTS hiring.app_notes;
DROP INDEX IF EXISTS hiring.applications_stage_idx;
ALTER TABLE hiring.applications
    DROP CONSTRAINT IF EXISTS applications_disqualify_reason_check,
    DROP COLUMN IF EXISTS disqualify_reason,
    DROP COLUMN IF EXISTS stage_id;
DROP TABLE IF EXISTS hiring.app_job_stages;
-- +goose StatementEnd
