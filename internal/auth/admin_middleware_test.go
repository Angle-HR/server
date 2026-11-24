package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/session"
)

type fakeAdminStore struct {
	active bool
	perms  []string
}

func (f fakeAdminStore) GetAdminPrincipal(_ context.Context, id uuid.UUID) (auth.AdminPrincipal, error) {
	return auth.AdminPrincipal{ID: id, Email: "ops@example.com", IsActive: f.active}, nil
}

func (f fakeAdminStore) ListAdminPermissions(context.Context, uuid.UUID) ([]string, error) {
	return f.perms, nil
}

func newManagers(t *testing.T) (product, admin *session.Manager) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return session.NewManager(rdb, session.ManagerConfig{Kind: session.KindProduct}),
		session.NewManager(rdb, session.ManagerConfig{Kind: session.KindAdmin})
}

func TestRequireAdmin_sessionCSRFAndSeparation(t *testing.T) {
	product, admin := newManagers(t)
	store := &fakeAdminStore{active: true, perms: []string{"waitlist.read"}}
	mw := auth.NewAdminMiddleware(admin, store)
	h := mw.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.AdminHasPermission(r.Context(), "waitlist.read") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	adminID := uuid.New()
	rec := httptest.NewRecorder()
	ident, err := admin.Start(rec, httptest.NewRequest(http.MethodPost, "/", nil), adminID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := rec.Result().Cookies()[0]

	call := func(method, csrf string, cookies ...*http.Cookie) int {
		req := httptest.NewRequest(method, "/x", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		if csrf != "" {
			req.Header.Set(session.CSRFHeader, csrf)
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		return out.Code
	}

	if got := call(http.MethodGet, ""); got != http.StatusUnauthorized {
		t.Fatalf("no cookie: %d", got)
	}
	if got := call(http.MethodGet, "", adminCookie); got != http.StatusNoContent {
		t.Fatalf("admin cookie GET: %d", got)
	}
	if got := call(http.MethodPost, "", adminCookie); got != http.StatusForbidden {
		t.Fatalf("admin POST without CSRF: %d", got)
	}
	if got := call(http.MethodPost, ident.CSRFToken, adminCookie); got != http.StatusNoContent {
		t.Fatalf("admin POST with CSRF: %d", got)
	}

	// A product session must not open admin routes, even if replayed under the admin cookie name.
	prec := httptest.NewRecorder()
	if _, err = product.Start(prec, httptest.NewRequest(http.MethodPost, "/", nil), adminID, "uk", false); err != nil {
		t.Fatal(err)
	}
	spoof := &http.Cookie{Name: admin.SessionCookieName(), Value: prec.Result().Cookies()[0].Value}
	if got := call(http.MethodGet, "", spoof); got != http.StatusUnauthorized {
		t.Fatalf("product session as admin: %d", got)
	}

	// Deactivated staff are rejected even with a live session, and RevokeAll ends it for good.
	store.active = false
	if got := call(http.MethodGet, "", adminCookie); got != http.StatusUnauthorized {
		t.Fatalf("inactive admin: %d", got)
	}
	store.active = true
	if err = admin.RevokeAll(context.Background(), adminID); err != nil {
		t.Fatal(err)
	}
	if got := call(http.MethodGet, "", adminCookie); got != http.StatusUnauthorized {
		t.Fatalf("revoked admin session: %d", got)
	}
}
