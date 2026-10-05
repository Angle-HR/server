package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
)

const testKYBOrgID = "33333333-3333-4333-8333-333333333333"

var testKYBUserID = uuid.MustParse("44444444-4444-4444-8444-444444444444")

func kybTestRouter(t *testing.T, reg region.Region, regional, global pgxmock.PgxPoolIface) http.Handler {
	t.Helper()
	router := dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{region.RegionUK: regional})
	h := NewKYBHandler(router, global, kyb.NewRegistry())
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), testKYBUserID, reg)))
		})
	})
	r.Route("/api/v1", h.RegisterProtectedRoutes)
	return r
}

func kybMocks(t *testing.T) (regional, global pgxmock.PgxPoolIface) {
	t.Helper()
	regional, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	global, err = pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	return regional, global
}

func kybDo(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func expectKYBOrg(regional pgxmock.PgxPoolIface) {
	regional.ExpectQuery(regexp.QuoteMeta("FROM accounts.organizations WHERE owner_user_id")).
		WithArgs(testKYBUserID).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(testKYBOrgID))
}

func TestKYBStatus_notStarted(t *testing.T) {
	regional, global := kybMocks(t)
	expectKYBOrg(regional)
	regional.ExpectQuery(regexp.QuoteMeta("FROM accounts.organization_verifications")).
		WithArgs(testKYBOrgID).WillReturnError(pgx.ErrNoRows)

	rec := kybDo(t, kybTestRouter(t, region.RegionUK, regional, global), http.MethodGet, "/api/v1/organization/verification/", "")
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"status":"not_started"`) ||
		!strings.Contains(rec.Body.String(), `"can_publish":false`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	assertMocksMet(t, regional, global)
}

func TestKYBStatus_pendingAccountHasNoOrganization(t *testing.T) {
	regional, global := kybMocks(t)

	rec := kybDo(t, kybTestRouter(t, region.RegionGlobal, regional, global), http.MethodGet, "/api/v1/organization/verification/", "")
	assertStatus(t, rec, http.StatusBadRequest)
	if got := decodeError(t, rec).Code; got != apperror.CodeOnboardingIncomplete {
		t.Fatalf("code: got %q", got)
	}
}

func TestKYBStatus_noOrganization(t *testing.T) {
	regional, global := kybMocks(t)
	regional.ExpectQuery(regexp.QuoteMeta("FROM accounts.organizations WHERE owner_user_id")).
		WithArgs(testKYBUserID).WillReturnError(pgx.ErrNoRows)

	rec := kybDo(t, kybTestRouter(t, region.RegionUK, regional, global), http.MethodGet, "/api/v1/organization/verification/", "")
	assertStatus(t, rec, http.StatusNotFound)
}

func TestKYBSubmit_invalidBody(t *testing.T) {
	regional, global := kybMocks(t)

	rec := kybDo(t, kybTestRouter(t, region.RegionUK, regional, global), http.MethodPost, "/api/v1/organization/verification/", "{")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestKYBSubmit_missingFields(t *testing.T) {
	regional, global := kybMocks(t)
	expectKYBOrg(regional)

	rec := kybDo(t, kybTestRouter(t, region.RegionUK, regional, global), http.MethodPost, "/api/v1/organization/verification/",
		`{"country_code":"GB"}`)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestKYBError_mapping(t *testing.T) {
	cases := map[error]int{
		kyb.ErrInvalidInput:           http.StatusBadRequest,
		kyb.ErrResubmissionBlocked:    http.StatusForbidden,
		kyb.ErrAlreadyVerified:        http.StatusConflict,
		kyb.ErrReviewInProgress:       http.StatusConflict,
		kyb.ErrNothingToRetry:         http.StatusConflict,
		kyb.ErrActionNotAllowed:       http.StatusConflict,
		kyb.ErrTemporarilyUnavailable: http.StatusServiceUnavailable,
	}
	for in, want := range cases {
		if got := apperror.HTTPStatus(kybError(in)); got != want {
			t.Errorf("%v: got %d want %d", in, got, want)
		}
	}
}

func TestKYBStatusData_failedNameMismatch(t *testing.T) {
	d := kybStatusData(&kyb.Record{Status: kyb.StatusFailed, FailureReason: kyb.ReasonNameMismatch, RegistryName: "ACME LIMITED"})
	if !d.CanRetry || !d.LightRetry || !d.NameConfirmable || d.AddressConfirm || d.CanPublish ||
		d.Display != string(kyb.DisplayActionRequired) {
		t.Fatalf("unexpected: %+v", d)
	}
	d = kybStatusData(&kyb.Record{Status: kyb.StatusFailed, FailureReason: kyb.ReasonInactiveEntity})
	if d.CanRetry || d.Display != string(kyb.DisplayFailed) {
		t.Fatalf("hard block should not allow retry: %+v", d)
	}
}
