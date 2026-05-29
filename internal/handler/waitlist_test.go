package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/oschwald/geoip2-golang"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

func TestWaitlistSignup_validExplicitRegion(t *testing.T) {
	t.Parallel()

	regionalMock, globalMock := expectSuccessfulSignup(t, region.RegionUK, "explicit")

	router := testWaitlistRouter(t, regionalMock, globalMock, nil)
	rec := postWaitlist(t, router, `{
		"email": "jane@acme.com",
		"company_name": "Acme Corp",
		"role": "HR Manager",
		"region": "uk",
		"metadata": {"source": "ph"}
	}`)

	assertStatus(t, rec, http.StatusCreated)
	data := decodeData(t, rec)
	if data["message"] != "You're on the list!" {
		t.Fatalf("message: got %q", data["message"])
	}
	if data["region"] != "uk" {
		t.Fatalf("region: got %q, want uk", data["region"])
	}

	assertMocksMet(t, regionalMock, globalMock)
}

func TestWaitlistSignup_validGeoFallback(t *testing.T) {
	geoDB := openTestGeoDB(t)
	resolver := region.NewRegionResolver(nil, geoDB, nil)

	regionalMock, globalMock := expectSuccessfulSignupGeo(t)

	router := testWaitlistRouter(t, regionalMock, globalMock, resolver)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/waitlist", bytes.NewReader([]byte(`{
		"email": "jane@acme.com",
		"company_name": "Acme Corp"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "81.2.69.142:12345"

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	data := decodeData(t, rec)
	if data["region"] != "uk" {
		t.Fatalf("region: got %q, want uk", data["region"])
	}

	assertMocksMet(t, regionalMock, globalMock)
}

func TestWaitlistSignup_duplicateEmail(t *testing.T) {
	t.Parallel()

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	regionalMock.ExpectBegin()
	regionalMock.ExpectQuery(regionalWaitlistInsertSQL).WithArgs(
		"dup@acme.com",
		"Acme Corp",
		pgxmock.AnyArg(),
		"uk",
		"param",
		pgxmock.AnyArg(),
	).WillReturnError(pgx.ErrNoRows)
	regionalMock.ExpectRollback()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	router := testWaitlistRouter(t, regionalMock, globalMock, nil)
	rec := postWaitlist(t, router, `{
		"email": "dup@acme.com",
		"company_name": "Acme Corp",
		"region": "uk"
	}`)

	assertStatus(t, rec, http.StatusConflict)
	errBody := decodeError(t, rec)
	if errBody.Message != apperror.MsgEmailAlreadyRegistered {
		t.Fatalf("message: got %q", errBody.Message)
	}

	assertMocksMet(t, regionalMock, globalMock)
}

func TestWaitlistSignup_invalidEmail(t *testing.T) {
	t.Parallel()

	regionalMock, globalMock := emptyMocks(t)
	router := testWaitlistRouter(t, regionalMock, globalMock, nil)

	rec := postWaitlist(t, router, `{
		"email": "not-an-email",
		"company_name": "Acme Corp",
		"region": "uk"
	}`)

	assertStatus(t, rec, http.StatusBadRequest)
	errBody := decodeError(t, rec)
	if errBody.Details["field"] != "email" {
		t.Fatalf("field: got %v, want email", errBody.Details["field"])
	}

	assertMocksMet(t, regionalMock, globalMock)
}

func TestWaitlistSignup_missingCompanyName(t *testing.T) {
	t.Parallel()

	regionalMock, globalMock := emptyMocks(t)
	router := testWaitlistRouter(t, regionalMock, globalMock, nil)

	rec := postWaitlist(t, router, `{
		"email": "jane@acme.com",
		"company_name": "",
		"region": "uk"
	}`)

	assertStatus(t, rec, http.StatusBadRequest)
	errBody := decodeError(t, rec)
	if errBody.Details["field"] != "company_name" {
		t.Fatalf("field: got %v, want company_name", errBody.Details["field"])
	}

	assertMocksMet(t, regionalMock, globalMock)
}

func TestWaitlistSignup_unknownRegion(t *testing.T) {
	t.Parallel()

	regionalMock, globalMock := emptyMocks(t)
	router := testWaitlistRouterNoMiddleware(t, regionalMock, globalMock)

	rec := postWaitlist(t, router, `{
		"email": "jane@acme.com",
		"company_name": "Acme Corp",
		"region": "antarctica"
	}`)

	assertStatus(t, rec, http.StatusBadRequest)
	errBody := decodeError(t, rec)
	if errBody.Message != apperror.MsgInvalidRegion {
		t.Fatalf("message: got %q", errBody.Message)
	}
	if errBody.Details["field"] != "region" {
		t.Fatalf("field: got %v, want region", errBody.Details["field"])
	}

	assertMocksMet(t, regionalMock, globalMock)
}

func TestIsDuplicateWaitlistSignup(t *testing.T) {
	t.Parallel()

	if !isDuplicateWaitlistSignup(pgx.ErrNoRows) {
		t.Fatal("expected pgx.ErrNoRows to be duplicate")
	}

	if !isDuplicateWaitlistSignup(errors.New("no rows in result set")) {
		t.Fatal("expected no rows message to be duplicate")
	}
}

func TestSignup_duplicateDirect(t *testing.T) {
	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	regionalMock.ExpectBegin()
	regionalMock.ExpectQuery(regionalWaitlistInsertSQL).WithArgs(
		"dup@acme.com", "Acme Corp", pgxmock.AnyArg(), "uk", "param", pgxmock.AnyArg(),
	).WillReturnError(pgx.ErrNoRows)
	regionalMock.ExpectRollback()

	globalMock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	h := NewWaitlistHandler(nil, dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{
		region.RegionUK: regionalMock,
	}), globalMock)

	err = h.signup(context.Background(), region.RegionUK, "param", "dup@acme.com", "Acme Corp", nil, []byte("{}"))
	if !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("signup: %v", err)
	}

	assertMocksMet(t, regionalMock)
}

func TestMaskEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		email string
		want  string
	}{
		{"jane@acme.com", "j***@acme.com"},
		{"  jane@acme.com  ", "j***@acme.com"},
		{"invalid", "***"},
		{"@acme.com", "***"},
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			t.Parallel()
			if got := maskEmail(tt.email); got != tt.want {
				t.Fatalf("maskEmail(%q): got %q, want %q", tt.email, got, tt.want)
			}
		})
	}
}

func TestRegistryRegionSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"jwt", "jwt"},
		{"subdomain", "subdomain"},
		{"db", "db"},
		{"param", "explicit"},
		{"geo", "ip"},
		{"other", "inferred"},
	}

	for _, tt := range tests {
		if got := registryRegionSource(tt.in); got != tt.want {
			t.Fatalf("registryRegionSource(%q): got %q, want %q", tt.in, got, tt.want)
		}
	}
}

func expectSuccessfulSignup(t *testing.T, reg region.Region, registrySource string) (pgxmock.PgxPoolIface, pgxmock.PgxPoolIface) {
	t.Helper()

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	metadata := []byte(`{"source":"ph"}`)
	role := "HR Manager"

	regionalMock.ExpectBegin()
	regionalMock.ExpectQuery(regionalWaitlistInsertSQL).WithArgs(
		"jane@acme.com",
		"Acme Corp",
		&role,
		string(reg),
		"param",
		metadata,
	).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(1)))
	regionalMock.ExpectCommit()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	globalMock.ExpectExec(globalRegistryInsertSQL).WithArgs(
		"jane@acme.com",
		string(reg),
		registrySource,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	return regionalMock, globalMock
}

func expectSuccessfulSignupGeo(t *testing.T) (pgxmock.PgxPoolIface, pgxmock.PgxPoolIface) {
	t.Helper()

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	regionalMock.ExpectBegin()
	regionalMock.ExpectQuery(regionalWaitlistInsertSQL).WithArgs(
		"jane@acme.com",
		"Acme Corp",
		pgxmock.AnyArg(),
		"uk",
		"geo",
		pgxmock.AnyArg(),
	).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(1)))
	regionalMock.ExpectCommit()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	globalMock.ExpectExec(globalRegistryInsertSQL).WithArgs(
		"jane@acme.com",
		"uk",
		"ip",
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	return regionalMock, globalMock
}

func emptyMocks(t *testing.T) (pgxmock.PgxPoolIface, pgxmock.PgxPoolIface) {
	t.Helper()

	regionalMock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	globalMock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	return regionalMock, globalMock
}

func testWaitlistRouter(
	t *testing.T,
	regionalMock pgxmock.PgxPoolIface,
	globalMock pgxmock.PgxPoolIface,
	resolver *region.RegionResolver,
) chi.Router {
	t.Helper()

	if resolver == nil {
		resolver = region.NewRegionResolver(nil, nil, nil)
	}

	h := NewWaitlistHandler(resolver, dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{
		region.RegionUK: regionalMock,
	}), globalMock)

	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		r.Use(resolver.Middleware())
		h.RegisterRoutes(r)
	})

	return router
}

func testWaitlistRouterNoMiddleware(
	t *testing.T,
	regionalMock pgxmock.PgxPoolIface,
	globalMock pgxmock.PgxPoolIface,
) chi.Router {
	t.Helper()

	h := NewWaitlistHandler(
		region.NewRegionResolver(nil, nil, nil),
		dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{
			region.RegionUK: regionalMock,
		}),
		globalMock,
	)

	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		h.RegisterRoutes(r)
	})

	return router
}

func postWaitlist(t *testing.T, router chi.Router, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/waitlist", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("status: got %d, want %d; body: %s", rec.Code, want, rec.Body.String())
	}
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()

	var envelope response.Envelope
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}

	raw, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}

	var data map[string]string
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	return data
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) response.ErrorBody {
	t.Helper()

	var envelope response.Envelope
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}

	if envelope.Error == nil {
		t.Fatal("expected error body")
	}

	return *envelope.Error
}

func assertMocksMet(t *testing.T, mocks ...pgxmock.PgxPoolIface) {
	t.Helper()

	for _, mock := range mocks {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	}
}

func openTestGeoDB(t *testing.T) *geoip2.Reader {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	path := filepath.Join(filepath.Dir(file), "..", "region", "testdata", "GeoLite2-Country-Test.mmdb")
	db, err := geoip2.Open(path)
	if err != nil {
		t.Fatalf("open test geodb: %v", err)
	}

	t.Cleanup(func() { db.Close() })

	return db
}
