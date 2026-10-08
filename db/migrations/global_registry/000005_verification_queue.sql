-- Global review queue for company verification (KYB) in markets that need a person to check a government portal.
-- Holds ids, region and status only: details stay in the organization's own region.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE admin.verification_queue (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL,
    region TEXT NOT NULL,
    country_code TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_by UUID REFERENCES admin.users (id) ON DELETE SET NULL,
    decided_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT verification_queue_region_check CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia')),
    CONSTRAINT verification_queue_status_check CHECK (status IN ('pending', 'approved', 'rejected'))
);

-- One open item per organization.
CREATE UNIQUE INDEX verification_queue_one_pending_idx
    ON admin.verification_queue (organization_id) WHERE status = 'pending';
CREATE INDEX verification_queue_pending_idx
    ON admin.verification_queue (submitted_at) WHERE status = 'pending';

CREATE TRIGGER admin_verification_queue_set_updated_at
    BEFORE UPDATE ON admin.verification_queue
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS admin.verification_queue;
-- +goose StatementEnd
