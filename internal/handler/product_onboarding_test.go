package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/query"
)

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

	h := NewProductOnboardingHandler(nil, mock, nil)
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
