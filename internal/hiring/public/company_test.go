package public

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
)

type fakeSlugs map[string]hiringtypes.RegistryEntry

func (f fakeSlugs) LookupSlug(_ context.Context, slug string) (hiringtypes.RegistryEntry, error) {
	if e, ok := f[slug]; ok {
		return e, nil
	}
	return hiringtypes.RegistryEntry{}, ErrNotFound
}

type fakeRegional struct {
	page  *CompanyPage
	query CompanyQuery
}

func (f *fakeRegional) LoadPosting(context.Context, string, string) (*Posting, error) {
	return nil, ErrNotFound
}

func (f *fakeRegional) LoadCompanyPage(_ context.Context, _ string, q CompanyQuery) (*CompanyPage, error) {
	f.query = q
	return f.page, nil
}

type onlyPosting struct{}

func (onlyPosting) LoadPosting(context.Context, string, string) (*Posting, error) {
	return nil, ErrNotFound
}

type fakeDir struct{ r Regional }

func (d fakeDir) Regional(string) (Regional, error) { return d.r, nil }

func companyService(r Regional) *Service {
	return &Service{
		Slugs:     fakeSlugs{"acme": {Region: "uk", OrganizationID: "org-1"}},
		Directory: fakeDir{r},
		Now:       func() time.Time { return time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC) },
	}
}

func TestCompanyUnknownSlug(t *testing.T) {
	s := companyService(&fakeRegional{})
	for _, slug := range []string{"nope", "", "  ", "a-very-long-slug-that-is-over-the-limit-of-forty"} {
		if _, err := s.Company(context.Background(), slug, CompanyQuery{}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: got %v, want ErrNotFound", slug, err)
		}
	}
}

func TestCompanyOffWithoutRegistry(t *testing.T) {
	s := companyService(&fakeRegional{})
	s.Slugs = nil
	if _, err := s.Company(context.Background(), "acme", CompanyQuery{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestCompanyRegionWithoutReader(t *testing.T) {
	s := companyService(onlyPosting{})
	if _, err := s.Company(context.Background(), "acme", CompanyQuery{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestCompanyRejectsUnknownFilters(t *testing.T) {
	s := companyService(&fakeRegional{})
	for _, q := range []CompanyQuery{{Workplace: "moon"}, {EmploymentType: "gig"}} {
		if _, err := s.Company(context.Background(), "acme", q); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%+v: got %v, want ErrBadQuery", q, err)
		}
	}
}

func TestCompanyServesPageAndNormalizesQuery(t *testing.T) {
	fr := &fakeRegional{page: &CompanyPage{Company: CompanyProfile{Slug: "acme", Name: "Acme"}}}
	s := companyService(fr)
	page, err := s.Company(context.Background(), " ACME ", CompanyQuery{Team: " Eng ", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if page.Company.Name != "Acme" {
		t.Errorf("got %+v", page)
	}
	if fr.query.Team != "Eng" || fr.query.Limit != MaxCompanyPageSize || fr.query.Today != "2026-11-27" {
		t.Errorf("query not normalized: %+v", fr.query)
	}
}

func TestCompanyDefaultPageSize(t *testing.T) {
	fr := &fakeRegional{page: &CompanyPage{Company: CompanyProfile{Slug: "acme"}}}
	if _, err := companyService(fr).Company(context.Background(), "acme", CompanyQuery{}); err != nil {
		t.Fatal(err)
	}
	if fr.query.Limit != DefaultCompanyPageSize {
		t.Errorf("limit %d", fr.query.Limit)
	}
}

// A regional slug that no longer matches the registry (a rename racing the read) must not serve the old page.
func TestCompanyStaleSlugIsNotFound(t *testing.T) {
	fr := &fakeRegional{page: &CompanyPage{Company: CompanyProfile{Slug: "renamed"}}}
	if _, err := companyService(fr).Company(context.Background(), "acme", CompanyQuery{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}
