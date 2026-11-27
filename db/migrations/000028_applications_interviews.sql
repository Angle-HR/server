-- Interviews, phase 4 (ATS plan): an interview of an application (time, place or link, interviewers), and the
-- calendar invite that goes to the candidate with it. Owned by the applications service (app_ prefix).
--
--   hiring.app_interviews   one row per interview. starts_at is UTC; timezone is the IANA zone the interview was
--                           planned in, used to show it and to write the invite. sequence counts changes (it is the
--                           SEQUENCE of the calendar event, so clients replace the old invite) and guards edits.
--   hiring.app_messages.ics the calendar part of a message, when the message is an invite, an update or a cancellation
--
-- An interview goes with its application (ON DELETE CASCADE), so retention and erasure remove it.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE hiring.app_interviews (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL,
    job_id          UUID        NOT NULL,
    application_id  UUID        NOT NULL REFERENCES hiring.applications (id) ON DELETE CASCADE,
    created_by      UUID,
    starts_at       TIMESTAMPTZ NOT NULL,
    duration_min    INTEGER     NOT NULL,
    timezone        TEXT        NOT NULL,
    kind            TEXT        NOT NULL,
    location        TEXT        NOT NULL DEFAULT '',
    notes           TEXT        NOT NULL DEFAULT '',
    interviewer_ids UUID[]      NOT NULL DEFAULT '{}',
    status          TEXT        NOT NULL DEFAULT 'scheduled',
    sequence        INTEGER     NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_interviews_kind_check CHECK (kind IN ('video', 'phone', 'onsite')),
    CONSTRAINT app_interviews_status_check CHECK (status IN ('scheduled', 'completed', 'cancelled')),
    CONSTRAINT app_interviews_duration_check CHECK (duration_min BETWEEN 15 AND 480),
    CONSTRAINT app_interviews_location_check CHECK (char_length(location) <= 300),
    CONSTRAINT app_interviews_notes_check CHECK (char_length(notes) <= 2000),
    CONSTRAINT app_interviews_interviewers_check CHECK (cardinality(interviewer_ids) <= 10)
);
CREATE INDEX app_interviews_application_idx ON hiring.app_interviews (tenant_id, application_id, starts_at);
CREATE INDEX app_interviews_schedule_idx ON hiring.app_interviews (tenant_id, starts_at) WHERE status = 'scheduled';
CREATE INDEX app_interviews_interviewers_idx ON hiring.app_interviews USING gin (interviewer_ids);

ALTER TABLE hiring.app_interviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE hiring.app_interviews FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON hiring.app_interviews
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE hiring.app_messages ADD COLUMN ics TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE hiring.app_messages DROP COLUMN IF EXISTS ics;
DROP TABLE IF EXISTS hiring.app_interviews;
-- +goose StatementEnd
