-- Older audit rows stored the socket address (192.168.65.1:64455, [2001:db8::1]:443); the writer now stores the bare
-- IP. Strip the port from those rows. Only two shapes are touched, so a bare IPv6 address (which contains colons)
-- is never mangled, and the statement is idempotent: a second run matches nothing.
--   IPv4 with port:      a.b.c.d:port        -> a.b.c.d
--   bracketed IPv6:      [addr]:port, [addr] -> addr

-- +goose Up
-- +goose StatementBegin
UPDATE admin.audit_logs
SET ip = regexp_replace(ip, '^(\d{1,3}(\.\d{1,3}){3}):\d{1,5}$', '\1')
WHERE ip ~ '^\d{1,3}(\.\d{1,3}){3}:\d{1,5}$';

UPDATE admin.audit_logs
SET ip = regexp_replace(ip, '^\[([0-9A-Fa-f:.%a-zA-Z0-9_-]+)\](:\d{1,5})?$', '\1')
WHERE ip ~ '^\[[0-9A-Fa-f:.%a-zA-Z0-9_-]+\](:\d{1,5})?$';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The port was never information worth restoring; nothing to undo.
SELECT 1;
-- +goose StatementEnd
