package orghttp

import (
	"github.com/Angle-HR/server/pkg/apperror"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
)

// A business gives its country on the profile step (it decides the region); the save path itself is covered by the
// local e2e scripts because it moves the account between databases.
func TestPutBusinessProfile_requiresCountry(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mock.Close)
	userID, roleID := uuid.New(), uuid.New()
	h := NewProductOnboardingHandler(nil, mock, nil, nil)
	req := httptest.NewRequest(http.MethodPut, "/onboarding/profile", strings.NewReader(
		`{"account_type":"business","legal_full_name":"Jane Doe","legal_business_name":"Example Ltd","company_role_id":"`+roleID.String()+`"}`))
	req = req.WithContext(auth.WithUser(req.Context(), userID, region.RegionGlobal))
	rec := httptest.NewRecorder()
	h.putProfile(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid country_id") {
		t.Fatalf("status: got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestListBusinessTypes_emptyCatalog(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { mock.Close() })

	sql, args, err := query.ListBusinessTypes()
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	mock.ExpectQuery(sql).WithArgs(args...).WillReturnRows(
		pgxmock.NewRows([]string{"id", "name", "slug"}),
	)

	h := NewProductOnboardingHandler(nil, mock, nil, nil)
	router := chi.NewRouter()
	h.RegisterRoutes(router)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/onboarding/business-types", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body %s", rec.Code, rec.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestProductOnboarding_verifyAddressNotImplemented(t *testing.T) {
	t.Parallel()

	h := NewProductOnboardingHandler(nil, nil, nil, nil)
	router := chi.NewRouter()
	router.Post("/onboarding/address/verify", h.verifyAddress)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/onboarding/address/verify", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status: got %d", rec.Code)
	}

	errBody := decodeError(t, rec)
	if errBody.Code != apperror.CodeNotImplemented {
		t.Fatalf("code: got %q", errBody.Code)
	}
}
