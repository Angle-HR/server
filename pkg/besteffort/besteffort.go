// Package besteffort handles errors from calls whose failure must not change
// the outcome of the operation, such as closing a connection, rolling back an
// already-finished transaction or writing a non-critical audit row. The error
// is logged instead of being silently dropped.
package besteffort

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// Log records err at warn level, tagged with op. It is a no-op for a nil error
// and for pgx.ErrTxClosed, which just means the transaction already finished.
func Log(ctx context.Context, op string, err error) {
	if err == nil || errors.Is(err, pgx.ErrTxClosed) {
		return
	}
	slog.WarnContext(ctx, "best-effort operation failed", "op", op, "error", err)
}
