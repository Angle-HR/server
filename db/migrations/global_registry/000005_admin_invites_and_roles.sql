-- +goose Up
-- +goose StatementBegin

-- Pending invites have no password until the invite is accepted.
ALTER TABLE admin.users
    ALTER COLUMN password_hash DROP NOT NULL;

ALTER TABLE admin.roles
    ADD COLUMN IF NOT EXISTS is_system BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE admin.roles SET is_system = TRUE WHERE slug = 'superadmin';

-- Keep only the seeded superadmin role; custom roles are created by admins at runtime.
-- CASCADE clears role_permissions / user_roles for the removed roles.
DELETE FROM admin.roles
WHERE slug IN ('ops', 'support', 'content_editor');

CREATE TABLE admin.invites (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL UNIQUE REFERENCES admin.users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    invited_by UUID REFERENCES admin.users (id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX admin_invites_expires_idx ON admin.invites (expires_at)
    WHERE accepted_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS admin.invites;
ALTER TABLE admin.roles DROP COLUMN IF EXISTS is_system;
-- Existing NULL password_hash rows must be cleared before restoring NOT NULL.
UPDATE admin.users SET password_hash = '' WHERE password_hash IS NULL;
ALTER TABLE admin.users ALTER COLUMN password_hash SET NOT NULL;
-- +goose StatementEnd
