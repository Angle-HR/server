package waitlist

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/region"
)

// PoolFor returns the regional PostgreSQL pool for the request context.
func PoolFor(ctx context.Context, router *dbrouter.DBRouter) (*pgxpool.Pool, error) {
	if router == nil {
		return nil, errors.New("waitlist: db router is nil")
	}

	reg, ok := region.GetRegion(ctx)
	if !ok {
		return nil, fmt.Errorf("waitlist: region not on context")
	}

	return router.DB(reg)
}
