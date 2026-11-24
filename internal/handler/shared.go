package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/region"
)

// globalDB reads and writes against the global registry database.
type globalDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// KYBNotifierFactory builds a region's KYB email notifier. The monolith keeps the field on AdminHandler so the
// wiring in internal/app is shared with identityhttp, which does the review itself.
type KYBNotifierFactory func(reg region.Region) kyb.Notifier

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
