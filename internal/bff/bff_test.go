package bff

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type fixture struct {
	mr       *miniredis.Miniredis
	up       *httptest.Server
	edge     *httptest.Server
	cfg      Config
	lastReq  *http.Request
	lastBody string
}

type errLimiter struct{}

func (errLimiter) Allow(context.Context, string, Rule) (bool, time.Duration, error) {
	return false, 0, errors.New("redis down")
}
func (errLimiter) Ping(context.Context) error { return errors.New("redis down") }

func newFixture(t *testing.T, mutate func(*Config), lim func(*miniredis.Miniredis) Limiter) *fixture {
	t.Helper()
	f := &fixture{mr: miniredis.RunT(t)}
	f.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastReq = r.Clone(context.Background())
		b, _ := io.ReadAll(r.Body)
		f.lastBody = string(b)
		switch r.URL.Path {
		case "/healthz":
			w.Write([]byte("ok"))
		case "/api/v1/stream":
			fl := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: one\n\n"))
			fl.Flush()
			time.Sleep(300 * time.Millisecond)
			w.Write([]byte("data: two\n\n"))
		default:
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc", HttpOnly: true, Path: "/"})
			w.Header().Set("Access-Control-Allow-Origin", "*") // must be stripped
			w.Header().Set("X-Upstream", "yes")
			w.Header().Set("X-Seen-Body", f.lastBody)
			w.Write([]byte(`{"data":"ok"}`))
		}
	}))
	t.Cleanup(f.up.Close)
	f.cfg = Config{
		Name: "t", UpstreamURL: f.up.URL, APIPrefix: "/api/v1", MaxBodyBytes: 1024,
		UpstreamTimeout: 2 * time.Second, AllowedOrigins: []string{"https://app.example.com"},
		GlobalLimit: Rule{1000, time.Minute}, Classes: DefaultClasses(), FailClosedAuth: true,
	}
	if mutate != nil {
		mutate(&f.cfg)
	}
	var l Limiter = &RedisLimiter{Client: redis.NewClient(&redis.Options{Addr: f.mr.Addr()}), Prefix: "t:"}
	if lim != nil {
		l = lim(f.mr)
	}
	h, err := NewHandler(f.cfg, Deps{Limiter: l})
	if err != nil {
		t.Fatal(err)
	}
	f.edge = httptest.NewServer(h)
	t.Cleanup(f.edge.Close)
	return f
}

func do(t *testing.T, method, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestProxyHeadersCookiesAndRequestID(t *testing.T) {
	f := newFixture(t, nil, nil)
	resp := do(t, "POST", f.edge.URL+"/api/v1/things?x=1", `{"a":1}`, map[string]string{
		"Cookie": "sid=in", "Authorization": "Bearer t", "X-CSRF-Token": "c", "Connection": "X-Drop", "X-Drop": "1",
		"X-Forwarded-For": "6.6.6.6", "X-Openhr-Subject": "spoof",
	})
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	r := f.lastReq
	if r.URL.Path != "/api/v1/things" || r.URL.RawQuery != "x=1" {
		t.Errorf("path %s?%s", r.URL.Path, r.URL.RawQuery)
	}
	if r.Header.Get("Cookie") != "sid=in" || r.Header.Get("Authorization") != "Bearer t" || r.Header.Get("X-CSRF-Token") != "c" {
		t.Errorf("credentials not forwarded: %v", r.Header)
	}
	if r.Header.Get("X-Drop") != "" {
		t.Error("hop-by-hop listed header leaked")
	}
	if r.Header.Get("X-Openhr-Subject") != "" {
		t.Error("spoofed identity header leaked")
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "127.0.0.1" { // peer untrusted: client XFF discarded
		t.Errorf("xff %q", xff)
	}
	if r.Header.Get("X-Forwarded-Host") == "" || r.Header.Get("X-Forwarded-Proto") != "http" {
		t.Errorf("forwarded host/proto: %v", r.Header)
	}
	if f.lastBody != `{"a":1}` {
		t.Errorf("body %q", f.lastBody)
	}
	id := resp.Header.Get("X-Request-Id")
	if id == "" || r.Header.Get("X-Request-Id") != id {
		t.Errorf("request id resp=%q upstream=%q", id, r.Header.Get("X-Request-Id"))
	}
	if c := resp.Header.Get("Set-Cookie"); !strings.Contains(c, "sid=abc") || !strings.Contains(c, "HttpOnly") {
		t.Errorf("set-cookie %q", c)
	}
	if resp.Header.Get("X-Upstream") != "yes" {
		t.Error("upstream header lost")
	}
	if v := resp.Header.Values("Access-Control-Allow-Origin"); len(v) != 0 {
		t.Errorf("upstream CORS leaked %v", v)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Frame-Options") != "DENY" ||
		resp.Header.Get("Referrer-Policy") == "" || resp.Header.Get("Permissions-Policy") == "" {
		t.Error("security headers missing")
	}
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS over plain http")
	}

	// client-supplied id is kept
	resp = do(t, "GET", f.edge.URL+"/api/v1/x", "", map[string]string{"X-Request-Id": "abc-123"})
	if resp.Header.Get("X-Request-Id") != "abc-123" || f.lastReq.Header.Get("X-Request-Id") != "abc-123" {
		t.Error("request id not preserved")
	}
}

func TestProxyStreaming(t *testing.T) {
	f := newFixture(t, nil, nil)
	resp := do(t, "GET", f.edge.URL+"/api/v1/stream", "", nil)
	start := time.Now()
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if line != "data: one\n" || time.Since(start) > 200*time.Millisecond {
		t.Errorf("first event not flushed promptly: %q after %v", line, time.Since(start))
	}
}

func TestBodyLimit(t *testing.T) {
	f := newFixture(t, nil, nil)
	resp := do(t, "POST", f.edge.URL+"/api/v1/x", strings.Repeat("a", 2048), nil)
	if resp.StatusCode != 413 {
		t.Errorf("status %d", resp.StatusCode)
	}
	var env struct {
		Error struct{ Code string }
		Meta  struct {
			RequestID string `json:"request_id"`
		}
	}
	json.NewDecoder(resp.Body).Decode(&env)
	if env.Error.Code != "PAYLOAD_TOO_LARGE" || env.Meta.RequestID == "" {
		t.Errorf("envelope %+v", env)
	}
}

func TestUpstreamDown(t *testing.T) {
	f := newFixture(t, nil, nil)
	f.up.Close()
	resp := do(t, "GET", f.edge.URL+"/api/v1/x", "", nil)
	if resp.StatusCode != 502 {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestRateLimitClasses(t *testing.T) {
	paths := map[string]string{
		"LOGIN": "/api/v1/auth/login", "LOGIN_OTP_REQUEST": "/api/v1/auth/login/otp/request",
		"LOGIN_OTP_VERIFY": "/api/v1/auth/login/otp/verify", "LOGIN_TOTP": "/api/v1/auth/login/totp",
		"SIGNUP": "/api/v1/auth/signup", "VERIFY_EMAIL": "/api/v1/auth/verify-email",
		"RESEND_VERIFICATION": "/api/v1/auth/resend-verification", "FORGOT_PASSWORD": "/api/v1/auth/forgot-password",
		"RESET_PASSWORD": "/api/v1/auth/reset-password", "PUBLIC_APPLY": "/api/v1/public/jobs/abc/applications",
	}
	if len(paths) != len(DefaultClasses()) {
		t.Fatal("test must cover every class")
	}
	for class, p := range paths {
		t.Run(class, func(t *testing.T) {
			f := newFixture(t, func(c *Config) {
				cc := c.Classes[class]
				cc.IP = Rule{2, time.Minute}
				cc.Email = Rule{}
				c.Classes[class] = cc
			}, nil)
			for i := 0; i < 2; i++ {
				if r := do(t, "POST", f.edge.URL+p, `{}`, nil); r.StatusCode != 200 {
					t.Fatalf("req %d: %d", i, r.StatusCode)
				}
			}
			resp := do(t, "POST", f.edge.URL+p, `{}`, nil)
			if resp.StatusCode != 429 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if ra := resp.Header.Get("Retry-After"); ra == "" || ra == "0" {
				t.Errorf("Retry-After %q", ra)
			}
			var env struct {
				Error struct{ Code, Message string }
				Meta  struct {
					RequestID string `json:"request_id"`
				}
			}
			json.NewDecoder(resp.Body).Decode(&env)
			if env.Error.Code != "TOO_MANY_REQUESTS" || env.Meta.RequestID == "" {
				t.Errorf("envelope %+v", env)
			}
			// other paths are unaffected
			if r := do(t, "GET", f.edge.URL+"/api/v1/jobs", "", nil); r.StatusCode != 200 {
				t.Errorf("unrelated path %d", r.StatusCode)
			}
		})
	}
}

func TestEmailKeyingAcrossIPsAndBodyRestored(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
		cc := c.Classes["LOGIN"]
		cc.IP = Rule{100, time.Minute}
		cc.Email = Rule{2, time.Minute}
		c.Classes["LOGIN"] = cc
	}, nil)
	login := func(ip, email string) *http.Response {
		return do(t, "POST", f.edge.URL+"/api/v1/auth/login", `{"email":"`+email+`","password":"p"}`,
			map[string]string{"X-Forwarded-For": ip})
	}
	if r := login("1.1.1.1", "Bob@Example.com"); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if !strings.Contains(f.lastBody, `"password":"p"`) {
		t.Errorf("body not restored for upstream: %q", f.lastBody)
	}
	if r := login("2.2.2.2", " bob@example.COM "); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if r := login("3.3.3.3", "bob@example.com"); r.StatusCode != 429 {
		t.Errorf("third attempt for same email from new IP: %d", r.StatusCode)
	}
	if r := login("3.3.3.3", "other@example.com"); r.StatusCode != 200 {
		t.Errorf("different email must not be limited: %d", r.StatusCode)
	}
}

func TestGlobalLimitAndTrustedProxy(t *testing.T) {
	trusted := func(c *Config) {
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
		c.GlobalLimit = Rule{2, time.Minute}
	}
	f := newFixture(t, trusted, nil)
	get := func(xff string) int {
		return do(t, "GET", f.edge.URL+"/api/v1/x", "", map[string]string{"X-Forwarded-For": xff}).StatusCode
	}
	get("9.9.9.9")
	get("9.9.9.9")
	if get("9.9.9.9") != 429 {
		t.Error("same client IP should be limited")
	}
	if get("8.8.8.8") != 200 {
		t.Error("different client IP behind trusted proxy must have its own bucket")
	}
	if got := f.lastReq.Header.Get("X-Forwarded-For"); got != "8.8.8.8, 127.0.0.1" {
		t.Errorf("forwarded chain %q", got)
	}

	// untrusted peer: XFF is ignored, so spoofing different values cannot dodge the limit
	f2 := newFixture(t, func(c *Config) { c.GlobalLimit = Rule{2, time.Minute} }, nil)
	codes := []int{}
	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		codes = append(codes, do(t, "GET", f2.edge.URL+"/api/v1/x", "", map[string]string{"X-Forwarded-For": ip}).StatusCode)
	}
	if codes[2] != 429 {
		t.Errorf("spoofed XFF dodged the limit: %v", codes)
	}
}

func TestClientIPChain(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	mk := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.5:1000", "1.2.3.4", "203.0.113.5"},
		{"10.0.0.1:1000", "", "10.0.0.1"},
		{"10.0.0.1:1000", "1.2.3.4", "1.2.3.4"},
		{"10.0.0.1:1000", "6.6.6.6, 1.2.3.4, 10.0.0.9", "1.2.3.4"}, // rightmost untrusted wins; left is attacker-supplied
		{"10.0.0.1:1000", "garbage", "10.0.0.1"},
	}
	for _, c := range cases {
		if got := ClientIP(mk(c.remote, c.xff), trusted); got != c.want {
			t.Errorf("%s xff=%q: got %s want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestCORSAndOrigin(t *testing.T) {
	f := newFixture(t, nil, nil)
	pre := do(t, "OPTIONS", f.edge.URL+"/api/v1/auth/login", "", map[string]string{
		"Origin": "https://app.example.com", "Access-Control-Request-Method": "POST",
		"Access-Control-Request-Headers": "x-csrf-token",
	})
	if pre.StatusCode != 204 || pre.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		pre.Header.Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(pre.Header.Get("Access-Control-Allow-Headers"), "X-CSRF-Token") ||
		!strings.Contains(pre.Header.Get("Access-Control-Allow-Headers"), "If-Match") { // job saves carry the revision
		t.Errorf("preflight %d %v", pre.StatusCode, pre.Header)
	}
	if bad := do(t, "OPTIONS", f.edge.URL+"/api/v1/x", "", map[string]string{
		"Origin": "https://evil.example", "Access-Control-Request-Method": "POST"}); bad.StatusCode != 403 {
		t.Errorf("evil preflight %d", bad.StatusCode)
	}
	if r := do(t, "POST", f.edge.URL+"/api/v1/x", `{}`, map[string]string{"Origin": "https://evil.example"}); r.StatusCode != 403 {
		t.Errorf("unsafe from evil origin: %d", r.StatusCode)
	}
	if r := do(t, "GET", f.edge.URL+"/api/v1/x", "", map[string]string{"Origin": "https://evil.example"}); r.StatusCode != 200 ||
		r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("safe GET from evil origin: %d acao=%q", r.StatusCode, r.Header.Get("Access-Control-Allow-Origin"))
	}
	ok := do(t, "POST", f.edge.URL+"/api/v1/x", `{}`, map[string]string{"Origin": "https://app.example.com"})
	if ok.StatusCode != 200 || ok.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Errorf("allowed origin POST %d %v", ok.StatusCode, ok.Header)
	}
	if r := do(t, "POST", f.edge.URL+"/api/v1/x", `{}`, nil); r.StatusCode != 200 {
		t.Errorf("no Origin should pass by default: %d", r.StatusCode)
	}
	f2 := newFixture(t, func(c *Config) { c.RequireOrigin = true }, nil)
	if r := do(t, "POST", f2.edge.URL+"/api/v1/x", `{}`, nil); r.StatusCode != 403 {
		t.Errorf("ORIGIN_REQUIRED: %d", r.StatusCode)
	}
}

func TestHSTSBehindTrustedTLSProxy(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")} }, nil)
	r := do(t, "GET", f.edge.URL+"/api/v1/x", "", map[string]string{"X-Forwarded-Proto": "https"})
	if r.Header.Get("Strict-Transport-Security") == "" {
		t.Error("expected HSTS")
	}
	if f.lastReq.Header.Get("X-Forwarded-Proto") != "https" {
		t.Error("proto not forwarded")
	}
}

func TestRedisDownFailModes(t *testing.T) {
	down := func(*miniredis.Miniredis) Limiter { return errLimiter{} }
	f := newFixture(t, nil, down)
	// auth class: fail closed
	r := do(t, "POST", f.edge.URL+"/api/v1/auth/login", `{}`, nil)
	if r.StatusCode != 503 {
		t.Errorf("auth class with redis down: %d", r.StatusCode)
	}
	// the global limiter fails open by default, so a non-auth path passes
	if r := do(t, "GET", f.edge.URL+"/api/v1/jobs", "", nil); r.StatusCode != 200 {
		t.Errorf("other path with redis down: %d", r.StatusCode)
	}
	// switch: auth fail-open
	f2 := newFixture(t, func(c *Config) { c.FailClosedAuth = false }, down)
	if r := do(t, "POST", f2.edge.URL+"/api/v1/auth/login", `{}`, nil); r.StatusCode != 200 {
		t.Errorf("fail-open auth: %d", r.StatusCode)
	}
	// switch: others fail-closed
	f3 := newFixture(t, func(c *Config) { c.FailClosedOthers = true }, down)
	if r := do(t, "GET", f3.edge.URL+"/api/v1/jobs", "", nil); r.StatusCode != 503 {
		t.Errorf("fail-closed others: %d", r.StatusCode)
	}
}

func TestRedisRealOutage(t *testing.T) {
	f := newFixture(t, nil, nil)
	f.mr.Close()
	if r := do(t, "POST", f.edge.URL+"/api/v1/auth/login", `{}`, nil); r.StatusCode != 503 {
		t.Errorf("auth with real outage: %d", r.StatusCode)
	}
	if r := do(t, "GET", f.edge.URL+"/api/v1/jobs", "", nil); r.StatusCode != 200 {
		t.Errorf("other with real outage: %d", r.StatusCode)
	}
}

func TestHealthAndReady(t *testing.T) {
	f := newFixture(t, nil, nil)
	if r := do(t, "GET", f.edge.URL+"/healthz", "", nil); r.StatusCode != 200 {
		t.Error("healthz")
	}
	if r := do(t, "GET", f.edge.URL+"/readyz", "", nil); r.StatusCode != 200 {
		t.Errorf("readyz %d", r.StatusCode)
	}
	f.mr.Close()
	if r := do(t, "GET", f.edge.URL+"/readyz", "", nil); r.StatusCode != 503 {
		t.Errorf("readyz with redis down %d", r.StatusCode)
	}
}

type denyValidator struct{}

func (denyValidator) Validate(r *http.Request) (*Identity, error) {
	if r.Header.Get("Cookie") == "" {
		return nil, ErrUnauthenticated
	}
	return &Identity{Subject: "u1", TenantID: "t1", Roles: []string{"admin"}}, nil
}

func TestSessionValidatorHook(t *testing.T) {
	f := newFixture(t, nil, nil)
	h, _ := NewHandler(f.cfg, Deps{Limiter: &RedisLimiter{Client: redis.NewClient(&redis.Options{Addr: f.mr.Addr()})}, Validator: denyValidator{}})
	s := httptest.NewServer(h)
	defer s.Close()
	if r := do(t, "GET", s.URL+"/api/v1/x", "", nil); r.StatusCode != 401 {
		t.Errorf("unauth %d", r.StatusCode)
	}
	do(t, "GET", s.URL+"/api/v1/x", "", map[string]string{"Cookie": "sid=1", "X-Openhr-Subject": "spoof"})
	if f.lastReq.Header.Get("X-Openhr-Subject") != "u1" || f.lastReq.Header.Get("X-Openhr-Roles") != "admin" {
		t.Errorf("identity headers %v", f.lastReq.Header)
	}
}

func TestConfig(t *testing.T) {
	env := map[string]string{
		"UPSTREAM_API_URL": "http://x:1", "TRUSTED_PROXY_CIDRS": "10.0.0.0/8, 192.168.1.1",
		"RATE_LIMIT_LOGIN": "7/2m", "ALLOWED_ORIGINS": "https://a.example, https://b.example",
	}
	c, err := LoadConfig(Defaults{Name: "web-bff"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 2 || c.Classes["LOGIN"].IP != (Rule{7, 2 * time.Minute}) || len(c.AllowedOrigins) != 2 || !c.FailClosedAuth || c.FailClosedOthers {
		t.Errorf("%+v", c)
	}
	if _, err := LoadConfig(Defaults{}, func(string) string { return "" }); err == nil {
		t.Error("missing upstream must fail")
	}
}

func TestApplicationsPath(t *testing.T) {
	yes := []string{"/public/jobs/abc/applications", "/public/jobs/abc/uploads", "/public/jobs/abc/uploads/x/y",
		"/jobs/1/applications", "/jobs/1/applications/2/status", "/hiring/applicant-settings"}
	no := []string{"/public/jobs/abc", "/public/jobs/sitemap.xml", "/jobs/1", "/jobs/1/publish", "/hiring/settings",
		"/auth/login", "/jobs/applications", "/hiring/applicant-settings/x"}
	for _, p := range yes {
		if !ApplicationsPath(p) {
			t.Errorf("%s should be routed to applications", p)
		}
	}
	for _, p := range no {
		if ApplicationsPath(p) {
			t.Errorf("%s should stay on the monolith", p)
		}
	}
}

func TestOrgPath(t *testing.T) {
	yes := []string{
		"/countries", "/onboarding/status", "/onboarding/profile", "/onboarding/address", "/onboarding/address/search",
		"/onboarding/address/verify", "/onboarding/compliance", "/onboarding/business", "/onboarding/complete",
		"/onboarding/individual", "/onboarding/business-types", "/onboarding/industries", "/onboarding/company-roles",
		"/onboarding/identification-requirements", "/organization/verification", "/organization/verification/",
		"/organization/verification/retry", "/organization/verification/confirm-name",
		"/organization/verification/confirm-address", "/organization/verification/change-country",
		"/organization/agreements", "/organization/privacy-contact",
	}
	no := []string{
		"/", "/countries/x", "/onboarding", "/onboarding/unknown", "/onboarding/address/other", "/onboarding/status/x",
		"/onboarding/address/search/x", "/organization", "/organization/dpia", "/organization/verification/other",
		"/organization/verification/retry/x", "/organization/agreements/x", "/organizations/invites", "/auth/login",
		"/auth/me", "/jobs", "/jobs/1/applications", "/hiring/settings", "/hiring/applicant-settings", "/admin/verification",
		"/public/jobs/abc",
	}
	for _, p := range yes {
		if !OrgPath(p) {
			t.Errorf("%s should be routed to org", p)
		}
		if ApplicationsPath(p) {
			t.Errorf("%s matches both org and applications", p)
		}
	}
	for _, p := range no {
		if OrgPath(p) {
			t.Errorf("%s should not be routed to org", p)
		}
	}
}

// Every path reaches exactly one upstream: the org paths go to the org service, the applications paths to the
// applications service, the rest to the monolith.
func TestRoutingToThreeUpstreams(t *testing.T) {
	var hits = map[string]string{}
	mk := func(name string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[r.URL.Path] = name
			w.Write([]byte(`{"data":"ok"}`))
		}))
		t.Cleanup(s.Close)
		return s
	}
	apps, org := mk("apps"), mk("org")
	f := newFixture(t, func(c *Config) { c.ApplicationsURL, c.OrgURL = apps.URL, org.URL }, nil)
	want := map[string]string{
		"/api/v1/onboarding/status":               "org",
		"/api/v1/organization/verification/retry": "org",
		"/api/v1/organization/agreements":         "org",
		"/api/v1/countries":                       "org",
		"/api/v1/jobs/1/applications":             "apps",
		"/api/v1/public/jobs/abc/applications":    "apps",
		"/api/v1/organization/dpia":               "",
		"/api/v1/organizations/invites":           "",
		"/api/v1/auth/login":                      "",
		"/api/v1/jobs":                            "",
	}
	for path := range want {
		do(t, "GET", f.edge.URL+path, "", nil)
	}
	for path, name := range want {
		if got := hits[path]; got != name {
			t.Errorf("%s reached %q, want %q (empty is the monolith)", path, got, name)
		}
	}
}

func TestConfigOrgURL(t *testing.T) {
	c, err := LoadConfig(Defaults{}, func(k string) string {
		return map[string]string{"UPSTREAM_API_URL": "http://m", "UPSTREAM_ORG_URL": "http://org:8080"}[k]
	})
	if err != nil || c.OrgURL != "http://org:8080" {
		t.Fatalf("%+v %v", c, err)
	}
	cfg := Config{UpstreamURL: "http://m", OrgURL: "nope", APIPrefix: "/api/v1"}
	if _, err = NewHandler(cfg, Deps{Limiter: errLimiter{}}); err == nil {
		t.Error("a relative UPSTREAM_ORG_URL must fail")
	}
}
