-- Permission to review company verification (KYB) items in the manual review queue.

-- +goose Up
-- +goose StatementBegin
INSERT INTO admin.permissions (slug, description) VALUES
    ('verification:review', 'List, view and decide company verification (KYB) review items');

INSERT INTO admin.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM admin.roles r
CROSS JOIN admin.permissions p
WHERE r.slug = 'superadmin' AND p.slug = 'verification:review';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM admin.role_permissions
WHERE permission_id IN (SELECT id FROM admin.permissions WHERE slug = 'verification:review');
DELETE FROM admin.permissions WHERE slug = 'verification:review';
-- +goose StatementEnd
