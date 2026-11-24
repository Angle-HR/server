-- Permission to delete a company (DELETE /admin/companies/{id}). Granted to the superadmin role only.

-- +goose Up
-- +goose StatementBegin
INSERT INTO admin.permissions (slug, description) VALUES
    ('companies:delete', 'Delete a company; its applications, files and jobs are removed asynchronously');

INSERT INTO admin.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM admin.roles r
CROSS JOIN admin.permissions p
WHERE r.slug = 'superadmin' AND p.slug = 'companies:delete';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM admin.role_permissions
WHERE permission_id IN (SELECT id FROM admin.permissions WHERE slug = 'companies:delete');
DELETE FROM admin.permissions WHERE slug = 'companies:delete';
-- +goose StatementEnd
