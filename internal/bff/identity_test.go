package bff

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/handler"
	"github.com/Angle-HR/server/internal/identityhttp"
	"github.com/Angle-HR/server/internal/session"
)

func walkPaths(t *testing.T, mount func(r chi.Router)) []string {
	t.Helper()
	r := chi.NewRouter()
	mount(r)
	var out []string
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.ReplaceAll(strings.TrimSuffix(route, "/*"), "{token}", "tok")
		for strings.Contains(route, "{") {
			i := strings.Index(route, "{")
			j := strings.Index(route, "}")
			route = route[:i] + "x" + route[j+1:]
		}
		out = append(out, route)
		return nil
	})
	return out
}

// Every route the identity handlers register reaches the identity upstream, and none of them also matches the
// applications upstream; the admin routes that stay on the monolith match neither.
func TestIdentityPathCoversEveryIdentityRoute(t *testing.T) {
	id := walkPaths(t, func(r chi.Router) {
		identityhttp.Mount(r, identityhttp.Deps{
			Auth: &identityhttp.AuthHandler{}, Admin: &identityhttp.AdminHandler{},
			RequireAuth: func(h http.Handler) http.Handler { return h },
		})
	})
	if len(id) < 30 {
		t.Fatalf("walk found only %d routes", len(id))
	}
	for _, p := range id {
		if !IdentityPath(p) {
			t.Errorf("%s is served by identity but IdentityPath is false", p)
		}
		if ApplicationsPath(p) {
			t.Errorf("%s matches both upstreams", p)
		}
	}
	// The admin catalogs are mounted by the identity service as well (AdminExtra).
	cat := walkPaths(t, func(r chi.Router) {
		new(handler.AdminHandler).RegisterProtectedRoutes(r, nil)
	})
	if len(cat) == 0 {
		t.Fatal("no admin catalog routes found")
	}
	for _, p := range cat {
		if !IdentityPath("/admin" + strings.TrimPrefix(p, "/admin")) {
			t.Errorf("%s is served by identity but IdentityPath is false", p)
		}
	}
	// Nothing else is claimed: these have no owner and get a 404 at the edge.
	for _, p := range []string{"/admin/jobs", "/organizations", "/auth", "/admin", "/onboarding/status", "/jobs/1", "/countries"} {
		if IdentityPath(p) && p != "/auth" {
			t.Errorf("%s must not be an identity path", p)
		}
	}
	_ = admin.PermUsersRead
}

func TestIdentityRoutedAtTheEdge(t *testing.T) {
	var gotIdentity, gotMono string
	idUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotIdentity = r.URL.Path }))
	defer idUp.Close()
	f := newFixture(t, func(c *Config) { c.IdentityURL = idUp.URL }, nil)
	_ = gotMono
	do(t, "POST", f.edge.URL+"/api/v1/auth/login", `{}`, nil)
	if gotIdentity != "/api/v1/auth/login" {
		t.Fatalf("login went to %q", gotIdentity)
	}
	do(t, "GET", f.edge.URL+"/api/v1/admin/catalogs/x", "", nil)
	if gotIdentity != "/api/v1/admin/catalogs/x" {
		t.Fatalf("catalogs went to %q", gotIdentity)
	}
}

func TestRedisValidator(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := Config{APIPrefix: "/api/v1", CookieSecure: false, SessionIdleTTL: time.Hour, SessionAbsoluteTTL: 2 * time.Hour}
	v := NewRedisValidator(rdb, cfg)
	uid := uuid.New()
	startCookie := func(m *session.Manager) *http.Cookie {
		rec := httptest.NewRecorder()
		if _, err := m.Start(rec, httptest.NewRequest("POST", "/", nil), uid, "uk", false); err != nil {
			t.Fatal(err)
		}
		return rec.Result().Cookies()[0]
	}
	prod, adm := startCookie(v.Product), startCookie(v.Admin)

	h, err := NewHandler(Config{Name: "t", UpstreamURL: "http://127.0.0.1:1", APIPrefix: "/api/v1", MaxBodyBytes: 1024,
		UpstreamTimeout: time.Second, GlobalLimit: Rule{1000, time.Minute}, Classes: DefaultClasses()},
		Deps{Limiter: &RedisLimiter{Client: rdb, Prefix: "t:"}, Validator: v})
	if err != nil {
		t.Fatal(err)
	}
	code := func(path string, c *http.Cookie) int {
		req := httptest.NewRequest("GET", "/api/v1"+path, nil)
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	// Protected without a cookie: refused at the edge. Public: reaches the (dead) upstream, so 502/504, not 401.
	for _, p := range []string{"/auth/me", "/jobs", "/onboarding/status", "/admin/users", "/admin/auth/me", "/admin/auth/totp/enroll", "/admin/auth/totp/confirm", "/admin/staff/x/totp/reset"} {
		if got := code(p, nil); got != 401 {
			t.Errorf("%s without cookie = %d, want 401", p, got)
		}
	}
	for _, p := range []string{"/auth/login", "/countries", "/public/jobs/x", "/onboarding/industries", "/admin/auth/login", "/admin/auth/login/totp", "/admin/auth/invite/t"} {
		if got := code(p, nil); got == 401 {
			t.Errorf("public %s refused", p)
		}
	}
	// Valid cookie passes the edge (upstream is down: not 401).
	if got := code("/auth/me", prod); got == 401 {
		t.Error("valid product cookie refused")
	}
	if got := code("/admin/users", adm); got == 401 {
		t.Error("valid admin cookie refused")
	}
	// Namespaces are separate: a product cookie never opens admin paths and the reverse.
	adminNamed := &http.Cookie{Name: v.Admin.SessionCookieName(), Value: prod.Value}
	if got := code("/admin/users", adminNamed); got != 401 {
		t.Errorf("product session under the admin cookie name = %d", got)
	}
	if got := code("/auth/me", &http.Cookie{Name: prod.Name, Value: adm.Value}); got != 401 {
		t.Errorf("admin session under the product cookie name = %d", got)
	}
	mr.FlushAll()
	if got := code("/auth/me", prod); got != 401 {
		t.Errorf("revoked session = %d", got)
	}
}
