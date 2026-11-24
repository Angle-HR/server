package identityhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/response"
)

func auditRouter(t *testing.T, mock pgxmock.PgxPoolIface, perms ...string) http.Handler {
	t.Helper()
	h := &AdminHandler{Store: admin.NewStore(mock)}
	mw := auth.NewAdminMiddleware(nil, nil)
	r := chi.NewRouter()
	r.Route("/api/v1/admin", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.WithAdmin(req.Context(), uuid.New(), "a@example.com", perms)))
			})
		})
		h.RegisterProtectedRoutes(r, mw)
	})
	return r
}

var auditCols = []string{"id", "actor_id", "email", "action", "resource_type", "resource_id", "meta", "ip", "created_at"}

func getAudit(t *testing.T, r http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/admin/audit-logs"+query, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAuditLogsNeedAuditRead(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	rec := getAudit(t, auditRouter(t, mock, admin.PermUsersRead), "")
	assertStatus(t, rec, http.StatusForbidden)
	assertMocksMet(t, mock) // no query ran
}

func TestAuditLogsFiltersPageAndIPAsStored(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	actor := uuid.New()
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC) // to=2026-10-08 covers that whole day
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	email, res := "a@example.com", "r1"
	legacyIP, bareIP := "192.168.65.1:64455", "2001:db8::1" // a row from before 000016 is not rewritten on read
	// limit=2 asks for 3 rows: the third only proves another page exists.
	mock.ExpectQuery("FROM admin.audit_logs").
		WithArgs(&actor, "users.patch", "user", "r1", &from, &to, 3, 10).
		WillReturnRows(pgxmock.NewRows(auditCols).
			AddRow(int64(3), &actor, &email, "users.patch", "user", &res, []byte(`{}`), &legacyIP, at).
			AddRow(int64(2), &actor, &email, "users.patch", "user", &res, []byte(`{}`), &bareIP, at).
			AddRow(int64(1), &actor, &email, "users.patch", "user", &res, []byte(`{}`), nil, at))

	rec := getAudit(t, auditRouter(t, mock, admin.PermAuditRead),
		"?actor_id="+actor.String()+"&action=users.patch&resource_type=user&resource_id=r1&from=2026-10-01&to=2026-10-08&limit=2&offset=10")
	assertStatus(t, rec, http.StatusOK)
	var env struct {
		Data []admin.AuditLog `json:"data"`
		Meta response.Meta    `json:"meta"`
	}
	if err = json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 2 || env.Meta.HasMore == nil || !*env.Meta.HasMore {
		t.Fatalf("data %d meta %+v", len(env.Data), env.Meta)
	}
	if *env.Data[0].IP != legacyIP || *env.Data[1].IP != bareIP {
		t.Fatalf("ips = %q %q: must be returned as stored", *env.Data[0].IP, *env.Data[1].IP)
	}
	assertMocksMet(t, mock)
}

func TestAuditLogsLastPageHasNoMore(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("FROM admin.audit_logs").
		WithArgs((*uuid.UUID)(nil), "", "", "", (*time.Time)(nil), (*time.Time)(nil), 51, 0).
		WillReturnRows(pgxmock.NewRows(auditCols))
	rec := getAudit(t, auditRouter(t, mock, admin.PermAuditRead), "")
	assertStatus(t, rec, http.StatusOK)
	var env struct {
		Data []admin.AuditLog `json:"data"`
		Meta response.Meta    `json:"meta"`
	}
	if err = json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Data == nil || len(env.Data) != 0 || (env.Meta.HasMore != nil && *env.Meta.HasMore) {
		t.Fatalf("data %v meta %+v", env.Data, env.Meta)
	}
}

func TestAuditLogsValidation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	r := auditRouter(t, mock, admin.PermAuditRead)
	for _, q := range []string{"?actor_id=x", "?from=yesterday", "?to=2026-13-01", "?from=2026-10-08&to=2026-10-01"} {
		assertStatus(t, getAudit(t, r, q), http.StatusBadRequest)
	}
	assertMocksMet(t, mock)
}
