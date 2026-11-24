package orghttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
)

func TestProductAddressSearchPayload(t *testing.T) {
	var req productAddressBody
	if err := json.Unmarshal([]byte(`{"country_id":"a1b2c3d4-e5f6-4789-a012-3456789abcde","entry_mode":"search","formatted_address":"Addlestone, UK","identification":{"registration_number":"66767676"}}`), &req); err != nil {
		t.Fatal(err)
	}
	v := validator.New()
	if err := v.Struct(req); err != nil {
		t.Fatalf("search address should allow omitted street, city, state/county, and postcode: %v", err)
	}
	req.EntryMode = "manual"
	err := v.Struct(req)
	fields, ok := err.(validator.ValidationErrors)
	if !ok || len(fields) != 4 {
		t.Fatalf("manual address should require street, city, state/county, and postcode: %v", err)
	}
	for _, field := range fields {
		if (field.Field() != "Line1" && field.Field() != "PostCode" && field.Field() != "City" && field.Field() != "StateOrCounty") || field.Tag() != "required_if" {
			t.Fatalf("unexpected validation error: %v", field)
		}
	}
	req.Line1 = "1 High Street"
	req.PostCode = "KT15 1AA"
	req.City = "Addlestone"
	req.StateOrCounty = "England"
	if err := v.Struct(req); err != nil {
		t.Fatalf("complete manual address should pass: %v", err)
	}
}

func TestPersistAddressPreservesDatabaseError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mock.Close)
	cause := errors.New("database unavailable")
	mock.ExpectBegin().WillReturnError(cause)
	h := &ProductOnboardingHandler{}
	_, _, err = h.persistAddress(context.Background(), mock, region.RegionGlobal, uuid.New(), uuid.New(), &productAddressBody{}, nil, "")
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "save address: begin transaction") {
		t.Fatalf("expected operation and underlying error, got %v", err)
	}
	code, message := apperror.Public(err)
	if code != apperror.CodeInternalError || message != apperror.MsgInternalServerError {
		t.Fatalf("unexpected public error: %s: %s", code, message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPutAddressRequiresProfile(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mock.Close)
	userID, countryID := uuid.New(), uuid.New()
	countrySQL, countryArgs, err := query.LookupCountryByID(countryID)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(countrySQL)).WithArgs(countryArgs...).WillReturnRows(
		pgxmock.NewRows([]string{"id", "name", "slug", "region", "icon_key"}).AddRow(countryID, "United Kingdom", "united-kingdom", "uk", nil))
	userSQL, userArgs, err := query.LookupPendingUserByID(userID)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(userSQL)).WithArgs(userArgs...).WillReturnRows(
		pgxmock.NewRows([]string{"id", "email", "password_hash", "email_verified_at", "onboarding_completed_at", "account_type", "first_name", "last_name", "legal_full_name", "country_id", "totp_secret", "totp_enabled_at"}).AddRow(userID, "test@example.com", "hash", nil, nil, nil, nil, nil, nil, nil, nil, nil))
	h := NewProductOnboardingHandler(nil, mock, nil, nil)
	req := httptest.NewRequest(http.MethodPut, "/onboarding/address", strings.NewReader(`{"country_id":"`+countryID.String()+`","entry_mode":"search","formatted_address":"United Kingdom","identification":{"registration_number":"17066367"}}`))
	req = req.WithContext(auth.WithUser(req.Context(), userID, region.RegionGlobal))
	rec := httptest.NewRecorder()
	h.putAddress(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error.Code != apperror.CodeOnboardingIncomplete || result.Error.Message != "complete the profile step before continuing" {
		t.Fatalf("unexpected error: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
