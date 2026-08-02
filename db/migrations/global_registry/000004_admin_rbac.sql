-- +goose Up
-- +goose StatementBegin
CREATE SCHEMA IF NOT EXISTS admin;

CREATE TABLE admin.users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX admin_users_email_idx ON admin.users (email)
    WHERE is_active = TRUE;

CREATE TRIGGER admin_users_set_updated_at
    BEFORE UPDATE ON admin.users
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE admin.roles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE admin.permissions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE admin.role_permissions (
    role_id UUID NOT NULL REFERENCES admin.roles (id) ON DELETE CASCADE,
    permission_id UUID NOT NULL REFERENCES admin.permissions (id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE admin.user_roles (
    user_id UUID NOT NULL REFERENCES admin.users (id) ON DELETE CASCADE,
    role_id UUID NOT NULL REFERENCES admin.roles (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE admin.audit_logs (
    id BIGSERIAL PRIMARY KEY,
    actor_id UUID REFERENCES admin.users (id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT,
    meta JSONB NOT NULL DEFAULT '{}'::jsonb,
    ip TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX admin_audit_logs_actor_idx ON admin.audit_logs (actor_id);
CREATE INDEX admin_audit_logs_resource_idx ON admin.audit_logs (resource_type, resource_id);
CREATE INDEX admin_audit_logs_created_idx ON admin.audit_logs (created_at DESC);

INSERT INTO admin.permissions (slug, description) VALUES
    ('waitlist:read', 'List and view waitlist entries'),
    ('waitlist:write', 'Update, soft-delete, and restore waitlist entries'),
    ('users:read', 'List and view product accounts'),
    ('users:write', 'Update product accounts (soft-delete / restore)'),
    ('catalogs:read', 'List catalog entries including inactive'),
    ('catalogs:write', 'Create and update catalog entries'),
    ('jobs:read', 'List and view Fluvio jobs'),
    ('jobs:write', 'Retry failed or dead jobs'),
    ('admins:read', 'List staff and roles'),
    ('admins:write', 'Create and update staff and role assignments'),
    ('audit:read', 'View admin audit logs');

INSERT INTO admin.roles (slug, name) VALUES
    ('superadmin', 'Superadmin'),
    ('ops', 'Operations'),
    ('support', 'Support'),
    ('content_editor', 'Content editor');

INSERT INTO admin.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM admin.roles r
CROSS JOIN admin.permissions p
WHERE r.slug = 'superadmin';

INSERT INTO admin.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM admin.roles r
JOIN admin.permissions p ON p.slug IN (
    'waitlist:read', 'waitlist:write',
    'users:read', 'users:write',
    'jobs:read', 'jobs:write',
    'audit:read'
)
WHERE r.slug = 'ops';

INSERT INTO admin.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM admin.roles r
JOIN admin.permissions p ON p.slug IN (
    'waitlist:read', 'waitlist:write',
    'users:read', 'users:write'
)
WHERE r.slug = 'support';

INSERT INTO admin.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM admin.roles r
JOIN admin.permissions p ON p.slug IN (
    'catalogs:read', 'catalogs:write'
)
WHERE r.slug = 'content_editor';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS admin.audit_logs;
DROP TABLE IF EXISTS admin.user_roles;
DROP TABLE IF EXISTS admin.role_permissions;
DROP TABLE IF EXISTS admin.permissions;
DROP TABLE IF EXISTS admin.roles;
DROP TRIGGER IF EXISTS admin_users_set_updated_at ON admin.users;
DROP TABLE IF EXISTS admin.users;
DROP SCHEMA IF EXISTS admin;
-- +goose StatementEnd
