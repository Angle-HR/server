-- Applications extraction (phase 3): drop every foreign key FROM a table the applications service owns TO a table
-- owned by hiring (job_postings, form_versions) or by org/identity (accounts.organizations, accounts.users). The
-- columns stay and are now opaque ids that the API edge validates. Foreign keys between applications-owned tables
-- (application_files -> applications, review_tasks -> applications, special_answers -> applications,
-- email_outbox -> applications) are kept.
--
-- Applications-owned tables: hiring.applications, application_files, review_tasks, reapply_blocks, email_outbox,
-- job_applicant_counts, hiring_restricted.special_answers and hiring.org_settings (the applicant settings; the
-- table also holds hiring's automated-screening flag, whose user reference is dropped too).
--
-- Cascades that go away, and why nothing depended on them (see docs/microservices/BOUNDARIES.md, 3.1):
--   * job_postings -> job_applicant_counts, review_tasks (ON DELETE CASCADE) and -> applications,
--     application_files (ON DELETE RESTRICT): hiring never deletes a job row. DeleteDraft only soft-deletes
--     (deleted_at) and only drafts, which cannot have applications, counters or tasks. The RESTRICT guard against
--     hard-deleting a job that has applications is gone; the rule now lives in hiring (jobs are never hard-deleted).
--   * organizations -> org_settings, reapply_blocks, email_outbox (ON DELETE CASCADE): the only hard delete of an
--     organization is DELETE FROM organizations WHERE owner_user_id (switching an onboarding account to
--     individual), which runs before the company can hold applications, blocks or emails. If org ever deletes a
--     company for real, it must emit org.company.deleted and applications must delete the tenant's rows on that
--     event; the cascade can no longer do it.
--   * users -> opened_by / decided_by / first_opened_by (ON DELETE SET NULL): no code hard-deletes a user. A user
--     removed by hand now leaves a dangling id on the application or review task instead of a NULL.
--
-- Indexes: nothing is added. PostgreSQL does not create an index for the referencing side of a foreign key, so
-- dropping these constraints removes no index. The lookups that matter are covered by applications_job_email_idx,
-- applications_job_idempotency_idx, applications_job_submitted_idx (tenant_id, job_id, ...),
-- application_files_application_idx and _expiry_idx, review_tasks_open_idx (tenant_id, job_id),
-- reapply_blocks_unique (tenant_id, email_hmac), email_outbox_due_idx and the primary keys of
-- job_applicant_counts (job_id) and org_settings (tenant_id).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE hiring.applications
    DROP CONSTRAINT IF EXISTS applications_job_tenant_fk,
    DROP CONSTRAINT IF EXISTS applications_form_version_fk,
    DROP CONSTRAINT IF EXISTS applications_first_opened_by_fkey,
    DROP CONSTRAINT IF EXISTS applications_decided_by_fkey;

ALTER TABLE hiring.application_files
    DROP CONSTRAINT IF EXISTS application_files_job_tenant_fk;

ALTER TABLE hiring.job_applicant_counts
    DROP CONSTRAINT IF EXISTS job_applicant_counts_job_tenant_fk;

ALTER TABLE hiring.review_tasks
    DROP CONSTRAINT IF EXISTS review_tasks_job_tenant_fk,
    DROP CONSTRAINT IF EXISTS review_tasks_opened_by_fkey,
    DROP CONSTRAINT IF EXISTS review_tasks_decided_by_fkey;

ALTER TABLE hiring.reapply_blocks
    DROP CONSTRAINT IF EXISTS reapply_blocks_tenant_id_fkey;

ALTER TABLE hiring.email_outbox
    DROP CONSTRAINT IF EXISTS email_outbox_tenant_id_fkey;

ALTER TABLE hiring.org_settings
    DROP CONSTRAINT IF EXISTS org_settings_tenant_id_fkey,
    DROP CONSTRAINT IF EXISTS org_settings_automated_screening_enabled_by_fkey;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Restores the constraints exactly as 000010 and 000013-000016 created them. It fails if rows now point at a job,
-- form version, company or user that does not exist, which is the right answer to a rollback over orphaned data.
ALTER TABLE hiring.applications
    ADD CONSTRAINT applications_job_tenant_fk FOREIGN KEY (job_id, tenant_id)
        REFERENCES hiring.job_postings (id, tenant_id) ON DELETE RESTRICT,
    ADD CONSTRAINT applications_form_version_fk FOREIGN KEY (form_id, form_version)
        REFERENCES hiring.form_versions (form_id, version),
    ADD CONSTRAINT applications_first_opened_by_fkey FOREIGN KEY (first_opened_by)
        REFERENCES accounts.users (id) ON DELETE SET NULL,
    ADD CONSTRAINT applications_decided_by_fkey FOREIGN KEY (decided_by)
        REFERENCES accounts.users (id) ON DELETE SET NULL;

ALTER TABLE hiring.application_files
    ADD CONSTRAINT application_files_job_tenant_fk FOREIGN KEY (job_id, tenant_id)
        REFERENCES hiring.job_postings (id, tenant_id) ON DELETE RESTRICT;

ALTER TABLE hiring.job_applicant_counts
    ADD CONSTRAINT job_applicant_counts_job_tenant_fk FOREIGN KEY (job_id, tenant_id)
        REFERENCES hiring.job_postings (id, tenant_id) ON DELETE CASCADE;

ALTER TABLE hiring.review_tasks
    ADD CONSTRAINT review_tasks_job_tenant_fk FOREIGN KEY (job_id, tenant_id)
        REFERENCES hiring.job_postings (id, tenant_id) ON DELETE CASCADE,
    ADD CONSTRAINT review_tasks_opened_by_fkey FOREIGN KEY (opened_by)
        REFERENCES accounts.users (id) ON DELETE SET NULL,
    ADD CONSTRAINT review_tasks_decided_by_fkey FOREIGN KEY (decided_by)
        REFERENCES accounts.users (id) ON DELETE SET NULL;

ALTER TABLE hiring.reapply_blocks
    ADD CONSTRAINT reapply_blocks_tenant_id_fkey FOREIGN KEY (tenant_id)
        REFERENCES accounts.organizations (id) ON DELETE CASCADE;

ALTER TABLE hiring.email_outbox
    ADD CONSTRAINT email_outbox_tenant_id_fkey FOREIGN KEY (tenant_id)
        REFERENCES accounts.organizations (id) ON DELETE CASCADE;

ALTER TABLE hiring.org_settings
    ADD CONSTRAINT org_settings_tenant_id_fkey FOREIGN KEY (tenant_id)
        REFERENCES accounts.organizations (id) ON DELETE CASCADE,
    ADD CONSTRAINT org_settings_automated_screening_enabled_by_fkey FOREIGN KEY (automated_screening_enabled_by)
        REFERENCES accounts.users (id);
-- +goose StatementEnd
