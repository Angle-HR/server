package bff

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/alicebob/miniredis/v2"
)

// Every path the monolith used to serve for hiring must reach the hiring service, never two upstreams.
func TestHiringPath(t *testing.T) {
	yes := []string{
		"/jobs", "/jobs/", "/jobs/counts", "/jobs/bulk", "/jobs/export", "/jobs/1", "/jobs/1/details", "/jobs/1/markets",
		"/jobs/1/application-form", "/jobs/1/preview", "/jobs/1/duplicates/dismiss", "/jobs/1/publish-check",
		"/jobs/1/publish", "/jobs/1/pause", "/jobs/1/resume", "/jobs/1/close", "/jobs/1/reopen", "/jobs/1/duplicate",
		"/jobs/1/share-kit", "/jobs/1/compliance", "/jobs/1/compliance/g1", "/jobs/1/members", "/jobs/1/disqualification-rules",
		"/jobs/applications",
		"/hiring/catalog", "/hiring/timezones", "/hiring/skills", "/hiring/me", "/hiring/people", "/hiring/departments",
		"/hiring/templates", "/hiring/templates/1/export", "/hiring/settings",
		"/organization/dpia",
		"/public/jobs/sitemap.xml", "/public/jobs/abc", "/public/companies/acme",
		"/j/abc", "/j/abc/privacy",
	}
	no := []string{
		// applications keep their own upstream
		"/public/jobs/abc/applications", "/public/jobs/abc/uploads", "/jobs/1/applications", "/jobs/1/applications/2/status",
		"/jobs/1/applications/2/files/3", "/hiring/applicant-settings",
		"/jobs/1/stages", "/jobs/1/stages/order", "/jobs/1/stages/2", "/jobs/1/applications/2/stage", "/jobs/1/applications/2/notes",
		"/jobs/1/applications/bulk/stage", "/hiring/tags", "/hiring/tags/3", "/hiring/disqualification-reasons", "/hiring/email-templates", "/hiring/email-templates/3", "/hiring/email-settings", "/hiring/interviews/mine", "/jobs/1/applications/compare", "/jobs/1/applications/2/interviews/3/cancel", "/hiring/offer-templates", "/hiring/offer-templates/3", "/jobs/1/applications/2/offers/3/send", "/public/offers/uk.t.s", "/public/offers/uk.t.s/sign", "/public/offers/uk.t.s/decline", "/public/offers/uk.t.s/document",
		"/jobs/1/applications/2/emails", "/jobs/1/applications/2/emails/preview",
		// everything else stays on the monolith
		"/auth/login", "/auth/me", "/organization", "/organization/members", "/organization/agreements", "/organization/privacy-contact", "/organization/agreements/x", "/public", "/public/jobs", "/public/companies", "/public/companies/acme/jobs",
		"/organization/profile",
		"/countries", "/admin/users", "/j", "/j/abc/other", "/jobsx", "/kyb/submit",
	}
	for _, p := range yes {
		if !HiringPath(p) {
			t.Errorf("%s should be routed to hiring", p)
		}
		if ApplicationsPath(p) || OrgPath(p) {
			t.Errorf("%s matches both hiring and another upstream", p)
		}
	}
	for _, p := range no {
		if HiringPath(p) {
			t.Errorf("%s must not be routed to hiring", p)
		}
	}
}

// With all four upstreams configured each request lands on exactly one of them, the most specific wins.
func TestRoutedToFourUpstreams(t *testing.T) {
	mk := func(name string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Upstream", name)
			_, _ = io.WriteString(w, "ok")
		}))
		t.Cleanup(s.Close)
		return s
	}
	mono, apps, hire, org := mk("monolith"), mk("applications"), mk("hiring"), mk("org")
	mr := miniredis.RunT(t)
	cfg := Config{
		Name: "t", UpstreamURL: mono.URL, ApplicationsURL: apps.URL, HiringURL: hire.URL, OrgURL: org.URL, APIPrefix: "/api/v1",
		MaxBodyBytes: 1024, UpstreamTimeout: 2 * time.Second, GlobalLimit: Rule{1000, time.Minute},
		Classes: DefaultClasses(), FailClosedAuth: true,
	}
	h, err := NewHandler(cfg, Deps{Limiter: &RedisLimiter{Client: redis.NewClient(&redis.Options{Addr: mr.Addr()}), Prefix: "t:"}})
	if err != nil {
		t.Fatal(err)
	}
	edge := httptest.NewServer(h)
	t.Cleanup(edge.Close)
	cases := map[string]string{
		"/api/v1/jobs":                            "hiring",
		"/api/v1/jobs/1/publish":                  "hiring",
		"/api/v1/jobs/1/applications":             "applications",
		"/api/v1/public/jobs/abc":                 "hiring",
		"/api/v1/public/jobs/abc/applications":    "applications",
		"/api/v1/public/jobs/sitemap.xml":         "hiring",
		"/api/v1/public/companies/acme":           "hiring",
		"/api/v1/organization/profile":            "org",
		"/api/v1/organization/profile/slug-check": "org",
		"/api/v1/hiring/settings":                 "hiring",
		"/api/v1/hiring/applicant-settings":       "applications",
		"/api/v1/organization/agreements":         "org",
		"/api/v1/organization/dpia":               "hiring",
		"/api/v1/countries":                       "org",
		"/j/abc":                                  "hiring",
		"/api/v1/auth/me":                         "monolith",
		"/api/v1/organization/other":              "monolith",
	}
	for path, want := range cases {
		resp, err := http.Get(edge.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("X-Upstream"); got != want {
			t.Errorf("%s went to %q, want %q", path, got, want)
		}
	}
}

// With no UPSTREAM_API_URL (the monolith is retired) a path no service claims is a 404 at the edge, and readiness
// follows the routed upstreams.
func TestNoCatchAllUpstream(t *testing.T) {
	hire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer hire.Close()
	f := newFixture(t, func(c *Config) { c.UpstreamURL = ""; c.HiringURL = hire.URL }, nil)
	if code := do(t, "GET", f.edge.URL+"/api/v1/jobs", "", nil).StatusCode; code != http.StatusOK {
		t.Fatalf("hiring route: %d", code)
	}
	if code := do(t, "GET", f.edge.URL+"/api/v1/nothing-here", "", nil).StatusCode; code != http.StatusNotFound {
		t.Fatalf("unowned route: %d, want 404", code)
	}
	if code := do(t, "GET", f.edge.URL+"/readyz", "", nil).StatusCode; code != http.StatusOK {
		t.Fatalf("readyz: %d", code)
	}
}

func TestDocsPagesGetTheirOwnCSP(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.Name = "web-bff"
		c.AppEnv = "development"
		c.PublicAPIURL = "http://localhost:8081"
	}, nil)
	resp := do(t, "GET", f.edge.URL+"/api/docs", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("docs: %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "cdn.jsdelivr.net") {
		t.Fatalf("docs CSP does not allow the viewer script: %q", csp)
	}
	if csp := do(t, "GET", f.edge.URL+"/healthz", "", nil).Header.Get("Content-Security-Policy"); csp != "default-src 'none'; frame-ancestors 'none'" {
		t.Fatalf("other routes must keep the lock-down policy: %q", csp)
	}
}
