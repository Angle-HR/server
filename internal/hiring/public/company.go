package public

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
)

// Careers page paging.
const (
	DefaultCompanyPageSize = 20
	MaxCompanyPageSize     = 50
)

// SlugRegistry finds a company by its careers page address in the global database.
type SlugRegistry interface {
	// LookupSlug returns the region and company of a slug; ErrNotFound for an unknown slug.
	LookupSlug(ctx context.Context, slug string) (hiringtypes.RegistryEntry, error)
}

// CompanyReader is the part of a region's database the careers page needs. A Regional that also implements it
// serves /public/companies/{slug}.
type CompanyReader interface {
	// LoadCompanyPage reads the company's public profile and its open jobs. ErrNotFound when the company has no
	// public profile (no slug) or does not exist.
	LoadCompanyPage(ctx context.Context, tenantID string, q CompanyQuery) (*CompanyPage, error)
}

// CompanyQuery filters and pages the open jobs. Empty fields do not filter. Today is YYYY-MM-DD in UTC: a job is
// open through its closing date.
type CompanyQuery struct {
	Team           string
	Workplace      string // onsite, hybrid or remote
	EmploymentType string // full_time, part_time, contract or internship
	Location       string // matched against the job's location text, case-insensitive
	Search         string // matched against the title, case-insensitive
	Limit          int
	Cursor         string
	Today          string
}

// CompanyProfile is what a company tells the public about itself.
type CompanyProfile struct {
	Slug    string
	Name    string // the display name, else the legal name
	About   string
	Website string
	LogoURL string
}

// CompanyJob is one open job in the careers page list.
type CompanyJob struct {
	PublicID       string
	Title          string
	Team           string
	Location       string
	Workplace      string
	EmploymentType string
	PublishedAt    time.Time
	ClosingDate    string // YYYY-MM-DD, "" when open-ended
}

// CompanyPage is the careers page of one company.
type CompanyPage struct {
	Company CompanyProfile
	Jobs    []CompanyJob
	// Teams are the departments that have an open job, for the filter. They ignore the query's filters.
	Teams []string
	// Total counts the jobs that match the filters, across all pages.
	Total int
	// NextCursor is empty on the last page.
	NextCursor string
}

// Valid filter values, so an unknown one is a client error instead of an empty page.
var (
	workplaceValues  = map[string]bool{"onsite": true, "hybrid": true, "remote": true}
	employmentValues = map[string]bool{"full_time": true, "part_time": true, "contract": true, "internship": true}
)

// ErrBadQuery means a filter value is not one the page knows.
var ErrBadQuery = errors.New("public: invalid careers page query")

// Normalize trims the query, applies the paging defaults and rejects unknown filter values.
func (q *CompanyQuery) Normalize(now time.Time) error {
	q.Team = strings.TrimSpace(q.Team)
	q.Location = strings.TrimSpace(q.Location)
	q.Search = strings.TrimSpace(q.Search)
	if q.Workplace != "" && !workplaceValues[q.Workplace] {
		return fmt.Errorf("%w: workplace", ErrBadQuery)
	}
	if q.EmploymentType != "" && !employmentValues[q.EmploymentType] {
		return fmt.Errorf("%w: employment_type", ErrBadQuery)
	}
	if len(q.Team) > 100 || len(q.Location) > 100 || len(q.Search) > 100 {
		return fmt.Errorf("%w: filter too long", ErrBadQuery)
	}
	switch {
	case q.Limit <= 0:
		q.Limit = DefaultCompanyPageSize
	case q.Limit > MaxCompanyPageSize:
		q.Limit = MaxCompanyPageSize
	}
	q.Today = now.UTC().Format("2006-01-02")
	return nil
}

// Company reads a careers page by the company's slug. It returns ErrNotFound for an unknown slug, a company that
// has not chosen one, or a region whose store cannot serve the page.
func (s *Service) Company(ctx context.Context, slug string, q CompanyQuery) (*CompanyPage, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" || len(slug) > 40 || s.Slugs == nil {
		return nil, ErrNotFound
	}
	if err := q.Normalize(s.now()); err != nil {
		return nil, err
	}
	entry, err := s.Slugs.LookupSlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	reg, err := s.Directory.Regional(entry.Region)
	if err != nil {
		return nil, fmt.Errorf("public: region %q: %w", entry.Region, err)
	}
	cr, ok := reg.(CompanyReader)
	if !ok {
		return nil, ErrNotFound
	}
	page, err := cr.LoadCompanyPage(ctx, entry.OrganizationID, q)
	if err != nil {
		return nil, err
	}
	// The registry row is the source of truth for the address: a stale regional slug must not serve another page.
	if page.Company.Slug != slug {
		return nil, ErrNotFound
	}
	return page, nil
}
