package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
)

const testWaitlistToken = "550e8400-e29b-41d4-a716-446655440000"

func TestOnboardingSubmit_unknownToken(t *testing.T) {
	t.Parallel()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	lookupSQL, lookupArgs, err := query.LookupWaitlistRegistryByToken(uuid.MustParse(testWaitlistToken))
	if err != nil {
		t.Fatalf("LookupWaitlistRegistryByToken: %v", err)
	}
	globalMock.ExpectQuery(lookupSQL).WithArgs(lookupArgs...).WillReturnError(pgx.ErrNoRows)

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	router := testOnboardingRouter(t, regionalMock, globalMock)
	rec := postOnboarding(t, router, minimalOnboardingBody())

	assertStatus(t, rec, http.StatusNotFound)
	assertMocksMet(t, globalMock, regionalMock)
}

func TestOnboardingSubmit_alreadySubmitted(t *testing.T) {
	t.Parallel()

	token := uuid.MustParse(testWaitlistToken)
	globalMock, regionalMock := expectRegistryLookup(t, token, "jane@acme.com", "uk")

	lookupSQL, lookupArgs, err := query.LookupWaitlistByUUID(token)
	if err != nil {
		t.Fatalf("LookupWaitlistByUUID: %v", err)
	}
	submitted := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	regionalMock.ExpectQuery(lookupSQL).WithArgs(lookupArgs...).WillReturnRows(
		pgxmock.NewRows([]string{"id", "full_name", "email", "onboarding_submitted_at"}).
			AddRow(int64(1), "Jane", "jane@acme.com", &submitted),
	)

	router := testOnboardingRouter(t, regionalMock, globalMock)
	rec := postOnboarding(t, router, minimalOnboardingBody())

	assertStatus(t, rec, http.StatusConflict)
	errBody := decodeError(t, rec)
	if errBody.Message != apperror.MsgOnboardingAlreadySubmitted {
		t.Fatalf("message: got %q", errBody.Message)
	}

	assertMocksMet(t, globalMock, regionalMock)
}

func expectRegistryLookup(
	t *testing.T,
	token uuid.UUID,
	email, reg string,
) (pgxmock.PgxPoolIface, pgxmock.PgxPoolIface) {
	t.Helper()

	globalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool global: %v", err)
	}
	t.Cleanup(func() { globalMock.Close() })

	lookupSQL, lookupArgs, err := query.LookupWaitlistRegistryByToken(token)
	if err != nil {
		t.Fatalf("LookupWaitlistRegistryByToken: %v", err)
	}
	globalMock.ExpectQuery(lookupSQL).WithArgs(lookupArgs...).WillReturnRows(
		pgxmock.NewRows([]string{"email", "region"}).AddRow(email, reg),
	)

	regionalMock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool regional: %v", err)
	}
	t.Cleanup(func() { regionalMock.Close() })

	return globalMock, regionalMock
}

func testOnboardingRouter(
	t *testing.T,
	regionalMock pgxmock.PgxPoolIface,
	globalMock pgxmock.PgxPoolIface,
) chi.Router {
	t.Helper()

	h := NewOnboardingHandler(dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{
		region.RegionUK: regionalMock,
	}), globalMock, nil)

	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		h.RegisterRoutes(r)
	})

	return router
}

func postOnboarding(t *testing.T, router chi.Router, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/waitlist/onboarding", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func minimalOnboardingBody() string {
	body := map[string]any{
		"token":              testWaitlistToken,
		"industry_ids":       []string{"10000000-0000-4000-8000-000000000001"},
		"tool_ids":           []string{"20000000-0000-4000-8000-000000000001"},
		"frustration_ids":    []string{"30000000-0000-4000-8000-000000000001"},
		"role_id":            "40000000-0000-4000-8000-000000000001",
		"team_size_id":       "50000000-0000-4000-8000-000000000001",
		"wants_early_access": true,
		"wants_user_testing": false,
	}

	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}

	return string(raw)
}
