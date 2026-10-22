-- Application pipeline (phase 3e). Regional: applied to every region.
-- Statuses: received -> in_review -> rejected | hired; flagged -> in_review or rejected only through a logged
-- review; withdrawn from any open state. rejected, hired and withdrawn are final.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE hiring.applications DROP CONSTRAINT applications_status_check;
ALTER TABLE hiring.applications ADD CONSTRAINT applications_status_check
    CHECK (status IN ('received', 'in_review', 'flagged', 'rejected', 'withdrawn', 'hired'));

-- The first person to open an application takes it out of the job's "new" count.
ALTER TABLE hiring.applications ADD COLUMN first_opened_at TIMESTAMPTZ;
ALTER TABLE hiring.applications ADD COLUMN first_opened_by UUID REFERENCES accounts.users (id) ON DELETE SET NULL;
-- When and by whom the application was rejected, hired or withdrawn, and the reviewer's note.
ALTER TABLE hiring.applications ADD COLUMN decided_at TIMESTAMPTZ;
ALTER TABLE hiring.applications ADD COLUMN decided_by UUID REFERENCES accounts.users (id) ON DELETE SET NULL;
ALTER TABLE hiring.applications ADD COLUMN status_note TEXT;
ALTER TABLE hiring.applications ADD CONSTRAINT applications_status_note_check
    CHECK (status_note IS NULL OR char_length(status_note) <= 2000);
ALTER TABLE hiring.applications ADD CONSTRAINT applications_decided_check
    CHECK ((status IN ('rejected', 'hired', 'withdrawn')) = (decided_at IS NOT NULL));

-- The list's filters: one job, optionally one status, newest first.
CREATE INDEX applications_job_status_idx ON hiring.applications (tenant_id, job_id, status, submitted_at DESC, id DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS hiring.applications_job_status_idx;
ALTER TABLE hiring.applications DROP CONSTRAINT IF EXISTS applications_decided_check;
ALTER TABLE hiring.applications DROP CONSTRAINT IF EXISTS applications_status_note_check;
ALTER TABLE hiring.applications DROP COLUMN IF EXISTS status_note;
ALTER TABLE hiring.applications DROP COLUMN IF EXISTS decided_by;
ALTER TABLE hiring.applications DROP COLUMN IF EXISTS decided_at;
ALTER TABLE hiring.applications DROP COLUMN IF EXISTS first_opened_by;
ALTER TABLE hiring.applications DROP COLUMN IF EXISTS first_opened_at;
ALTER TABLE hiring.applications DROP CONSTRAINT applications_status_check;
ALTER TABLE hiring.applications ADD CONSTRAINT applications_status_check CHECK (status IN ('received', 'flagged'));
-- +goose StatementEnd
