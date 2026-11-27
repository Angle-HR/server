package hiringstore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/public"
)

// lookupSlugSQL reads org.company_slugs, which the org service owns and writes.
const lookupSlugSQL = `SELECT region, organization_id::text FROM org.company_slugs WHERE slug = $1`

// LookupSlug finds the region and company of a careers page address. It implements public.SlugRegistry.
func (g *Global) LookupSlug(ctx context.Context, slug string) (hiringtypes.RegistryEntry, error) {
	var e hiringtypes.RegistryEntry
	err := g.DB.QueryRow(ctx, lookupSlugSQL, slug).Scan(&e.Region, &e.OrganizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, fmt.Errorf("hiringstore: lookup slug: %w", err)
	}
	return e, nil
}

// openJobsFilterSQL is the WHERE of the careers page: published, shown on the career page, and inside the closing
// date. $1 is the tenant, $2 today. Each filter is optional through its parameter.
//
//	$3 team, $4 workplace, $5 employment type, $6 location, $7 title search
const openJobsFilterSQL = `
j.tenant_id = $1::uuid AND j.deleted_at IS NULL AND j.status = 'published' AND j.public_id IS NOT NULL
AND j.show_on_career_page
AND (j.closing_date IS NULL OR j.closing_date >= $2::date)`

const companyJobsSQL = `
SELECT j.public_id, COALESCE(j.title, ''), COALESCE(d.name, ''), COALESCE(j.location_text, ''),
       COALESCE(j.workplace_type, ''), COALESCE(j.employment_type, ''),
       COALESCE(j.published_at, j.created_at), COALESCE(to_char(j.closing_date, 'YYYY-MM-DD'), ''), j.id::text
FROM hiring.job_postings j
LEFT JOIN hiring.departments d ON d.id = j.department_id
WHERE ` + openJobsFilterSQL + `
  AND ($3 = '' OR d.name = $3)
  AND ($4 = '' OR j.workplace_type = $4)
  AND ($5 = '' OR j.employment_type = $5)
  AND ($6 = '' OR j.location_text ILIKE '%' || $6 || '%')
  AND ($7 = '' OR j.title ILIKE '%' || $7 || '%')
  AND ($8::timestamptz IS NULL OR (COALESCE(j.published_at, j.created_at), j.id) < ($8::timestamptz, NULLIF($9, '')::uuid))
ORDER BY COALESCE(j.published_at, j.created_at) DESC, j.id DESC
LIMIT $10::int`

const companyJobsCountSQL = `
SELECT count(*) FROM hiring.job_postings j
LEFT JOIN hiring.departments d ON d.id = j.department_id
WHERE ` + openJobsFilterSQL + `
  AND ($3 = '' OR d.name = $3)
  AND ($4 = '' OR j.workplace_type = $4)
  AND ($5 = '' OR j.employment_type = $5)
  AND ($6 = '' OR j.location_text ILIKE '%' || $6 || '%')
  AND ($7 = '' OR j.title ILIKE '%' || $7 || '%')`

const companyTeamsSQL = `
SELECT DISTINCT d.name FROM hiring.job_postings j
JOIN hiring.departments d ON d.id = j.department_id
WHERE ` + openJobsFilterSQL + ` ORDER BY d.name`

// likeEscape stops a search term from acting as a LIKE pattern.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// encodeCursor and decodeCursor carry the sort key of the last job of a page: its time and id.
func encodeCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(c string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", public.ErrBadQuery
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, "", public.ErrBadQuery
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil || len(id) != 36 {
		return time.Time{}, "", public.ErrBadQuery
	}
	return t, id, nil
}

// LoadCompanyPage reads a company's public profile and its open jobs. It implements public.CompanyReader.
func (s *Store) LoadCompanyPage(ctx context.Context, tenantID string, q public.CompanyQuery) (*public.CompanyPage, error) {
	var (
		curT  *time.Time
		curID string
	)
	if q.Cursor != "" {
		t, id, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, err
		}
		curT, curID = &t, id
	}
	var out *public.CompanyPage
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		prof, err := s.companyProfile(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		if prof.Slug == "" {
			return ErrNotFound
		}
		page := &public.CompanyPage{Company: public.CompanyProfile{
			Slug: prof.Slug, Name: prof.Name, About: prof.About, Website: prof.Website, LogoURL: prof.LogoURL,
		}, Jobs: []public.CompanyJob{}, Teams: []string{}}
		if prof.DisplayName != "" {
			page.Company.Name = prof.DisplayName
		}
		search, location := likeEscape(q.Search), likeEscape(q.Location)
		if err = tx.QueryRow(ctx, companyJobsCountSQL, tenantID, q.Today, q.Team, q.Workplace, q.EmploymentType,
			location, search).Scan(&page.Total); err != nil {
			return fmt.Errorf("hiringstore: count company jobs: %w", err)
		}
		rows, err := tx.Query(ctx, companyJobsSQL, tenantID, q.Today, q.Team, q.Workplace, q.EmploymentType,
			location, search, curT, curID, q.Limit+1)
		if err != nil {
			return fmt.Errorf("hiringstore: list company jobs: %w", err)
		}
		type row struct {
			public.CompanyJob
			id string
		}
		var got []row
		for rows.Next() {
			var r row
			if err = rows.Scan(&r.PublicID, &r.Title, &r.Team, &r.Location, &r.Workplace, &r.EmploymentType,
				&r.PublishedAt, &r.ClosingDate, &r.id); err != nil {
				rows.Close()
				return fmt.Errorf("hiringstore: scan company job: %w", err)
			}
			got = append(got, r)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return fmt.Errorf("hiringstore: company jobs: %w", err)
		}
		if len(got) > q.Limit {
			last := got[q.Limit-1]
			page.NextCursor = encodeCursor(last.PublishedAt, last.id)
			got = got[:q.Limit]
		}
		for _, r := range got {
			page.Jobs = append(page.Jobs, r.CompanyJob)
		}
		trows, err := tx.Query(ctx, companyTeamsSQL, tenantID, q.Today)
		if err != nil {
			return fmt.Errorf("hiringstore: company teams: %w", err)
		}
		defer trows.Close()
		for trows.Next() {
			var name string
			if err = trows.Scan(&name); err != nil {
				return fmt.Errorf("hiringstore: scan team: %w", err)
			}
			page.Teams = append(page.Teams, name)
		}
		if err = trows.Err(); err != nil {
			return err
		}
		out = page
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
