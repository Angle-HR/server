package public_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/hiring/questions"
)

type fakeRegistry struct {
	entries map[string]hiringtypes.RegistryEntry
	open    []public.OpenJob
}

func (f *fakeRegistry) LookupJob(_ context.Context, id string) (hiringtypes.RegistryEntry, error) {
	e, ok := f.entries[id]
	if !ok {
		return e, public.ErrNotFound
	}
	return e, nil
}

func (f *fakeRegistry) ListOpenJobs(_ context.Context, limit int) ([]public.OpenJob, error) {
	if len(f.open) > limit {
		return f.open[:limit], nil
	}
	return f.open, nil
}

type fakeRegional struct{ postings map[string]*public.Posting }

func (f *fakeRegional) LoadPosting(_ context.Context, _, id string) (*public.Posting, error) {
	p, ok := f.postings[id]
	if !ok {
		return nil, public.ErrNotFound
	}
	return p, nil
}

type fakeDir struct{ r public.Regional }

func (d fakeDir) Regional(string) (public.Regional, error) { return d.r, nil }

func i64(v int64) *int64 { return &v }

func posting(status string) *public.Posting {
	desc := jobs.Sections{"role": json.RawMessage(`{"type":"doc","content":[{"type":"text","text":"Build things."}]}`)}
	j := jobs.Job{
		ID: "job-1", JobNumber: 7, JobCode: "JB-7", PublicID: "jabcdefghijkl", Status: status,
		PublishedAt: "2026-10-01T09:30:00.000000Z",
		Details: jobs.Details{
			Title: "Engineer", ClosingDate: "2026-12-31", EmploymentType: "full_time", WorkplaceType: "onsite",
			Description: desc, LawfulBasis: "legitimate_interests", RetentionMonths: 6,
			Markets: []jobs.MarketSel{{MarketCode: "UK", City: "London"}},
			Pay:     jobs.Pay{Type: "range", Min: i64(4000000), Max: i64(5000000), Currency: "GBP", Period: "year", Visible: true},
		},
	}
	return &public.Posting{
		Job:         j,
		FormVersion: 1,
		Company:     public.Company{Name: "Acme Ltd", PrivacyEmail: "privacy@acme.test"},
		Form: []questions.FormQuestion{
			{ID: "q1", Section: "personal_information", Type: "short_text", Label: "Full name", Required: true, SystemKey: "full_name"},
		},
	}
}

func newService(p *public.Posting, now time.Time) *public.Service {
	reg := &fakeRegistry{entries: map[string]hiringtypes.RegistryEntry{
		"jabcdefghijkl": {PublicID: "jabcdefghijkl", Region: "uk", OrganizationID: "org-1", Status: "published"},
	}}
	return &public.Service{
		Registry:  reg,
		Directory: fakeDir{r: &fakeRegional{postings: map[string]*public.Posting{"jabcdefghijkl": p}}},
		SiteURL:   "https://jobs.example.test",
		Now:       func() time.Time { return now },
	}
}

var day = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func TestStateOf(t *testing.T) {
	cases := []struct {
		status, closing string
		now             time.Time
		want            public.State
		ok              bool
	}{
		{jobs.StatusPublished, "", day, public.StateOpen, true},
		{jobs.StatusPublished, "2026-10-16", day, public.StateOpen, true}, // last day to apply
		{jobs.StatusPublished, "2026-10-15", day, public.StateClosed, true},
		{jobs.StatusPaused, "2026-12-01", day, public.StatePaused, true},
		{jobs.StatusClosed, "", day, public.StateClosed, true},
		{jobs.StatusExpired, "", day, public.StateClosed, true},
		{jobs.StatusDraft, "", day, "", false},
		{jobs.StatusArchived, "", day, "", false},
	}
	for _, c := range cases {
		got, ok := public.StateOf(c.status, c.closing, c.now)
		if got != c.want || ok != c.ok {
			t.Errorf("StateOf(%q,%q) = %q,%v want %q,%v", c.status, c.closing, got, ok, c.want, c.ok)
		}
	}
}

func TestLoadStates(t *testing.T) {
	ctx := context.Background()
	s := newService(posting(jobs.StatusPublished), day)
	v, err := s.Load(ctx, "jabcdefghijkl")
	if err != nil || v.State != public.StateOpen || !v.Accepting() {
		t.Fatalf("open job: %+v %v", v, err)
	}
	if v.PublishedAt != "2026-10-01T09:30:00Z" || v.Job.Company != "Acme Ltd" || v.FormVersion != 1 {
		t.Errorf("view fields: %+v", v)
	}

	if _, err = s.Load(ctx, "jnosuchjob123"); !errors.Is(err, public.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if _, err = s.Load(ctx, "bad id!"); !errors.Is(err, public.ErrNotFound) {
		t.Errorf("malformed id should not reach the registry: %v", err)
	}
	if _, err = newService(posting(jobs.StatusArchived), day).Load(ctx, "jabcdefghijkl"); !errors.Is(err, public.ErrGone) {
		t.Errorf("archived: %v", err)
	}
	if _, err = newService(posting(jobs.StatusDraft), day).Load(ctx, "jabcdefghijkl"); !errors.Is(err, public.ErrNotFound) {
		t.Errorf("draft: %v", err)
	}
	v, err = newService(posting(jobs.StatusPaused), day).Load(ctx, "jabcdefghijkl")
	if err != nil || v.State != public.StatePaused || v.Accepting() {
		t.Errorf("paused: %+v %v", v, err)
	}
	late := day.AddDate(0, 3, 0) // after the closing date, before the expiry task has run
	v, err = newService(posting(jobs.StatusPublished), late).Load(ctx, "jabcdefghijkl")
	if err != nil || v.State != public.StateClosed {
		t.Errorf("past closing date: %+v %v", v, err)
	}
}

func TestRenderPageOpenJob(t *testing.T) {
	s := newService(posting(jobs.StatusPublished), day)
	v, _ := s.Load(context.Background(), "jabcdefghijkl")
	out, err := public.RenderPage(v, s.JobURL(v.PublicID), s.ApplyLink(v.PublicID, ""), s.JobURL(v.PublicID)+"/privacy")
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	for _, want := range []string{
		`<title>Engineer at Acme Ltd</title>`,
		`og:title`,
		`application/ld+json`,
		`"validThrough":"2026-12-31T23:59:59Z"`,
		`"employmentType":"FULL_TIME"`,
		`href="https://jobs.example.test/j/jabcdefghijkl/apply"`,
		`GBP 40,000 to 50,000 per year`,
		`Build things.`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Contains(html, `name="robots"`) {
		t.Error("an open job must be indexable")
	}
}

func TestRenderPageClosedJobIsNotOfferedToGoogle(t *testing.T) {
	for _, status := range []string{jobs.StatusPaused, jobs.StatusClosed, jobs.StatusExpired} {
		s := newService(posting(status), day)
		v, err := s.Load(context.Background(), "jabcdefghijkl")
		if err != nil {
			t.Fatal(err)
		}
		out, _ := public.RenderPage(v, s.JobURL(v.PublicID), s.ApplyLink(v.PublicID, ""), "/p")
		html := string(out)
		if strings.Contains(html, "application/ld+json") {
			t.Errorf("%s: closed job must not carry JobPosting markup", status)
		}
		if !strings.Contains(html, `content="noindex"`) {
			t.Errorf("%s: closed job must be noindex", status)
		}
		if strings.Contains(html, "Apply for this role") {
			t.Errorf("%s: no apply button on a job that is not accepting applications", status)
		}
	}
}

func TestRenderPageEscapesEmployerText(t *testing.T) {
	p := posting(jobs.StatusPublished)
	p.Job.Title = `</script><script>alert(1)</script>`
	p.Company.Name = `Acme <b>"Ltd"</b>`
	p.Job.Description["role"] = json.RawMessage(`{"text":"<img src=x onerror=alert(1)>"}`)
	s := newService(p, day)
	v, _ := s.Load(context.Background(), "jabcdefghijkl")
	out, err := public.RenderPage(v, s.JobURL(v.PublicID), s.ApplyLink(v.PublicID, ""), "/p")
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	if strings.Contains(html, "<script>alert(1)") || strings.Contains(html, "<img src=x") || strings.Contains(html, "<b>") {
		t.Errorf("employer text was not escaped:\n%s", html)
	}
	// The JSON-LD block must hold exactly one closing script tag.
	if n := strings.Count(html, "</script>"); n != 1 {
		t.Errorf("found %d closing script tags, want 1", n)
	}
}

func TestJobPostingLDRemoteJobAndExactPay(t *testing.T) {
	p := posting(jobs.StatusPublished)
	p.Job.WorkplaceType = "remote"
	p.Job.Pay = jobs.Pay{Type: "exact", Min: i64(250000), Currency: "USD", Period: "hour", Visible: true}
	p.Job.Markets = []jobs.MarketSel{{MarketCode: "US"}, {MarketCode: "EU"}}
	v, _ := newService(p, day).Load(context.Background(), "jabcdefghijkl")
	ld := public.JobPostingLD(v)
	if ld["jobLocationType"] != "TELECOMMUTE" {
		t.Errorf("jobLocationType = %v", ld["jobLocationType"])
	}
	if _, has := ld["jobLocation"]; has {
		t.Error("a remote job has no jobLocation")
	}
	reqs, _ := ld["applicantLocationRequirements"].([]any)
	if len(reqs) != 1 { // EU is a bloc and must not be claimed as a country
		t.Errorf("applicantLocationRequirements = %v", reqs)
	}
	raw, _ := json.Marshal(ld["baseSalary"])
	if !strings.Contains(string(raw), `"value":2500`) || !strings.Contains(string(raw), `"unitText":"HOUR"`) {
		t.Errorf("baseSalary = %s", raw)
	}
}

func TestNoticeReflectsTheJob(t *testing.T) {
	p := posting(jobs.StatusPublished)
	p.Job.RetentionMonths = 12
	p.Job.LawfulBasis = "consent"
	p.Declarations = []questions.Declaration{{Category: "health_disability", LegalCondition: "x", Purpose: "Plan adjustments"}}
	p.Form = append(p.Form,
		questions.FormQuestion{ID: "q2", Section: "screening", Type: "short_text", Label: "Why <us>?"},
		questions.FormQuestion{ID: "q3", Section: "screening", Type: "single_choice", Label: "Disability?", SpecialCategory: "health_disability"},
		questions.FormQuestion{ID: "q4", Section: "screening", Type: "number", Label: "Years", Knockout: json.RawMessage(`{"operator":"lt","value":2}`)},
	)
	s := newService(p, day)
	v, _ := s.Load(context.Background(), "jabcdefghijkl")
	out, err := public.RenderNotice(v, s.JobURL(v.PublicID))
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	for _, want := range []string{
		"Up to 12 months", "You have given your consent", "privacy@acme.test", "health or disability: Plan adjustments",
		"No application is rejected automatically", "stored in the United Kingdom", public.NoticeVersion,
		"Why &lt;us&gt;?",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("notice is missing %q", want)
		}
	}
	if strings.Contains(html, "Disability?") {
		t.Error("a special category question is listed under its own heading, not as ordinary data")
	}
}

func TestShareKitCarriesSourcePerChannel(t *testing.T) {
	kit := public.BuildShareKit("https://jobs.example.test/", "jabcdefghijkl", "Engineer", "Acme")
	if kit.JobURL != "https://jobs.example.test/j/jabcdefghijkl" || len(kit.Links) != 6 {
		t.Fatalf("kit: %+v", kit)
	}
	for _, l := range kit.Links {
		if !public.ValidSource(l.Channel) {
			t.Errorf("channel %q is not a valid source", l.Channel)
		}
		if l.Channel != "email" && !strings.Contains(l.URL, "src%3D"+l.Channel) {
			t.Errorf("%s link does not carry its source: %s", l.Channel, l.URL)
		}
	}
	if !public.ValidSource("direct") || public.ValidSource("javascript:alert(1)") || public.ValidSource("") {
		t.Error("ValidSource accepts the wrong values")
	}
}

func TestSitemap(t *testing.T) {
	out, err := public.RenderSitemap("https://jobs.example.test", []public.OpenJob{
		{PublicID: "jaaa", UpdatedAt: day}, {PublicID: "jbbb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`<?xml version="1.0"`, `xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"`,
		`<loc>https://jobs.example.test/j/jaaa</loc>`, `<lastmod>2026-10-16</lastmod>`, `<loc>https://jobs.example.test/j/jbbb</loc>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("sitemap is missing %q:\n%s", want, s)
		}
	}
}
