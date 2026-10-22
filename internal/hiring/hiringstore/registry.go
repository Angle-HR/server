package hiringstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/public"
)

const lookupJobSQL = `
SELECT public_id, region, organization_id::text, status, published_at, COALESCE(to_char(valid_through, 'YYYY-MM-DD'), '')
FROM hiring.job_registry WHERE public_id = $1`

// LookupJob finds the region and company that hold a public job. It implements public.Registry.
func (g *Global) LookupJob(ctx context.Context, publicID string) (hiringtypes.RegistryEntry, error) {
	var (
		e         hiringtypes.RegistryEntry
		published *time.Time
	)
	err := g.DB.QueryRow(ctx, lookupJobSQL, publicID).
		Scan(&e.PublicID, &e.Region, &e.OrganizationID, &e.Status, &published, &e.ValidThrough)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, fmt.Errorf("hiringstore: lookup job: %w", err)
	}
	if published != nil {
		e.PublishedAt = *published
	}
	return e, nil
}

// openJobsSQL lists published jobs that are still inside their closing date. The registry only knows ids,
// regions and dates, never job text, so the sitemap is built without touching a regional database.
const openJobsSQL = `
SELECT public_id, updated_at FROM hiring.job_registry
WHERE status = 'published' AND (valid_through IS NULL OR valid_through >= (now() AT TIME ZONE 'UTC')::date)
ORDER BY published_at DESC NULLS LAST, public_id
LIMIT $1::int`

// ListOpenJobs lists up to limit open jobs, newest first. It implements public.Registry.
func (g *Global) ListOpenJobs(ctx context.Context, limit int) ([]public.OpenJob, error) {
	if limit < 1 {
		limit = public.MaxSitemapURLs
	}
	rows, err := g.DB.Query(ctx, openJobsSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: list open jobs: %w", err)
	}
	defer rows.Close()
	out := []public.OpenJob{}
	for rows.Next() {
		var j public.OpenJob
		if err = rows.Scan(&j.PublicID, &j.UpdatedAt); err != nil {
			return nil, fmt.Errorf("hiringstore: scan open job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
