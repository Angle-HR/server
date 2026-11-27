-- org.profile.edit lets a member change the company's public profile and careers page address. Founder and HR 1.
-- The Go matrix (pkg/rbac) must match: TestMatrixMatchesSQLSeed reads this file after 000004 and 000008.

-- +goose Up
-- +goose StatementBegin
INSERT INTO rbac.role_permissions (role, permission) VALUES
    ('founder', 'org.profile.edit'),
    ('hr_1', 'org.profile.edit')
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM rbac.role_permissions WHERE permission = 'org.profile.edit';
-- +goose StatementEnd
