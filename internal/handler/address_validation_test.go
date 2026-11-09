package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
)

func TestCleanAddressFields(t *testing.T) {
	t.Parallel()
	l1, city, st, pc := "  1 High St ", "London", "Greater London", "N1 1AA"
	if err := cleanAddressFields(&l1, nil, &city, &st, &pc, true); err != nil || l1 != "1 High St" {
		t.Fatalf("valid: err=%v l1=%q", err, l1)
	}

	blank, nul := "   ", "Lon\x00don"
	err := cleanAddressFields(&blank, nil, &nul, &st, &pc, true)
	details := apperror.PublicDetails(err)
	fields, _ := details["fields"].([]fieldError)
	if len(fields) != 2 || fields[0].Path != "line_1" || fields[1].Path != "city" ||
		fields[0].Message != "Enter a valid address" {
		t.Fatalf("fields = %+v (err=%v)", fields, err)
	}
	if apperror.HTTPStatus(err) != http.StatusBadRequest {
		t.Fatalf("status = %d", apperror.HTTPStatus(err))
	}

	// Search mode does not require the manual fields.
	e1, e2, e3, e4 := "", "", "", ""
	if err := cleanAddressFields(&e1, nil, &e2, &e3, &e4, false); err != nil {
		t.Fatalf("search mode blank: %v", err)
	}
}

func TestIsBadInputDBError(t *testing.T) {
	t.Parallel()
	if !isBadInputDBError(&pgconn.PgError{Code: "22021"}) || !isBadInputDBError(&pgconn.PgError{Code: "23514"}) {
		t.Fatal("expected data exception / check violation to be bad input")
	}
	if isBadInputDBError(&pgconn.PgError{Code: "40001"}) || isBadInputDBError(errors.New("boom")) {
		t.Fatal("other errors must stay internal")
	}
}

type failingVerifier struct{ err error }

func (f failingVerifier) Verify(context.Context, onboarding.ProductAddress) (onboarding.VerificationResult, error) {
	return onboarding.VerificationResult{}, f.err
}

func postVerifyAddress(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewProductOnboardingHandler(nil, nil, nil, nil)
	h.AddressProvider = failingVerifier{err: onboarding.ErrInvalidAddressInput}
	router := chi.NewRouter()
	router.Post("/onboarding/address/verify", h.verifyAddress)
	req := httptest.NewRequestWithContext(
		auth.WithUser(t.Context(), uuid.New(), region.RegionUK),
		http.MethodPost, "/onboarding/address/verify", bytes.NewBufferString(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestVerifyAddress_blankManualFieldsReturn400(t *testing.T) {
	t.Parallel()
	rec := postVerifyAddress(t, `{"country_id":"`+uuid.NewString()+`","entry_mode":"manual",`+
		`"line_1":"   ","city":"London","state_or_county":"x","post_code":"N1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := decodeError(t, rec)
	if body.Code != apperror.CodeValidationError || body.Message != "Enter a valid address" || body.Details["fields"] == nil {
		t.Fatalf("body = %+v", body)
	}
}
