package hiringstore_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/orgstore"
	"github.com/Angle-HR/server/internal/region"
)

// The careers page against real databases. It runs when these are set, and is skipped otherwise:
//
//	TEST_REGIONAL_ADMIN_DB  a superuser connection to a migrated regional database (seeds the rows)
//	TEST_REGIONAL_DB        the same database as a role without BYPASSRLS, as the services connect
//	TEST_GLOBAL_DB          a migrated global registry database, as that role
func TestCompanyPageIntegration(t *testing.T) {
	adminURL, appURL, globalURL := os.Getenv("TEST_REGIONAL_ADMIN_DB"), os.Getenv("TEST_REGIONAL_DB"), os.Getenv("TEST_GLOBAL_DB")
	if adminURL == "" || appURL == "" || globalURL == "" {
		t.Skip("TEST_REGIONAL_ADMIN_DB, TEST_REGIONAL_DB and TEST_GLOBAL_DB are not set")
	}
	ctx := context.Background()
	open := func(url string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	admin, app, global := open(adminURL), open(appURL), open(globalURL)
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Two companies, each with its own owner (one company per owner).
	var userID, acme, other string
	user := func(email string) string {
		var id string
		if err := admin.QueryRow(ctx, `INSERT INTO accounts.users (email, password_hash) VALUES ($1, 'x') RETURNING id::text`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	userID, otherOwner := user("it-owner@example.test"), user("it-owner2@example.test")
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DELETE FROM accounts.organizations WHERE owner_user_id IN ($1::uuid, $2::uuid)`, userID, otherOwner)
		_, _ = admin.Exec(ctx, `DELETE FROM accounts.users WHERE id IN ($1::uuid, $2::uuid)`, userID, otherOwner)
		_, _ = global.Exec(ctx, `DELETE FROM org.company_slugs WHERE slug LIKE 'it-%'`)
	})
	for _, c := range []struct {
		owner, name string
		dst         *string
	}{{userID, "IT Acme Ltd", &acme}, {otherOwner, "IT Other Ltd", &other}} {
		if err := admin.QueryRow(ctx, `INSERT INTO accounts.organizations (owner_user_id, legal_name, company_role_id)
			VALUES ($1::uuid, $2, gen_random_uuid()) RETURNING id::text`, c.owner, c.name).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}
	var dept string
	if err := admin.QueryRow(ctx, `INSERT INTO hiring.departments (tenant_id, name) VALUES ($1::uuid, 'Engineering') RETURNING id::text`, acme).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	job := func(n int, publicID, title, status, closing string, show bool, mode string, age time.Duration) {
		t.Helper()
		must(admin.Exec(ctx, `INSERT INTO hiring.job_postings
			(tenant_id, created_by, job_number, public_id, status, title, department_id, closing_date, show_on_career_page,
			 workplace_type, employment_type, location_text, published_at)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::uuid, NULLIF($8, '')::date, $9, $10, 'full_time', 'London', now() - $11::interval)`,
			acme, userID, n, publicID, status, title, dept, closing, show, mode, age.String()))
	}
	job(1, "it-open-1", "Backend Engineer", "published", "", true, "remote", 3*time.Hour)
	job(2, "it-open-2", "Frontend Engineer", "published", "2099-01-01", true, "hybrid", 2*time.Hour)
	job(3, "it-open-3", "Designer", "published", "", true, "onsite", time.Hour)
	job(4, "it-draft", "Draft Role", "draft", "", true, "remote", time.Hour)
	job(5, "it-hidden", "Hidden Role", "published", "", false, "remote", time.Hour)
	job(6, "it-expired", "Expired Role", "published", "2000-01-01", true, "remote", time.Hour)
	job(7, "it-paused", "Paused Role", "paused", "", true, "remote", time.Hour)

	slugs := orgstore.GlobalSlugs{DB: global}
	st := &orgstore.Store{Slugs: slugs, Regional: func(region.Region) (orgstore.DB, error) { return app, nil }}

	// Choosing a slug, then another company asking for it.
	prof := orgstore.Profile{Slug: "it-acme", DisplayName: "Acme", About: "We build things.", Website: "https://acme.example"}
	if err := st.SetProfile(ctx, region.Region("uk"), acme, prof); err != nil {
		t.Fatalf("set profile: %v", err)
	}
	if err := st.SetProfile(ctx, region.Region("uk"), other, orgstore.Profile{Slug: "it-acme"}); !errors.Is(err, orgstore.ErrSlugTaken) {
		t.Fatalf("a taken slug: got %v, want ErrSlugTaken", err)
	}
	if got, err := st.Profile(ctx, region.Region("uk"), other); err != nil || got.Slug != "" {
		t.Fatalf("the refused company must keep no slug: %+v %v", got, err)
	}
	// A rename frees the old address and the registry follows.
	if err := st.SetProfile(ctx, region.Region("uk"), acme, orgstore.Profile{Slug: "it-acme-hr", DisplayName: "Acme", About: "We build things.", Website: "https://acme.example"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := slugs.Lookup(ctx, "it-acme"); !errors.Is(err, orgstore.ErrNotFound) {
		t.Fatalf("old slug should be gone, got %v", err)
	}
	if err := st.SetProfile(ctx, region.Region("uk"), other, orgstore.Profile{Slug: "it-acme"}); err != nil {
		t.Fatalf("the freed slug should be takeable: %v", err)
	}
	if ok, why, err := st.SlugAvailable(ctx, acme, "it-acme"); err != nil || ok || why == "" {
		t.Fatalf("availability of a slug another company has: %v %q %v", ok, why, err)
	}

	// The public page.
	g := &hiringstore.Global{DB: global}
	entry, err := g.LookupSlug(ctx, "it-acme-hr")
	if err != nil || entry.OrganizationID != acme {
		t.Fatalf("lookup: %+v %v", entry, err)
	}
	if _, err = g.LookupSlug(ctx, "it-nope"); !errors.Is(err, hiringstore.ErrNotFound) {
		t.Fatalf("unknown slug: %v", err)
	}
	hs := &hiringstore.Store{DB: app, Region: "uk"}
	page := func(q public.CompanyQuery) *public.CompanyPage {
		t.Helper()
		if err := q.Normalize(time.Now()); err != nil {
			t.Fatal(err)
		}
		p, err := hs.LoadCompanyPage(ctx, acme, q)
		if err != nil {
			t.Fatalf("load page: %v", err)
		}
		return p
	}

	all := page(public.CompanyQuery{})
	if all.Company.Name != "Acme" || all.Company.About != "We build things." || all.Company.Slug != "it-acme-hr" {
		t.Errorf("profile: %+v", all.Company)
	}
	if all.Total != 3 || len(all.Jobs) != 3 {
		t.Fatalf("open jobs: total %d, listed %d (draft, hidden, expired and paused must not show)", all.Total, len(all.Jobs))
	}
	if all.Jobs[0].PublicID != "it-open-3" || all.Jobs[2].PublicID != "it-open-1" {
		t.Errorf("newest first: %v %v %v", all.Jobs[0].PublicID, all.Jobs[1].PublicID, all.Jobs[2].PublicID)
	}
	if len(all.Teams) != 1 || all.Teams[0] != "Engineering" {
		t.Errorf("teams: %v", all.Teams)
	}
	if got := page(public.CompanyQuery{Workplace: "remote"}); got.Total != 1 || got.Jobs[0].PublicID != "it-open-1" {
		t.Errorf("workplace filter: %+v", got.Jobs)
	}
	if got := page(public.CompanyQuery{Search: "engineer"}); got.Total != 2 {
		t.Errorf("search: %d", got.Total)
	}
	if got := page(public.CompanyQuery{Search: "100%"}); got.Total != 0 {
		t.Errorf("a percent sign must not match everything: %d", got.Total)
	}
	if got := page(public.CompanyQuery{Team: "Sales"}); got.Total != 0 || len(got.Jobs) != 0 {
		t.Errorf("unknown team: %+v", got)
	}
	// Paging: one at a time, no job twice, none missed.
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 5; i++ {
		p := page(public.CompanyQuery{Limit: 1, Cursor: cursor})
		for _, j := range p.Jobs {
			if seen[j.PublicID] {
				t.Fatalf("%s listed twice", j.PublicID)
			}
			seen[j.PublicID] = true
		}
		if p.Total != 3 {
			t.Errorf("total on a later page: %d", p.Total)
		}
		if cursor = p.NextCursor; cursor == "" {
			break
		}
	}
	if len(seen) != 3 {
		t.Errorf("paging saw %v", seen)
	}

	// A company with no slug has no page; row-level security hides other companies' jobs.
	if _, err = hs.LoadCompanyPage(ctx, userID, public.CompanyQuery{Today: "2026-01-01", Limit: 5}); err == nil {
		t.Error("an unknown company must not load")
	}
	if p, err := hs.LoadCompanyPage(ctx, other, public.CompanyQuery{Today: "2026-01-01", Limit: 5}); err != nil || p.Total != 0 {
		t.Errorf("the other company has no open jobs: %+v %v", p, err)
	}
}
