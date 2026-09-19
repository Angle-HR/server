package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
)

func TestPutBusinessProfile_globalAccount(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mock.Close)
	userID, roleID := uuid.New(), uuid.New()
	roleSQL, roleArgs, err := query.CompanyRoleByID(roleID)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(roleSQL)).WithArgs(roleArgs...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name"}).AddRow(roleID, "Owner"))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE pending_users`).WithArgs(userID, "Jane Doe").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO pending_organizations`).WithArgs(userID, "Example Ltd", roleID).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`SELECT user_id, current_step, completed_steps FROM pending_onboarding_progress`).WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "current_step", "completed_steps"}).
			AddRow(userID, "profile", []string{"verify_email"}))
	mock.ExpectExec(`INSERT INTO pending_onboarding_progress`).
		WithArgs(userID, "profile", []string{"verify_email", "profile"}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	h := NewProductOnboardingHandler(nil, mock, nil, nil)
	req := httptest.NewRequest(http.MethodPut, "/onboarding/profile", strings.NewReader(
		`{"account_type":"business","legal_full_name":"Jane Doe","legal_business_name":"Example Ltd","company_role_id":"`+roleID.String()+`"}`))
	req = req.WithContext(auth.WithUser(req.Context(), userID, region.RegionGlobal))
	rec := httptest.NewRecorder()
	h.putProfile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body %s; expectations: %v", rec.Code, rec.Body.String(), mock.ExpectationsWereMet())
	}
	var result struct {
		Data ProductProfileData `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Region != "global" || result.Data.Onboarding.NextStep == nil || *result.Data.Onboarding.NextStep != "identification_address" {
		t.Fatalf("unexpected profile response: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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

	req := httptest.NewRequest(http.MethodGet, "/onboarding/business-types", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body %s", rec.Code, rec.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
