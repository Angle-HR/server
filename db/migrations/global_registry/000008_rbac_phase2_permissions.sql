-- Phase 2 permission changes (7 Oct answers): two renames, one extra holder, and the member and status keys.
-- Migration 000004 stays as applied; this one moves the seed forward. The Go matrix (internal/rbac) must match
-- 000004 plus this file; TestMatrixMatchesSQLSeed checks it.

-- +goose Up
-- +goose StatementBegin
UPDATE rbac.role_permissions SET permission = 'job.collaborator.add_limited'
    WHERE permission = 'job.collaborator.add_restricted';
UPDATE rbac.role_permissions SET permission = 'job.export' WHERE permission = 'jobs.export';
UPDATE rbac.role_permissions SET permission = 'job.manage_any' WHERE permission = 'jobs.manage_any';

-- HR 2 gets the limited version only: with both keys, add_limited would mean nothing. Confirm with the roles doc.
DELETE FROM rbac.role_permissions WHERE role = 'hr_2' AND permission = 'job.collaborator.add';

INSERT INTO rbac.role_permissions (role, permission) VALUES
    ('hr_2', 'job.collaborator.add_limited'),
    ('founder', 'job.status.change'),
    ('hr_1', 'job.status.change'),
    ('hr_2', 'job.status.change'),
    ('founder', 'job.export'),
    ('hr_1', 'job.export'),
    ('founder', 'job.manage_any'),
    ('hr_1', 'job.manage_any'),
    ('founder', 'member.invite'),
    ('hr_1', 'member.invite'),
    ('founder', 'member.role.assign'),
    ('hr_1', 'member.role.assign'),
    ('founder', 'member.remove'),
    ('hr_1', 'member.remove'),
    ('founder', 'account.ownership.transfer'),
    ('founder', 'account.agreements.accept'),
    ('legal', 'account.agreements.accept')
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM rbac.role_permissions WHERE permission IN
    ('job.status.change', 'member.invite', 'member.role.assign', 'member.remove', 'account.ownership.transfer',
     'account.agreements.accept');
DELETE FROM rbac.role_permissions WHERE role = 'hr_2' AND permission = 'job.collaborator.add_limited';
INSERT INTO rbac.role_permissions (role, permission) VALUES ('hr_2', 'job.collaborator.add') ON CONFLICT DO NOTHING;
UPDATE rbac.role_permissions SET permission = 'job.collaborator.add_restricted'
    WHERE permission = 'job.collaborator.add_limited';
UPDATE rbac.role_permissions SET permission = 'jobs.export' WHERE permission = 'job.export';
UPDATE rbac.role_permissions SET permission = 'jobs.manage_any' WHERE permission = 'job.manage_any';
-- +goose StatementEnd
