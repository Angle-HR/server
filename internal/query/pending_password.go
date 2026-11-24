package query

import "github.com/google/uuid"

// UpdatePendingUserPassword returns SQL to replace the password hash of a
// still-unverified pending account (latest signup wins). Verified rows are
// never touched.
func UpdatePendingUserPassword(userID uuid.UUID, passwordHash string) (string, []any, error) {
	const sql = `
		UPDATE pending_users
		SET password_hash = $2
		WHERE id = $1 AND email_verified_at IS NULL AND deleted_at IS NULL
		RETURNING id`
	return sql, []any{userID, passwordHash}, nil
}
