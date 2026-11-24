-- Applications extraction (phase 3): the applications service's own audit and compliance trails. Until now the
-- applications side wrote "application.received|viewed|file_downloaded|status_changed" into hiring.audit_events and
-- the retention deletion record into hiring.compliance_log, both hiring-owned. They move to hiring.app_audit_events
-- and hiring.app_compliance_log (the "app_" prefix marks applications ownership in the shared hiring schema, as
-- for hiring.app_jobs in 000020). Hiring keeps writing its own job audit and compliance rows to its own tables.
--
-- No foreign keys at all: job_id, tenant_id and actor_id are opaque ids of other services, and the rows must
-- outlive the job, the user and the application. Row level security keyed on app.tenant_id, like the tables
-- they replace. app_compliance_log is append-only (triggers plus REVOKE), exactly like hiring.compliance_log.
--
-- Existing rows stay where they are (hiring.audit_events keeps its old application.* rows). Operators read the
-- combined trail with the UNION in docs/microservices/applications.md.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS hiring.app_audit_events (
    id         BIGSERIAL   PRIMARY KEY,
    job_id     UUID        NOT NULL,
    tenant_id  UUID        NOT NULL,
    actor_id   UUID,
    action     TEXT        NOT NULL,
    diff       JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS app_audit_events_job_idx ON hiring.app_audit_events (tenant_id, job_id, created_at);

CREATE TABLE IF NOT EXISTS hiring.app_compliance_log (
    id         BIGSERIAL   PRIMARY KEY,
    job_id     UUID        NOT NULL,
    tenant_id  UUID        NOT NULL,
    gate_id    TEXT        NOT NULL,
    severity   TEXT        NOT NULL,
    event      TEXT        NOT NULL,
    actor_id   UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_compliance_log_severity_check CHECK (severity IN ('required', 'advisory', 'informational'))
);
CREATE INDEX IF NOT EXISTS app_compliance_log_job_idx ON hiring.app_compliance_log (tenant_id, job_id, created_at);

CREATE OR REPLACE FUNCTION hiring.app_reject_log_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'hiring.app_compliance_log is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS app_compliance_log_append_only ON hiring.app_compliance_log;
CREATE TRIGGER app_compliance_log_append_only
    BEFORE UPDATE OR DELETE ON hiring.app_compliance_log
    FOR EACH ROW EXECUTE FUNCTION hiring.app_reject_log_mutation();
DROP TRIGGER IF EXISTS app_compliance_log_no_truncate ON hiring.app_compliance_log;
CREATE TRIGGER app_compliance_log_no_truncate
    BEFORE TRUNCATE ON hiring.app_compliance_log
    FOR EACH STATEMENT EXECUTE FUNCTION hiring.app_reject_log_mutation();
REVOKE UPDATE, DELETE, TRUNCATE ON hiring.app_compliance_log FROM PUBLIC;

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['app_audit_events', 'app_compliance_log'] LOOP
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
DROP TABLE IF EXISTS hiring.app_compliance_log;
DROP TABLE IF EXISTS hiring.app_audit_events;
DROP FUNCTION IF EXISTS hiring.app_reject_log_mutation();
-- +goose StatementEnd
