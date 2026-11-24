package hiringhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/pkg/rbac"
)

type fakeAudit struct {
	rows   []draft.AuditEvent
	got    draft.AuditFilter
	tenant string
	calls  int
}

func (f *fakeAudit) AuditEvents(_ context.Context, tenant string, flt draft.AuditFilter) ([]draft.AuditEvent, error) {
	f.calls++
	f.got, f.tenant = flt, tenant
	n := flt.Limit
	if n > len(f.rows) {
		n = len(f.rows)
	}
	return f.rows[:n], nil
}

func auditRows(n int) []draft.AuditEvent {
	actor := userA
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	out := make([]draft.AuditEvent, n)
	for i := range out {
		src := draft.AuditSourceHiring
		if i%2 == 1 {
			src = draft.AuditSourceApplications
		}
		out[i] = draft.AuditEvent{
			Source: src, ID: int64(100 - i), JobID: "cccccccc-0000-4000-8000-000000000001", ActorID: &actor,
			Action: "job.updated", Diff: json.RawMessage(`{"title":"Cook"}`), CreatedAt: base.Add(-time.Duration(i) * time.Minute),
		}
	}
	return out
}

func TestAuditTrailNeedsPermission(t *testing.T) {
	e := newEnv(rbac.RoleLineManager)
	fa := &fakeAudit{rows: auditRows(3)}
	e.dir.svc.Audit = fa
	r := e.do(t, "GET", "/jobs/audit", "")
	if r.Status != http.StatusForbidden || fa.calls != 0 {
		t.Fatalf("status %d calls %d: want 403 before any read", r.Status, fa.calls)
	}
}

func TestAuditTrailPagesAndFilters(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	fa := &fakeAudit{rows: auditRows(5)}
	e.dir.svc.Audit = fa

	r := e.do(t, "GET", "/jobs/audit?limit=2&job_id=cccccccc-0000-4000-8000-000000000001&actor_id="+userA+
		"&action=job.updated&from=2026-10-01&to=2026-10-08", "")
	if r.Status != http.StatusOK {
		t.Fatalf("status %d: %s", r.Status, r.Err.Message)
	}
	var page []draft.AuditEvent
	if err := json.Unmarshal(r.Data, &page); err != nil || len(page) != 2 {
		t.Fatalf("page = %d rows, err %v", len(page), err)
	}
	if fa.tenant != org || fa.got.Limit != 3 || fa.got.JobID == "" || fa.got.ActorID != userA || fa.got.Action != "job.updated" {
		t.Fatalf("filter = %+v tenant %q", fa.got, fa.tenant)
	}
	// "to" as a date includes that whole day: the exclusive bound is the next midnight.
	if fa.got.From == nil || fa.got.To == nil || !fa.got.To.Equal(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("time range = %v..%v", fa.got.From, fa.got.To)
	}
	if r.Meta["has_more"] != true {
		t.Fatalf("meta = %v", r.Meta)
	}
	next, _ := r.Meta["next_cursor"].(string)
	cur, err := draft.DecodeAuditCursor(next)
	if err != nil || cur.ID != page[1].ID || cur.Source != page[1].Source || !cur.CreatedAt.Equal(page[1].CreatedAt) {
		t.Fatalf("cursor = %+v err %v", cur, err)
	}

	// The cursor comes back to the store as the position to continue after.
	r = e.do(t, "GET", "/jobs/audit?limit=2&cursor="+next, "")
	if r.Status != http.StatusOK || fa.got.After == nil || fa.got.After.ID != page[1].ID {
		t.Fatalf("second page: status %d after %+v", r.Status, fa.got.After)
	}
}

func TestAuditTrailValidation(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	e.dir.svc.Audit = &fakeAudit{}
	for _, q := range []string{
		"job_id=nope", "actor_id=nope", "from=yesterday", "to=2026-13-40", "cursor=%25%25", "limit=0", "limit=101", "limit=x",
		"from=2026-10-08&to=2026-10-01",
	} {
		if r := e.do(t, "GET", "/jobs/audit?"+q, ""); r.Status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, r.Status)
		}
	}
}

func TestAuditTrailWithoutReaderIs503(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	if r := e.do(t, "GET", "/jobs/audit", ""); r.Status != http.StatusServiceUnavailable {
		t.Fatalf("status %d", r.Status)
	}
}

func TestAuditTrailEmptyPageIsAnArray(t *testing.T) {
	e := newEnv(rbac.RoleLegal)
	e.dir.svc.Audit = &fakeAudit{}
	r := e.do(t, "GET", "/jobs/audit", "")
	if r.Status != http.StatusOK || string(r.Data) != "[]" {
		t.Fatalf("status %d data %s", r.Status, r.Data)
	}
}
