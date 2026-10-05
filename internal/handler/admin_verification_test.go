package handler

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/region"
)

var testReviewerID = uuid.MustParse("55555555-5555-4555-8555-555555555555")

func adminVerificationRouter(regional, global pgxmock.PgxPoolIface) http.Handler {
	router := dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{region.RegionUK: regional})
	h := &AdminHandler{Router: router, GlobalDB: global}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithAdmin(req.Context(), testReviewerID, "ops@example.com", []string{"verification:review"})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Get("/verification", h.listVerificationQueue)
	r.Get("/verification/{organizationID}", h.getVerificationItem)
	r.Post("/verification/{organizationID}/review", h.reviewVerification)
	return r
}

func expectQueueItem(global pgxmock.PgxPoolIface) {
	global.ExpectQuery(regexp.QuoteMeta("FROM admin.verification_queue")).
		WithArgs(testKYBOrgID).
		WillReturnRows(pgxmock.NewRows([]string{"region", "submitted_at"}).AddRow("uk", time.Now().UTC()))
}

func expectPendingTier2Record(regional pgxmock.PgxPoolIface) {
	checked := time.Now().UTC()
	status := "pending"
	regional.ExpectQuery(regexp.QuoteMeta("FROM accounts.organization_verifications")).
		WithArgs(testKYBOrgID).
		WillReturnRows(pgxmock.NewRows([]string{
			"organization_id", "country_code", "registration_number", "identifiers", "legal_name_submitted",
			"submitted_address", "address_type", "tier", "failure_reason", "registry_name", "registered_address",
			"attempts", "checked_at", "verified_at", "reviewer_id", "notices_sent", "kyb_status",
		}).AddRow(testKYBOrgID, "KE", "PVT-123", []byte(`{}`), "Acme Kenya Ltd",
			[]byte(`{"Line1":"1 Moi Ave","City":"Nairobi"}`), "", int16(2), "", "", "",
			int32(1), &checked, (*time.Time)(nil), "", int16(0), &status))
}

func TestAdminVerification_list(t *testing.T) {
	regional, global := kybMocks(t)
	submitted := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	global.ExpectQuery(regexp.QuoteMeta("FROM admin.verification_queue")).
		WithArgs(defaultPageLimit, 0).
		WillReturnRows(pgxmock.NewRows([]string{"id", "organization_id", "region", "country_code", "submitted_at"}).
			AddRow("q1", testKYBOrgID, "africa", "KE", submitted))

	rec := kybDo(t, adminVerificationRouter(regional, global), http.MethodGet, "/verification", "")
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `"organization_id":"`+testKYBOrgID+`"`) || !strings.Contains(body, `"country_code":"KE"`) {
		t.Fatalf("unexpected body: %s", body)
	}
	assertMocksMet(t, regional, global)
}

func TestAdminVerification_detail(t *testing.T) {
	regional, global := kybMocks(t)
	expectQueueItem(global)
	expectPendingTier2Record(regional)

	rec := kybDo(t, adminVerificationRouter(regional, global), http.MethodGet, "/verification/"+testKYBOrgID, "")
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `"legal_name":"Acme Kenya Ltd"`) || !strings.Contains(body, `"registration_number":"PVT-123"`) {
		t.Fatalf("unexpected body: %s", body)
	}
	assertMocksMet(t, regional, global)
}

func TestAdminVerification_detailNotQueued(t *testing.T) {
	regional, global := kybMocks(t)
	global.ExpectQuery(regexp.QuoteMeta("FROM admin.verification_queue")).
		WithArgs(testKYBOrgID).WillReturnError(pgx.ErrNoRows)

	rec := kybDo(t, adminVerificationRouter(regional, global), http.MethodGet, "/verification/"+testKYBOrgID, "")
	assertStatus(t, rec, http.StatusNotFound)
}

func TestAdminVerification_invalidOrganizationID(t *testing.T) {
	regional, global := kybMocks(t)

	rec := kybDo(t, adminVerificationRouter(regional, global), http.MethodGet, "/verification/not-a-uuid", "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestAdminVerification_reviewInvalidDecision(t *testing.T) {
	regional, global := kybMocks(t)

	rec := kybDo(t, adminVerificationRouter(regional, global), http.MethodPost,
		"/verification/"+testKYBOrgID+"/review", `{"decision":"maybe"}`)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestAdminVerification_rejectNeedsReason(t *testing.T) {
	regional, global := kybMocks(t)
	expectQueueItem(global)

	rec := kybDo(t, adminVerificationRouter(regional, global), http.MethodPost,
		"/verification/"+testKYBOrgID+"/review", `{"decision":"reject"}`)
	assertStatus(t, rec, http.StatusBadRequest)
	assertMocksMet(t, regional, global)
}

func expectSaveAndResolve(regional, global pgxmock.PgxPoolIface, queueStatus string) {
	any16 := make([]any, 16)
	for i := range any16 {
		any16[i] = pgxmock.AnyArg()
	}
	regional.ExpectBegin()
	regional.ExpectExec("INSERT INTO accounts.organization_verifications").WithArgs(any16...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	regional.ExpectExec("UPDATE accounts.organizations").
		WithArgs(testKYBOrgID, pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	regional.ExpectExec("INSERT INTO accounts.organization_verification_events").
		WithArgs(testKYBOrgID, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), testReviewerID.String(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	regional.ExpectCommit()
	global.ExpectExec("UPDATE admin.verification_queue").
		WithArgs(testKYBOrgID, queueStatus, testReviewerID.String()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

func TestAdminVerification_approve(t *testing.T) {
	regional, global := kybMocks(t)
	expectQueueItem(global)
	expectPendingTier2Record(regional)
	expectSaveAndResolve(regional, global, "approved")

	rec := kybDo(t, adminVerificationRouter(regional, global), http.MethodPost,
		"/verification/"+testKYBOrgID+"/review", `{"decision":"approve"}`)
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"status":"verified"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	assertMocksMet(t, regional, global)
}

func TestAdminVerification_reject(t *testing.T) {
	regional, global := kybMocks(t)
	expectQueueItem(global)
	expectPendingTier2Record(regional)
	expectSaveAndResolve(regional, global, "rejected")

	rec := kybDo(t, adminVerificationRouter(regional, global), http.MethodPost,
		"/verification/"+testKYBOrgID+"/review", `{"decision":"reject","reason":"number_not_found"}`)
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"failed"`) || !strings.Contains(body, `"failure_reason":"number_not_found"`) {
		t.Fatalf("unexpected body: %s", body)
	}
	assertMocksMet(t, regional, global)
}
