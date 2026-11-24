package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration, mr *miniredis.Miniredis) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	mr.FastForward(d)
}

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis, *fakeClock) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	st := NewStore(rdb, Options{Namespace: "t", IdleTTL: time.Hour, AbsoluteTTL: 3 * time.Hour, Now: clk.Now})
	return st, mr, clk
}

func TestStoreCreateValidateHashesKey(t *testing.T) {
	st, mr, _ := newTestStore(t)
	ctx := context.Background()
	uid := uuid.New()

	raw, ident, err := st.Create(ctx, CreateParams{UserID: uid, Region: "uk", UserAgent: "ua"})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 43 {
		t.Fatalf("session id too short: %d chars", len(raw))
	}
	for _, k := range mr.Keys() {
		if strings.Contains(k, raw) {
			t.Fatalf("raw session id must not appear in redis keys: %s", k)
		}
	}
	got, err := st.Validate(ctx, raw)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got.UserID != uid || got.Region != "uk" || got.Stage != StageFull || got.CSRFToken == "" || got.CSRFToken != ident.CSRFToken {
		t.Fatalf("unexpected identity: %+v", got)
	}
	if _, err = st.Validate(ctx, raw+"x"); err != ErrInvalid {
		t.Fatalf("tampered id: got %v", err)
	}
	if _, err = st.Validate(ctx, ""); err != ErrInvalid {
		t.Fatalf("empty id: got %v", err)
	}
}

func TestStoreIdleTimeoutSlides(t *testing.T) {
	st, mr, clk := newTestStore(t)
	ctx := context.Background()
	raw, _, err := st.Create(ctx, CreateParams{UserID: uuid.New(), Region: "uk"})
	if err != nil {
		t.Fatal(err)
	}
	// Activity every 45 minutes keeps a 1 hour idle session alive.
	for range 3 {
		clk.Advance(45*time.Minute, mr)
		if _, err = st.Validate(ctx, raw); err != nil {
			t.Fatalf("expected session alive: %v", err)
		}
	}
	// 61 idle minutes ends it.
	clk.Advance(61*time.Minute, mr)
	if _, err = st.Validate(ctx, raw); err != ErrInvalid {
		t.Fatalf("expected idle expiry, got %v", err)
	}
}

func TestStoreAbsoluteTimeout(t *testing.T) {
	st, mr, clk := newTestStore(t)
	ctx := context.Background()
	raw, _, err := st.Create(ctx, CreateParams{UserID: uuid.New(), Region: "uk"})
	if err != nil {
		t.Fatal(err)
	}
	// Constant activity cannot outlive the 3 hour absolute limit.
	for range 5 {
		clk.Advance(30*time.Minute, mr)
		if _, err = st.Validate(ctx, raw); err != nil {
			t.Fatalf("alive before absolute limit: %v", err)
		}
	}
	clk.Advance(31*time.Minute, mr)
	if _, err = st.Validate(ctx, raw); err != ErrInvalid {
		t.Fatalf("expected absolute expiry, got %v", err)
	}
}

func TestStoreRotateInvalidatesOldAndKeepsAbsoluteDeadline(t *testing.T) {
	st, mr, clk := newTestStore(t)
	ctx := context.Background()
	uid := uuid.New()
	raw, first, err := st.Create(ctx, CreateParams{UserID: uid, Region: "global"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		clk.Advance(50*time.Minute, mr)
		if _, err = st.Validate(ctx, raw); err != nil {
			t.Fatal(err)
		}
	}
	newRaw, ident, err := st.Rotate(ctx, raw, "uk", false, "ua")
	if err != nil {
		t.Fatal(err)
	}
	if newRaw == raw || ident.CSRFToken == first.CSRFToken {
		t.Fatal("rotation must change both the session id and the CSRF token")
	}
	if ident.Region != "uk" {
		t.Fatalf("region = %q", ident.Region)
	}
	if _, err = st.Validate(ctx, raw); err != ErrInvalid {
		t.Fatalf("old id must be dead: %v", err)
	}
	if !ident.CreatedAt.Equal(first.CreatedAt) {
		t.Fatal("rotation must not extend the absolute lifetime")
	}
	clk.Advance(31*time.Minute, mr)
	if _, err = st.Validate(ctx, newRaw); err != ErrInvalid {
		t.Fatalf("absolute deadline must carry over rotation: %v", err)
	}
}

func TestStoreRevokeAndRevokeAll(t *testing.T) {
	st, _, _ := newTestStore(t)
	ctx := context.Background()
	uid, other := uuid.New(), uuid.New()
	a, _, _ := st.Create(ctx, CreateParams{UserID: uid, Region: "uk"})
	b, _, _ := st.Create(ctx, CreateParams{UserID: uid, Region: "uk"})
	c, _, _ := st.Create(ctx, CreateParams{UserID: other, Region: "uk"})

	if err := st.Revoke(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Validate(ctx, a); err != ErrInvalid {
		t.Fatalf("revoked session valid: %v", err)
	}
	if err := st.Revoke(ctx, a); err != nil {
		t.Fatalf("revoke must be idempotent: %v", err)
	}
	if n, _ := st.CountForUser(ctx, uid); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}

	d, _, _ := st.Create(ctx, CreateParams{UserID: uid, Region: "uk"})
	if err := st.RevokeAll(ctx, uid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{b, d} {
		if _, err := st.Validate(ctx, raw); err != ErrInvalid {
			t.Fatalf("session survived RevokeAll: %v", err)
		}
	}
	if _, err := st.Validate(ctx, c); err != nil {
		t.Fatalf("other user's session must survive: %v", err)
	}
}

func TestValidateDoesNotResurrectRevokedSession(t *testing.T) {
	st, mr, clk := newTestStore(t)
	ctx := context.Background()
	raw, _, _ := st.Create(ctx, CreateParams{UserID: uuid.New(), Region: "uk"})
	clk.Advance(5*time.Minute, mr)
	if err := st.Revoke(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Validate(ctx, raw); err != ErrInvalid {
		t.Fatalf("got %v", err)
	}
	if mr.Exists("t:s:" + hashID(raw)) {
		t.Fatal("revoked session key reappeared")
	}
}

func TestPreAuthSingleUse(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	m := NewManager(rdb, ManagerConfig{Kind: KindProduct, Secure: false})
	uid := uuid.New()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	if err := m.StartPreAuth(rec, req, uid, "uk"); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "openhr_preauth" || !cookies[0].HttpOnly || cookies[0].MaxAge != 300 {
		t.Fatalf("unexpected preauth cookie: %+v", cookies)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/auth/login/totp", nil)
	req2.AddCookie(cookies[0])
	if _, err := m.Authenticate(req2); err == nil {
		t.Fatal("a pre-auth cookie must not authenticate as a session")
	}
	if _, err := m.PeekPreAuth(req2); err != nil {
		t.Fatalf("peek: %v", err)
	}
	if _, err := m.PeekPreAuth(req2); err != nil {
		t.Fatalf("peek must not consume: %v", err)
	}
	if ident, err := m.ConsumePreAuth(httptest.NewRecorder(), req2); err != nil || ident.UserID != uid {
		t.Fatalf("consume: %+v %v", ident, err)
	}
	if _, err := m.ConsumePreAuth(httptest.NewRecorder(), req2); err != ErrInvalid {
		t.Fatalf("second consume must fail: %v", err)
	}
}

func TestPreAuthExpiresAfterFiveMinutes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	clk := &fakeClock{t: time.Now()}
	m := NewManager(rdb, ManagerConfig{Kind: KindProduct, Now: clk.Now})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if err := m.StartPreAuth(rec, req, uuid.New(), "uk"); err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.AddCookie(rec.Result().Cookies()[0])
	clk.Advance(6*time.Minute, mr)
	if _, err := m.PeekPreAuth(req2); err != ErrInvalid {
		t.Fatalf("expected expiry, got %v", err)
	}
}

func TestCookieNamesAndAttributes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	cases := []struct {
		kind   Kind
		secure bool
		want   string
	}{
		{KindProduct, true, "__Host-openhr_session"},
		{KindProduct, false, "openhr_session"},
		{KindAdmin, true, "__Host-openhr_admin_session"},
		{KindAdmin, false, "openhr_admin_session"},
	}
	for _, tc := range cases {
		m := NewManager(rdb, ManagerConfig{Kind: tc.kind, Secure: tc.secure})
		rec := httptest.NewRecorder()
		if _, err := m.Start(rec, httptest.NewRequest(http.MethodPost, "/", nil), uuid.New(), "uk", false); err != nil {
			t.Fatal(err)
		}
		cs := rec.Result().Cookies()
		if len(cs) != 1 || cs[0].Name != tc.want {
			t.Fatalf("%v/%v: cookies %+v, want name %s", tc.kind, tc.secure, cs, tc.want)
		}
		c := cs[0]
		if !c.HttpOnly || c.Secure != tc.secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
			t.Fatalf("bad attributes: %+v", c)
		}
	}
}

func TestProductAndAdminSessionsAreIsolated(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	product := NewManager(rdb, ManagerConfig{Kind: KindProduct})
	adm := NewManager(rdb, ManagerConfig{Kind: KindAdmin})

	rec := httptest.NewRecorder()
	if _, err := adm.Start(rec, httptest.NewRequest(http.MethodPost, "/", nil), uuid.New(), "", true); err != nil {
		t.Fatal(err)
	}
	// Replay the admin cookie value under the product cookie name: must not authenticate.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: product.SessionCookieName(), Value: rec.Result().Cookies()[0].Value})
	if _, err := product.Authenticate(req); err == nil {
		t.Fatal("admin session id must not validate in the product namespace")
	}
}

func TestStartRevokesPresentedSession(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	m := NewManager(rdb, ManagerConfig{Kind: KindProduct})
	uid := uuid.New()

	rec1 := httptest.NewRecorder()
	if _, err := m.Start(rec1, httptest.NewRequest(http.MethodPost, "/", nil), uid, "uk", false); err != nil {
		t.Fatal(err)
	}
	old := rec1.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(old)
	if _, err := m.Start(httptest.NewRecorder(), req, uid, "uk", false); err != nil {
		t.Fatal(err)
	}
	probe := httptest.NewRequest(http.MethodGet, "/", nil)
	probe.AddCookie(old)
	if _, err := m.Authenticate(probe); err == nil {
		t.Fatal("login must revoke the session id that was presented (fixation defence)")
	}
}

func TestOriginGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := OriginGuard([]string{"https://app.example.com/"})(ok)

	cases := []struct {
		name, method, origin, referer string
		want                          int
	}{
		{"allowed origin", http.MethodPost, "https://app.example.com", "", http.StatusNoContent},
		{"case-insensitive", http.MethodPost, "https://APP.example.com", "", http.StatusNoContent},
		{"evil origin", http.MethodPost, "https://evil.example", "", http.StatusForbidden},
		{"null origin", http.MethodPost, "null", "", http.StatusForbidden},
		{"allowed referer", http.MethodPost, "", "https://app.example.com/page?x=1", http.StatusNoContent},
		{"evil referer", http.MethodPost, "", "https://evil.example/page", http.StatusForbidden},
		{"no headers (non-browser)", http.MethodPost, "", "", http.StatusNoContent},
		{"safe method ignores origin", http.MethodGet, "https://evil.example", "", http.StatusNoContent},
		{"delete checked", http.MethodDelete, "https://evil.example", "", http.StatusForbidden},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, "http://api.example.com/x", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.referer != "" {
			req.Header.Set("Referer", tc.referer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestCheckCSRF(t *testing.T) {
	ident := Identity{CSRFToken: "abc123"}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if CheckCSRF(req, ident) {
		t.Fatal("missing header must fail")
	}
	req.Header.Set(CSRFHeader, "abc124")
	if CheckCSRF(req, ident) {
		t.Fatal("wrong token must fail")
	}
	req.Header.Set(CSRFHeader, "abc123")
	if !CheckCSRF(req, ident) {
		t.Fatal("matching token must pass")
	}
	if CheckCSRF(req, Identity{}) {
		t.Fatal("empty session token must never match")
	}
}
