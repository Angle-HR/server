package hiringhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/rbac"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
)

const (
	tJob  = "22222222-2222-4222-8222-222222222222"
	tApp  = "33333333-3333-4333-8333-333333333333"
	tUser = "55555555-5555-4555-8555-555555555555"
)

type stubDirectory struct{ err error }

func (d stubDirectory) Scope(_ context.Context, userID string, _ region.Region) (*draft.Service, draft.Caller, error) {
	if d.err != nil {
		return nil, draft.Caller{}, d.err
	}
	return nil, draft.Caller{UserID: userID, OrgID: "11111111-1111-4111-8111-111111111111",
		Perms: rbac.NewSet(rbac.JobViewAll, rbac.JobManageAny, rbac.JobRetentionSet)}, nil
}

type stubAppStore struct{ change error }

func (s *stubAppStore) JobAccess(context.Context, string, string, string) (*applications.JobAccess, error) {
	return &applications.JobAccess{JobID: tJob, CreatedBy: tUser, RetentionMonths: 6}, nil
}
func (s *stubAppStore) List(context.Context, string, string, applications.StoreListQuery) ([]applications.Summary, map[string]int, error) {
	return nil, nil, nil
}
func (s *stubAppStore) Open(_ context.Context, _ string, in applications.OpenInput) (*applications.Detail, error) {
	return &applications.Detail{Summary: applications.Summary{ID: in.AppID, SubmittedAt: time.Unix(0, 0).UTC()}}, nil
}
func (s *stubAppStore) FileInfo(context.Context, string, string, string, string, string) (*applications.FileInfo, error) {
	return nil, applications.ErrNotFound
}
func (s *stubAppStore) LoadChange(context.Context, string, string, string) (*applications.ChangeState, error) {
	return &applications.ChangeState{Status: applications.StatusFlagged}, nil
}
func (s *stubAppStore) ApplyChange(context.Context, string, *applications.Change) error {
	return s.change
}
func (s *stubAppStore) GetApplicantSettings(context.Context, string) (int, bool, error) {
	return 0, true, nil
}
func (s *stubAppStore) SaveApplicantSettings(context.Context, string, int, bool) error { return nil }

func appRouter(h *HiringHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterProtectedRoutes(r)
	return r
}

func appHandler(dir HiringDirectory, st applications.Store) *HiringHandler {
	h := NewHiringHandler(dir, func(context.Context) (string, region.Region, bool) { return tUser, region.Region("uk"), true })
	if st != nil {
		h.Applications = func(region.Region) (*applications.Service, error) { return &applications.Service{Store: st}, nil }
	}
	return h
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestApplicationRoutesNeedAService(t *testing.T) {
	rec := do(appRouter(appHandler(stubDirectory{}, nil)), http.MethodGet, "/jobs/"+tJob+"/applications", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
}

func TestApplicationListAndOpenAreNotCached(t *testing.T) {
	h := appRouter(appHandler(stubDirectory{}, &stubAppStore{}))
	rec := do(h, http.MethodGet, "/jobs/"+tJob+"/applications?status=received,flagged&limit=5", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	var body struct {
		Data struct {
			Items  []any          `json:"items"`
			Counts map[string]int `json:"counts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Data.Items == nil {
		t.Fatalf("empty list must be [] not null: %v %s", err, rec.Body)
	}
	rec = do(h, http.MethodGet, "/jobs/"+tJob+"/applications/"+tApp, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), tApp) {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	if rec = do(h, http.MethodGet, "/jobs/"+tJob+"/applications?limit=zero", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}
	if rec = do(h, http.MethodGet, "/jobs/"+tJob+"/applications?status=bogus", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", rec.Code)
	}
}

func TestApplicationDecisionErrors(t *testing.T) {
	h := appRouter(appHandler(stubDirectory{}, &stubAppStore{}))
	// the application is flagged: a plain status change must be refused with the review reason
	rec := do(h, http.MethodPost, "/jobs/"+tJob+"/applications/"+tApp+"/status", `{"status":"rejected"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "review_required") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// reviewing a flagged application nobody opened
	rec = do(h, http.MethodPost, "/jobs/"+tJob+"/applications/"+tApp+"/review", `{"decision":"reject"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_open_review") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec = do(h, http.MethodPost, "/jobs/"+tJob+"/applications/"+tApp+"/status", `{"status":"hired","extra":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec = do(h, http.MethodGet, "/jobs/"+tJob+"/applications/"+tApp+"/files/"+tApp, ""); rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusNotFound && rec.Code != http.StatusInternalServerError {
		t.Fatalf("file: %d", rec.Code)
	}
}

func TestApplicationErrorMapping(t *testing.T) {
	for err, reason := range map[error]string{
		applications.ErrInvalidTransition: "invalid_transition", applications.ErrReviewRequired: "review_required",
		applications.ErrNoReview: "no_open_review", applications.ErrNotOpened: "not_opened", applications.ErrStale: "changed",
	} {
		var ae *apperror.AppError
		if !errors.As(applicationsError(err), &ae) || ae.Code != apperror.CodeConflict || ae.Details["reason"] != reason {
			t.Errorf("%v: %+v", err, ae)
		}
	}
	var ae *apperror.AppError
	if !errors.As(applicationsError(applications.ErrNotFound), &ae) || ae.Code != apperror.CodeNotFound {
		t.Errorf("not found: %+v", ae)
	}
}

func TestApplicantSettingsRoutes(t *testing.T) {
	h := appRouter(appHandler(stubDirectory{}, &stubAppStore{}))
	rec := do(h, http.MethodGet, "/hiring/applicant-settings", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"send_status_emails":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = do(h, http.MethodPut, "/hiring/applicant-settings", `{"reapply_block_months":6}`)
	if rec.Code != http.StatusBadRequest { // no key on this service: cannot turn the block on
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = do(h, http.MethodPut, "/hiring/applicant-settings", `{"send_status_emails":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
