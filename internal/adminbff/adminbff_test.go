package adminbff

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/bff"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigDefaults(t *testing.T) {
	c, err := LoadConfig(env(map[string]string{"UPSTREAM_API_URL": "http://server:8080"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "admin-bff" || c.ListenAddr != ":8080" {
		t.Fatalf("name/listen = %q %q", c.Name, c.ListenAddr)
	}
	if c.CookieName != "__Host-openhr_admin_session" {
		t.Fatalf("secure cookie = %q", c.CookieName)
	}
	if c.AdminClasses["ADMIN_LOGIN"].IP.Max != 10 || c.AdminClasses["ADMIN_LOGIN"].Email.Max != 5 {
		t.Fatalf("login class = %+v", c.AdminClasses["ADMIN_LOGIN"])
	}
	if len(c.Classes) != 0 {
		t.Fatal("product classes must not apply")
	}
}

func TestConfigOverrides(t *testing.T) {
	c, err := LoadConfig(env(map[string]string{
		"UPSTREAM_API_URL": "http://server:8080", "COOKIE_SECURE": "false", "LISTEN_ADDR": ":9",
		"RATE_LIMIT_ADMIN_LOGIN": "3/1m", "ALLOWED_ORIGINS": "http://localhost:3001",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.CookieName != "openhr_admin_session" || c.ListenAddr != ":9" || c.AdminClasses["ADMIN_LOGIN"].IP.Max != 3 {
		t.Fatalf("%+v", c)
	}
	if len(c.AllowedOrigins) != 1 {
		t.Fatalf("origins = %v", c.AllowedOrigins)
	}
	if _, err := LoadConfig(env(map[string]string{"UPSTREAM_API_URL": "http://x", "RATE_LIMIT_ADMIN_LOGIN": "bad"})); err == nil {
		t.Fatal("bad rule accepted")
	}
	if _, err := LoadConfig(env(nil)); err == nil {
		t.Fatal("missing upstream accepted")
	}
}

func TestClassAndPublic(t *testing.T) {
	cases := []struct {
		m, p, class string
		public      bool
	}{
		{"POST", "/api/v1/admin/auth/login", "ADMIN_LOGIN", true},
		{"POST", "/api/v1/admin/auth/login/totp", "ADMIN_LOGIN_TOTP", true},
		{"POST", "/api/v1/admin/auth/accept-invite", "ADMIN_ACCEPT_INVITE", true},
		{"GET", "/api/v1/admin/auth/invite/abc", "ADMIN_INVITE_PREVIEW", true},
		{"POST", "/api/v1/admin/auth/logout", "", true},
		{"GET", "/api/v1/admin/auth/me", "", false},
		{"POST", "/api/v1/admin/auth/totp/enroll", "", false},
		{"POST", "/api/v1/admin/auth/totp/confirm", "", false},
		{"POST", "/api/v1/admin/auth/totp/disable", "", false},
		{"POST", "/api/v1/admin/staff/1/totp/reset", "", false},
		{"GET", "/api/v1/admin/users", "", false},
		{"POST", "/api/v1/admin/verification/1/review", "", false},
		{"POST", "/api/v1/auth/login", "", false},
	}
	for _, c := range cases {
		if got := Class(c.m, c.p); got != c.class {
			t.Errorf("Class(%s %s) = %q want %q", c.m, c.p, got, c.class)
		}
		if got := IsPublic(c.m, c.p); got != c.public {
			t.Errorf("IsPublic(%s %s) = %v want %v", c.m, c.p, got, c.public)
		}
	}
}

type edge struct {
	srv  *httptest.Server
	hits *[]string
}

func newEdge(t *testing.T, extra map[string]string) edge {
	t.Helper()
	var hits []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		_, _ = io.WriteString(w, `{"data":"ok"}`)
	}))
	t.Cleanup(up.Close)
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	e := map[string]string{
		"UPSTREAM_API_URL": up.URL, "COOKIE_SECURE": "false", "ALLOWED_ORIGINS": "http://localhost:3001",
		"RATE_LIMIT_ADMIN_LOGIN": "2/1m", "RATE_LIMIT_ADMIN_LOGIN_EMAIL": "100/1m", "RATE_LIMIT_GLOBAL": "1000/1m",
	}
	for k, v := range extra {
		e[k] = v
	}
	cfg, err := LoadConfig(env(e))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(cfg, &bff.RedisLimiter{Client: rc, Prefix: "t:"}, bff.NewMetrics(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return edge{srv: s, hits: &hits}
}

func do(t *testing.T, e edge, method, path, origin, cookie, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "openhr_admin_session", Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestRouting(t *testing.T) {
	e := newEdge(t, nil)
	// Product API is not reachable through the admin edge.
	if r := do(t, e, "POST", "/api/v1/auth/login", "", "", `{}`); r.StatusCode != 404 {
		t.Fatalf("product path = %d", r.StatusCode)
	}
	if r := do(t, e, "GET", "/healthz", "", "", ""); r.StatusCode != 200 {
		t.Fatalf("healthz = %d", r.StatusCode)
	}
	// Protected admin route: no cookie -> 401 at the edge, never forwarded.
	if r := do(t, e, "GET", "/api/v1/admin/users", "", "", ""); r.StatusCode != 401 {
		t.Fatalf("no cookie = %d", r.StatusCode)
	}
	if len(*e.hits) != 0 {
		t.Fatalf("forwarded without cookie: %v", *e.hits)
	}
	if r := do(t, e, "GET", "/api/v1/admin/users", "", "sid", ""); r.StatusCode != 200 {
		t.Fatalf("with cookie = %d", r.StatusCode)
	}
	// Public: logout and login need no cookie.
	if r := do(t, e, "POST", "/api/v1/admin/auth/logout", "http://localhost:3001", "", ""); r.StatusCode != 200 {
		t.Fatalf("logout = %d", r.StatusCode)
	}
	// Origin rules: a foreign origin on an unsafe method is refused; the allowed one gets CORS headers.
	if r := do(t, e, "POST", "/api/v1/admin/auth/logout", "http://evil.example", "", ""); r.StatusCode != 403 {
		t.Fatalf("foreign origin = %d", r.StatusCode)
	}
	r := do(t, e, "OPTIONS", "/api/v1/admin/users", "http://localhost:3001", "", "")
	if r.StatusCode != 200 && r.StatusCode != 204 {
		t.Fatalf("preflight = %d", r.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEdge(t, nil)
	var last *http.Response
	for i := 0; i < 3; i++ {
		last = do(t, e, "POST", "/api/v1/admin/auth/login", "http://localhost:3001", "", `{"email":"a@b.c","password":"x"}`)
	}
	if last.StatusCode != 429 || last.Header.Get("Retry-After") == "" {
		t.Fatalf("third login = %d", last.StatusCode)
	}
	if last.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3001" {
		t.Fatal("429 lacks CORS headers; the browser could not read it")
	}
	if n := len(*e.hits); n != 2 {
		t.Fatalf("upstream hits = %d", n)
	}
}
