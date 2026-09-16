package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

const testCountryID = "a1b2c3d4-e5f6-4789-a012-3456789abcde"

func TestWaitlistSignup_valid(t *testing.T) {
	t.Parallel()

	regionalMock, globalMock := expectSuccessfulSignup(t)

	router := testWaitlistRouter(t, regionalMock, globalMock)
	rec := postWaitlist(t, router, `{
		"email": "jane@acme.com"
	}`)

	assertStatus(t, rec, http.StatusCreated)
	data := decodeData(t, rec)
	if data["message"] != "You're on the list!" {
		t.Fatalf("message: got %q", data["message"])
	}
	if data["region"] != "uk" {
		t.Fatalf("region: got %q, want uk", data["region"])
	}
	if _, ok := data["token"]; ok {
		t.Fatal("token: expected omitted from response")
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
	waitlistSQL, waitlistArgs, err := query.InsertWaitlistSignup(
		"dup@acme.com", "uk", "inferred", []byte("{}"),
	)
	if err != nil {
		t.Fatalf("InsertWaitlistSignup: %v", err)
	}
	regionalMock.ExpectQuery(waitlistSQL).WithArgs(waitlistArgs...).WillReturnError(pgx.ErrNoRows)
	regionalMock.ExpectRollback()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	globalMock.ExpectBegin()
	globalMock.ExpectRollback()

	router := testWaitlistRouter(t, regionalMock, globalMock)
	rec := postWaitlist(t, router, `{
		"email": "dup@acme.com"
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
	router := testWaitlistRouter(t, regionalMock, globalMock)

	rec := postWaitlist(t, router, `{
		"email": "not-an-email"
	}`)

	assertStatus(t, rec, http.StatusBadRequest)
	errBody := decodeError(t, rec)
	fields := decodeDetailsFields(t, errBody)
	if len(fields) == 0 || fields[0]["field"] != "email" {
		t.Fatalf("fields: got %v, want first field=email", fields)
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
	waitlistSQL, waitlistArgs, err := query.InsertWaitlistSignup(
		"dup@acme.com", "uk", "inferred", []byte("{}"),
	)
	if err != nil {
		t.Fatalf("InsertWaitlistSignup: %v", err)
	}
	regionalMock.ExpectQuery(waitlistSQL).WithArgs(waitlistArgs...).WillReturnError(pgx.ErrNoRows)
	regionalMock.ExpectRollback()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	globalMock.ExpectBegin()
	globalMock.ExpectRollback()

	h := NewWaitlistHandler(dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{
		region.RegionUK: regionalMock,
	}), globalMock, nil, region.RegionUK)

	err = h.signup(context.Background(), "dup@acme.com")
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

func TestCountriesList(t *testing.T) {
	t.Parallel()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	iconKey := "flag-uk"
	listSQL, listArgs, err := query.ListActiveCountries()
	if err != nil {
		t.Fatalf("ListActiveCountries: %v", err)
	}
	globalMock.ExpectQuery(listSQL).WithArgs(listArgs...).WillReturnRows(
		pgxmock.NewRows([]string{"id", "name", "slug", "region", "icon_key"}).
			AddRow(uuid.MustParse(testCountryID), "United Kingdom", "united-kingdom", region.RegionUK, &iconKey),
	)

	h := NewCountriesHandler(globalMock)
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		h.RegisterRoutes(r)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/countries", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var envelope response.Envelope
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}

	raw, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}

	var countries []Country
	if err := json.Unmarshal(raw, &countries); err != nil {
		t.Fatalf("unmarshal countries: %v", err)
	}

	if len(countries) != 1 {
		t.Fatalf("countries: got %d, want 1", len(countries))
	}
	if countries[0].Name != "United Kingdom" {
		t.Fatalf("name: got %q", countries[0].Name)
	}

	assertMocksMet(t, globalMock)
}

func expectSuccessfulSignup(t *testing.T) (pgxmock.PgxPoolIface, pgxmock.PgxPoolIface) {
	t.Helper()

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	regionalMock.ExpectBegin()
	waitlistSQL, waitlistArgs, err := query.InsertWaitlistSignup(
		"jane@acme.com", "uk", "inferred", []byte("{}"),
	)
	if err != nil {
		t.Fatalf("InsertWaitlistSignup: %v", err)
	}
	testToken := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	regionalMock.ExpectQuery(waitlistSQL).WithArgs(waitlistArgs...).
		WillReturnRows(pgxmock.NewRows([]string{"uuid"}).AddRow(testToken))
	regionalMock.ExpectCommit()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	globalMock.ExpectBegin()
	registrySQL, registryArgs, err := query.InsertWaitlistRegistry("jane@acme.com", "uk", "inferred", testToken)
	if err != nil {
		t.Fatalf("InsertWaitlistRegistry: %v", err)
	}
	globalMock.ExpectExec(registrySQL).WithArgs(registryArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	globalMock.ExpectCommit()

	return regionalMock, globalMock
}

func emptyMocks(t *testing.T) (pgxmock.PgxPoolIface, pgxmock.PgxPoolIface) {
	t.Helper()

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
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
) chi.Router {
	t.Helper()

	h := NewWaitlistHandler(dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{
		region.RegionUK: regionalMock,
	}), globalMock, nil, region.RegionUK)

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

func decodeDetailsFields(t *testing.T, body response.ErrorBody) []map[string]string {
	t.Helper()

	raw, ok := body.Details["fields"]
	if !ok {
		t.Fatalf("details: missing \"fields\" key; got %v", body.Details)
	}

	rawSlice, ok := raw.([]any)
	if !ok {
		t.Fatalf("details.fields: expected []any, got %T", raw)
	}

	result := make([]map[string]string, 0, len(rawSlice))
	for _, item := range rawSlice {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("details.fields item: expected map[string]any, got %T", item)
		}
		entry := make(map[string]string, len(m))
		for k, v := range m {
			entry[k] = v.(string)
		}
		result = append(result, entry)
	}

	return result
}
