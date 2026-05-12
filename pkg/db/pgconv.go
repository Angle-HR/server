package db

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Angle-HR/server/pkg/apperror"
)

// Wrap maps common PostgreSQL errors onto application sentinels.
func Wrap(err error, message string) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%s: %w", message, err)
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return fmt.Errorf("%s: %w", message, apperror.ErrConflict)
	default:
		return fmt.Errorf("%s: %w", message, err)
	}
}

// OptionalUUID converts an optional UUID into a pgtype value for sqlc nullable args.
func OptionalUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}

	return pgtype.UUID{Bytes: *id, Valid: true}
}

// OptionalTime converts an optional timestamp into a pgtype value for sqlc nullable args.
func OptionalTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}

	return pgtype.Timestamptz{Time: *value, Valid: true}
}

// StringValue coerces sqlc scalar results into strings.
func StringValue(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	case nil:
		return "", fmt.Errorf("expected string value")
	default:
		return fmt.Sprint(typed), nil
	}
}
