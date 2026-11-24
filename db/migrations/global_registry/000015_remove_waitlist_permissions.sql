-- The waitlist feature is gone (CHANGELOG): its permissions no longer belong in GET /admin/permissions or in roles.
-- Idempotent: deletes nothing when the rows are already absent. role_permissions rows cascade, but are removed
-- explicitly first so the intent is visible and a schema without the cascade behaves the same.

-- +goose Up
-- +goose StatementBegin
DELETE FROM admin.role_permissions
WHERE permission_id IN (SELECT id FROM admin.permissions WHERE slug IN ('waitlist:read', 'waitlist:write'));
DELETE FROM admin.permissions WHERE slug IN ('waitlist:read', 'waitlist:write');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
INSERT INTO admin.permissions (slug, description) VALUES
    ('waitlist:read', 'List and view waitlist entries'),
    ('waitlist:write', 'Update, soft-delete, and restore waitlist entries')
ON CONFLICT (slug) DO NOTHING;
-- +goose StatementEnd
