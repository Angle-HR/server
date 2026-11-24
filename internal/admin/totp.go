package admin

import (
	"context"

	"github.com/google/uuid"
)

// SetTOTPSecret stores a freshly generated (encrypted) secret for an admin who has not enabled TOTP yet, replacing a
// pending enrolment. It reports false when TOTP is already enabled (nothing is written).
func (s *Store) SetTOTPSecret(ctx context.Context, id uuid.UUID, encrypted string) (bool, error) {
	tag, err := s.DB.Exec(ctx, `
		UPDATE admin.users SET totp_secret = $2
		WHERE id = $1 AND totp_enabled_at IS NULL
	`, id, encrypted)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// EnableTOTP marks the pending secret confirmed. It reports false when there is no pending secret.
func (s *Store) EnableTOTP(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.DB.Exec(ctx, `
		UPDATE admin.users SET totp_enabled_at = now()
		WHERE id = $1 AND totp_secret IS NOT NULL AND totp_enabled_at IS NULL
	`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClearTOTP removes the secret and the enabled flag (self-disable and superadmin reset). It reports whether the
// admin had any TOTP state.
func (s *Store) ClearTOTP(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.DB.Exec(ctx, `
		UPDATE admin.users SET totp_secret = NULL, totp_enabled_at = NULL
		WHERE id = $1 AND (totp_secret IS NOT NULL OR totp_enabled_at IS NOT NULL)
	`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
