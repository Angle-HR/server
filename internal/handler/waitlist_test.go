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
		"full_name": "Jerry",
		"email": "jane@acme.com",
		"country_id": "`+testCountryID+`"
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

func TestWaitlistSignup_duplicateEmail(t *testing.T) {
	t.Parallel()

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	regionalMock.ExpectBegin()
	waitlistSQL, waitlistArgs, err := query.InsertWaitlistSignup(
		"Jerry", "dup@acme.com", uuid.MustParse(testCountryID), "uk", "explicit", []byte("{}"),
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

	expectCountryLookup(t, globalMock, testCountryID)

	router := testWaitlistRouter(t, regionalMock, globalMock)
	rec := postWaitlist(t, router, `{
		"full_name": "Jerry",
		"email": "dup@acme.com",
		"country_id": "`+testCountryID+`"
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
		"full_name": "Jerry",
		"email": "not-an-email",
		"country_id": "`+testCountryID+`"
	}`)

	assertStatus(t, rec, http.StatusBadRequest)
	errBody := decodeError(t, rec)
	if errBody.Details["field"] != "email" {
		t.Fatalf("field: got %v, want email", errBody.Details["field"])
	}

	assertMocksMet(t, regionalMock, globalMock)
}

func TestWaitlistSignup_missingFullName(t *testing.T) {
	t.Parallel()

	regionalMock, globalMock := emptyMocks(t)
	router := testWaitlistRouter(t, regionalMock, globalMock)

	rec := postWaitlist(t, router, `{
		"full_name": "",
		"email": "jane@acme.com",
		"country_id": "`+testCountryID+`"
	}`)

	assertStatus(t, rec, http.StatusBadRequest)
	errBody := decodeError(t, rec)
	if errBody.Details["field"] != "full_name" {
		t.Fatalf("field: got %v, want full_name", errBody.Details["field"])
	}

	assertMocksMet(t, regionalMock, globalMock)
}

func TestWaitlistSignup_unknownCountry(t *testing.T) {
	t.Parallel()

	regionalMock, globalMock := emptyMocks(t)
	lookupSQL, lookupArgs, err := query.LookupCountryByID(uuid.MustParse(testCountryID))
	if err != nil {
		t.Fatalf("LookupCountryByID: %v", err)
	}
	globalMock.ExpectQuery(lookupSQL).WithArgs(lookupArgs...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "slug", "region", "icon_key"}))

	router := testWaitlistRouter(t, regionalMock, globalMock)
	rec := postWaitlist(t, router, `{
		"full_name": "Jerry",
		"email": "jane@acme.com",
		"country_id": "`+testCountryID+`"
	}`)

	assertStatus(t, rec, http.StatusBadRequest)
	errBody := decodeError(t, rec)
	if errBody.Message != apperror.MsgInvalidCountryID {
		t.Fatalf("message: got %q", errBody.Message)
	}
	if errBody.Details["field"] != "country_id" {
		t.Fatalf("field: got %v, want country_id", errBody.Details["field"])
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
		"Jerry", "dup@acme.com", uuid.MustParse(testCountryID), "uk", "explicit", []byte("{}"),
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

	h := NewWaitlistHandler(dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{
		region.RegionUK: regionalMock,
	}), globalMock)

	country := Country{
		ID:     uuid.MustParse(testCountryID),
		Name:   "United Kingdom",
		Slug:   "united-kingdom",
		Region: region.RegionUK,
	}

	err = h.signup(context.Background(), country, "Jerry", "dup@acme.com")
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
		"Jerry", "jane@acme.com", uuid.MustParse(testCountryID), "uk", "explicit", []byte("{}"),
	)
	if err != nil {
		t.Fatalf("InsertWaitlistSignup: %v", err)
	}
	regionalMock.ExpectQuery(waitlistSQL).WithArgs(waitlistArgs...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(1)))
	regionalMock.ExpectCommit()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	expectCountryLookup(t, globalMock, testCountryID)
	registrySQL, registryArgs, err := query.InsertUsersRegistry("jane@acme.com", "uk", "explicit")
	if err != nil {
		t.Fatalf("InsertUsersRegistry: %v", err)
	}
	globalMock.ExpectExec(registrySQL).WithArgs(registryArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	return regionalMock, globalMock
}

func expectCountryLookup(t *testing.T, globalMock pgxmock.PgxPoolIface, countryID string) {
	t.Helper()

	iconKey := "flag-uk"
	lookupSQL, lookupArgs, err := query.LookupCountryByID(uuid.MustParse(countryID))
	if err != nil {
		t.Fatalf("LookupCountryByID: %v", err)
	}
	globalMock.ExpectQuery(lookupSQL).WithArgs(lookupArgs...).WillReturnRows(
		pgxmock.NewRows([]string{"id", "name", "slug", "region", "icon_key"}).
			AddRow(uuid.MustParse(countryID), "United Kingdom", "united-kingdom", region.RegionUK, &iconKey),
	)
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
	}), globalMock)

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
