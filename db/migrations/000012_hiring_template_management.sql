-- Template management (phase 2b): pinning and usage tracking for company templates. Regional: applied to every region.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE hiring.templates
    ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS use_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ;

ALTER TABLE hiring.templates
    ADD CONSTRAINT templates_use_count_check CHECK (use_count >= 0);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE hiring.templates DROP CONSTRAINT IF EXISTS templates_use_count_check;
ALTER TABLE hiring.templates
    DROP COLUMN IF EXISTS last_used_at,
    DROP COLUMN IF EXISTS use_count,
    DROP COLUMN IF EXISTS pinned_at;

-- +goose StatementEnd
