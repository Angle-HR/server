-- Applications: public job id -> region and company, projected from hiring.job.*.v1 events so the applications
-- service resolves a public id without reading hiring.job_registry (internal/applicationsvc/jobmodel, LookupJob).
-- Insert-or-update by version; rows are never deleted (a closed job still resolves, as in job_registry).

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS hiring.app_job_index (
    public_id  TEXT        PRIMARY KEY,
    region     TEXT        NOT NULL,
    company_id UUID        NOT NULL,
    job_id     UUID        NOT NULL,
    version    BIGINT      NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hiring.app_job_index;
-- +goose StatementEnd
