package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/pkg/apperror"
)

// InviteResult is returned after creating or rotating an invite.
type InviteResult struct {
	Staff     StaffMember
	RawToken  string
	ExpiresAt time.Time
}

// InviteStaff creates an inactive admin user with roles and a pending invite.
// RawToken must be delivered out-of-band (email); only its hash is stored.
func (s *Store) InviteStaff(ctx context.Context, email, name string, roleSlugs []string, invitedBy uuid.UUID) (InviteResult, error) {
	rawToken, err := NewInviteToken()
	if err != nil {
		return InviteResult{}, err
	}
	expiresAt := time.Now().UTC().Add(InviteTTL)
	tokenHash := HashInviteToken(rawToken)

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return InviteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var u User
	err = tx.QueryRow(ctx, `
		INSERT INTO admin.users (email, password_hash, name, is_active)
		VALUES ($1, NULL, $2, FALSE)
		RETURNING id, email, name, is_active, created_at, updated_at, password_hash
	`, email, name).Scan(&u.ID, &u.Email, &u.Name, &u.IsActive, &u.CreatedAt, &u.UpdatedAt, &u.PasswordHash)
	if isUniqueViolation(err) {
		return InviteResult{}, apperror.New(apperror.CodeConflict, "admin email already registered")
	}
	if err != nil {
		return InviteResult{}, fmt.Errorf("create invited admin user: %w", err)
	}

	for _, slug := range roleSlugs {
		tag, err := tx.Exec(ctx, `
			INSERT INTO admin.user_roles (user_id, role_id)
			SELECT $1, r.id FROM admin.roles r WHERE r.slug = $2
		`, u.ID, slug)
		if err != nil {
			return InviteResult{}, err
		}
		if tag.RowsAffected() == 0 {
			return InviteResult{}, apperror.NewWithDetails(
				apperror.CodeValidationError,
				"unknown role",
				map[string]any{"role": slug},
			)
		}
	}

	var inviter *uuid.UUID
	if invitedBy != uuid.Nil {
		inviter = &invitedBy
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO admin.invites (user_id, token_hash, expires_at, invited_by)
		VALUES ($1, $2, $3, $4)
	`, u.ID, tokenHash, expiresAt, inviter)
	if err != nil {
		return InviteResult{}, fmt.Errorf("create invite: %w", err)
	}

	staff, err := scanStaffMember(tx.QueryRow(ctx, staffSelectSQL+`
		WHERE u.id = $1
		GROUP BY u.id
	`, u.ID))
	if err != nil {
		return InviteResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return InviteResult{}, err
	}

	return InviteResult{Staff: staff, RawToken: rawToken, ExpiresAt: expiresAt}, nil
}

// ResendInvite rotates the invite token for a pending staff member.
func (s *Store) ResendInvite(ctx context.Context, userID uuid.UUID) (InviteResult, error) {
	rawToken, err := NewInviteToken()
	if err != nil {
		return InviteResult{}, err
	}
	expiresAt := time.Now().UTC().Add(InviteTTL)
	tokenHash := HashInviteToken(rawToken)

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return InviteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var acceptedAt *time.Time
	var passwordHash *string
	err = tx.QueryRow(ctx, `
		SELECT u.password_hash, i.accepted_at
		FROM admin.users u
		LEFT JOIN admin.invites i ON i.user_id = u.id
		WHERE u.id = $1
	`, userID).Scan(&passwordHash, &acceptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return InviteResult{}, apperror.ErrNotFound
	}
	if err != nil {
		return InviteResult{}, err
	}
	if acceptedAt != nil || passwordHash != nil {
		return InviteResult{}, apperror.New(apperror.CodeConflict, "invite already accepted")
	}

	tag, err := tx.Exec(ctx, `
		UPDATE admin.invites
		SET token_hash = $2, expires_at = $3, accepted_at = NULL
		WHERE user_id = $1 AND accepted_at IS NULL
	`, userID, tokenHash, expiresAt)
	if err != nil {
		return InviteResult{}, err
	}
	if tag.RowsAffected() == 0 {
		// No invite row yet (shouldn't happen); insert one.
		_, err = tx.Exec(ctx, `
			INSERT INTO admin.invites (user_id, token_hash, expires_at)
			VALUES ($1, $2, $3)
		`, userID, tokenHash, expiresAt)
		if err != nil {
			return InviteResult{}, err
		}
	}

	staff, err := scanStaffMember(tx.QueryRow(ctx, staffSelectSQL+`
		WHERE u.id = $1
		GROUP BY u.id
	`, userID))
	if err != nil {
		return InviteResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return InviteResult{}, err
	}
	return InviteResult{Staff: staff, RawToken: rawToken, ExpiresAt: expiresAt}, nil
}

// GetInviteByToken returns invite preview for a valid pending token.
func (s *Store) GetInviteByToken(ctx context.Context, rawToken string) (InvitePreview, error) {
	if rawToken == "" {
		return InvitePreview{}, apperror.ErrNotFound
	}
	tokenHash := HashInviteToken(rawToken)

	var email string
	var expiresAt time.Time
	var acceptedAt *time.Time
	err := s.DB.QueryRow(ctx, `
		SELECT u.email, i.expires_at, i.accepted_at
		FROM admin.invites i
		JOIN admin.users u ON u.id = i.user_id
		WHERE i.token_hash = $1
	`, tokenHash).Scan(&email, &expiresAt, &acceptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvitePreview{}, apperror.ErrNotFound
	}
	if err != nil {
		return InvitePreview{}, err
	}
	if acceptedAt != nil {
		return InvitePreview{}, apperror.New(apperror.CodeGone, "invite already accepted")
	}
	if time.Now().UTC().After(expiresAt) {
		return InvitePreview{}, apperror.New(apperror.CodeGone, "invite expired")
	}
	return InvitePreview{Email: email, ExpiresAt: expiresAt}, nil
}

// AcceptInviteResult is returned after a successful invite acceptance.
type AcceptInviteResult struct {
	User User
}

// AcceptInvite sets name and password, activates the user, and marks the invite accepted.
func (s *Store) AcceptInvite(ctx context.Context, rawToken, name, passwordHash string) (AcceptInviteResult, error) {
	if rawToken == "" {
		return AcceptInviteResult{}, apperror.ErrNotFound
	}
	tokenHash := HashInviteToken(rawToken)

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return AcceptInviteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID uuid.UUID
	var expiresAt time.Time
	var acceptedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT user_id, expires_at, accepted_at
		FROM admin.invites
		WHERE token_hash = $1
		FOR UPDATE
	`, tokenHash).Scan(&userID, &expiresAt, &acceptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AcceptInviteResult{}, apperror.ErrNotFound
	}
	if err != nil {
		return AcceptInviteResult{}, err
	}
	if acceptedAt != nil {
		return AcceptInviteResult{}, apperror.New(apperror.CodeGone, "invite already accepted")
	}
	if time.Now().UTC().After(expiresAt) {
		return AcceptInviteResult{}, apperror.New(apperror.CodeGone, "invite expired")
	}

	_, err = tx.Exec(ctx, `
		UPDATE admin.users
		SET name = $2, password_hash = $3, is_active = TRUE, updated_at = now()
		WHERE id = $1
	`, userID, name, passwordHash)
	if err != nil {
		return AcceptInviteResult{}, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE admin.invites SET accepted_at = now() WHERE user_id = $1
	`, userID)
	if err != nil {
		return AcceptInviteResult{}, err
	}

	u, err := scanUser(tx.QueryRow(ctx, `
		SELECT id, email, name, is_active, created_at, updated_at, password_hash
		FROM admin.users WHERE id = $1
	`, userID))
	if err != nil {
		return AcceptInviteResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return AcceptInviteResult{}, err
	}
	return AcceptInviteResult{User: u}, nil
}
