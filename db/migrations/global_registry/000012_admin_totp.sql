-- Admin TOTP (second factor for the platform admin console). The secret is encrypted by the application
-- (internal/auth.TOTPCrypto, TOTP_ENCRYPTION_KEY, rotatable). totp_enabled_at is set when the first code is
-- confirmed; a secret without it is an enrolment in progress and does not gate login.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE admin.users
    ADD COLUMN totp_secret TEXT,
    ADD COLUMN totp_enabled_at TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE admin.users
    DROP COLUMN totp_enabled_at,
    DROP COLUMN totp_secret;
-- +goose StatementEnd
