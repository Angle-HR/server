package db

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
