-- Applications: who may act on a job, from hiring.job.members_changed.v1 and the job snapshot's created_by
-- (internal/applicationsvc/jobmodel). With them JobAccess no longer reads hiring.job_members or job_postings.
-- app_job_members holds the complete team of a job and the job revision it was saved at; a members event replaces it
-- only when its version is higher. It is separate from app_jobs because the two kinds of event can arrive in any
-- order. No foreign keys and no row level security, as for app_jobs: every read filters on company_id.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE hiring.app_jobs ADD COLUMN IF NOT EXISTS created_by UUID;
-- anywhere, specific_area, specific_timezone or '' (row from before events carried it). An anywhere job has no
-- markets and is open to candidates from every country.
ALTER TABLE hiring.app_jobs ADD COLUMN IF NOT EXISTS location_mode TEXT NOT NULL DEFAULT '';

-- An applicant of an anywhere job may live in a country no launch market covers: candidate_market is then OTHER.
ALTER TABLE hiring.applications DROP CONSTRAINT IF EXISTS applications_market_check;
ALTER TABLE hiring.applications ADD CONSTRAINT applications_market_check
    CHECK (candidate_market IN ('UK', 'US', 'EU', 'IN', 'NG', 'KE', 'ZA', 'OTHER'));

CREATE TABLE IF NOT EXISTS hiring.app_job_members (
    job_id     UUID        PRIMARY KEY,
    company_id UUID        NOT NULL,
    version    BIGINT      NOT NULL,
    members    JSONB       NOT NULL DEFAULT '[]',   -- [{"user_id":"...","role":"hiring_manager"}]
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Companies whose applications data was deleted (org.company.deleted.v1). The job projector ignores their events,
-- so the closing events of the deletion itself do not bring their jobs back.
CREATE TABLE IF NOT EXISTS hiring.app_deleted_companies (
    company_id UUID        PRIMARY KEY,
    deleted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS app_job_members_company_idx ON hiring.app_job_members (company_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hiring.app_deleted_companies;
DROP TABLE IF EXISTS hiring.app_job_members;
ALTER TABLE hiring.applications DROP CONSTRAINT IF EXISTS applications_market_check;
ALTER TABLE hiring.applications ADD CONSTRAINT applications_market_check
    CHECK (candidate_market IN ('UK', 'US', 'EU', 'IN', 'NG', 'KE', 'ZA')) NOT VALID;
ALTER TABLE hiring.app_jobs DROP COLUMN IF EXISTS location_mode;
ALTER TABLE hiring.app_jobs DROP COLUMN IF EXISTS created_by;
-- +goose StatementEnd
