// Package expiry runs the system action that expires published jobs whose closing date has passed.
package expiry

import (
	"context"
	"fmt"
	"log/slog"
)

// Expirer expires one company's due jobs and says how many it expired. draft.Service implements it.
type Expirer interface {
	ExpireDue(ctx context.Context, tenantID string, limit int) (int, error)
}

// Sweeper walks every company in one region and expires what is due.
type Sweeper struct {
	// Tenants lists the company ids of the region.
	Tenants func(ctx context.Context) ([]string, error)
	Expirer Expirer
	// BatchSize bounds jobs per company per run; zero means 200. A company with more is finished next run.
	BatchSize int
	Logger    *slog.Logger
}

// Result counts what one run did.
type Result struct {
	Companies int
	Expired   int
	Errors    int
}

// Run expires due jobs for every company. One company failing does not stop the others; the run returns an
// error at the end if any failed.
func (s *Sweeper) Run(ctx context.Context) (Result, error) {
	var res Result
	ids, err := s.Tenants(ctx)
	if err != nil {
		return res, fmt.Errorf("expiry: list companies: %w", err)
	}
	batch := s.BatchSize
	if batch <= 0 {
		batch = 200
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.Companies++
		n, err := s.Expirer.ExpireDue(ctx, id, batch)
		res.Expired += n
		if err != nil {
			res.Errors++
			if s.Logger != nil {
				s.Logger.Error("expiry: company failed", "company", id, "error", err)
			}
		}
	}
	if res.Errors > 0 {
		return res, fmt.Errorf("expiry: %d of %d companies had errors", res.Errors, res.Companies)
	}
	return res, nil
}
