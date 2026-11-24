-- Applications extraction (phase 3): the applications service's local read model of jobs, fed by the
-- hiring.job.{published,updated,paused,resumed,closed}.v1 events (internal/applicationsvc/jobmodel). It lets the
-- service serve public apply without reading hiring.job_postings, form_versions, disqualification_rules or
-- accounts.organizations. See docs/microservices/applications-jobmodel.md.
--
-- Where it lives: table hiring.app_jobs, in the hiring schema, next to the other applications-owned tables
-- (hiring.applications, job_applicant_counts, org_settings ...). internal/dbrouter sets the regional search_path to
-- "accounts,waitlist,hiring,public", so a new schema would be invisible to every pool until the router and each
-- service's DSN learn about it; the "app_" prefix marks ownership instead. All SQL schema-qualifies the table. When
-- applications gets its own database the table moves with the others.
--
-- One row per job. version is the job_postings.revision of the last event applied: an event is applied only when
-- its version is higher, so replays and out-of-order delivery are harmless. source is 'event' for rows written by
-- the projector and 'read_through' for rows the service wrote after asking hiring for a job it had no event for
-- (version 0, so any real event replaces them).
--
-- No foreign keys: company_id and job_id are opaque ids of other services. No row level security: the projector
-- writes across companies, every read filters on company_id.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS hiring.app_jobs (
    job_id           UUID        PRIMARY KEY,
    public_id        TEXT        NOT NULL,
    company_id       UUID        NOT NULL,
    region           TEXT        NOT NULL,
    status           TEXT        NOT NULL,                  -- stored job status: published, paused, closed, expired, draft ...
    public_state     TEXT        NOT NULL DEFAULT '',       -- open, paused or closed as the event derived it
    closing_date     DATE,                                  -- last day to apply
    closed_at        TIMESTAMPTZ,                           -- set while the job is closed or expired
    retention_months INTEGER     NOT NULL DEFAULT 0,
    job_number       INTEGER     NOT NULL DEFAULT 0,
    job_code         TEXT        NOT NULL DEFAULT '',
    title            TEXT        NOT NULL DEFAULT '',
    lawful_basis     TEXT        NOT NULL DEFAULT '',
    markets          TEXT[]      NOT NULL DEFAULT '{}',
    form_id          TEXT,                                  -- NULL: the job never published, no frozen form
    form_version     INTEGER     NOT NULL DEFAULT 0,
    form_schema      JSONB,                                 -- {"form_revision":n,"questions":[...]} as frozen
    rules            JSONB       NOT NULL DEFAULT '[]',
    declarations     JSONB       NOT NULL DEFAULT '[]',
    company_name     TEXT        NOT NULL DEFAULT '',
    privacy_email    TEXT        NOT NULL DEFAULT '',
    dpo_contact      TEXT        NOT NULL DEFAULT '',
    notice           JSONB       NOT NULL DEFAULT '{}',     -- privacy notice inputs: version, lawful basis, declarations, screening flag
    close_reason     TEXT        NOT NULL DEFAULT '',
    version          BIGINT      NOT NULL DEFAULT 0,        -- job revision of the last event applied
    source           TEXT        NOT NULL DEFAULT 'event',  -- 'event' or 'read_through'
    published_at     TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),    -- the job's updated_at from the event
    applied_at       TIMESTAMPTZ NOT NULL DEFAULT now(),    -- when this service last wrote the row
    CONSTRAINT app_jobs_source_check CHECK (source IN ('event', 'read_through'))
);

CREATE INDEX IF NOT EXISTS app_jobs_public_id_idx ON hiring.app_jobs (public_id);
CREATE INDEX IF NOT EXISTS app_jobs_company_idx ON hiring.app_jobs (company_id, status);

-- Every frozen form version the service has seen, so an application made against an older version can still be
-- reviewed after the job was published again. Insert-only (a frozen version never changes); filled from the form of
-- each event, so history before the first event is only there once a read-through or rebuild has fetched it.
CREATE TABLE IF NOT EXISTS hiring.app_job_forms (
    form_id    TEXT        NOT NULL,
    version    INTEGER     NOT NULL,
    company_id UUID        NOT NULL,
    job_id     UUID        NOT NULL,
    schema     JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (form_id, version)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hiring.app_job_forms;
DROP TABLE IF EXISTS hiring.app_jobs;
-- +goose StatementEnd
