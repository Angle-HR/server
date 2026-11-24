package jobmodel

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/formschema"
)

// fakeFallback is a scripted appjobs.JobSource that records what it was asked.
type fakeFallback struct {
	calls  []string
	target *appjobs.ApplyTarget // LoadForApply answer; nil: not found
	closed []appjobs.ClosedJob
	labels map[string]appjobs.JobLabel
	email  string
	access *appjobs.JobAccess
}

func (f *fakeFallback) LookupJob(_ context.Context, id string) (appjobs.Ref, error) {
	f.calls = append(f.calls, "LookupJob")
	return appjobs.Ref{PublicID: id, Region: "uk", OrganizationID: tCompany}, nil
}
func (f *fakeFallback) Regional(string) (appjobs.RegionalJobs, error) { return f, nil }
func (f *fakeFallback) LoadForApply(context.Context, string, string) (*appjobs.ApplyTarget, error) {
	f.calls = append(f.calls, "LoadForApply")
	if f.target == nil {
		return nil, apperror.ErrNotFound
	}
	c := *f.target
	return &c, nil
}
func (f *fakeFallback) JobAccess(context.Context, string, string, string) (*appjobs.JobAccess, error) {
	f.calls = append(f.calls, "JobAccess")
	return f.access, nil
}
func (f *fakeFallback) JobSummary(context.Context, string, string) (*appjobs.JobSummary, error) {
	f.calls = append(f.calls, "JobSummary")
	return nil, apperror.ErrNotFound
}
func (f *fakeFallback) FrozenQuestions(context.Context, string, string, int) ([]formschema.FormQuestion, error) {
	f.calls = append(f.calls, "FrozenQuestions")
	return nil, apperror.ErrNotFound
}
func (f *fakeFallback) ClosedJobs(context.Context, string, time.Time) ([]appjobs.ClosedJob, error) {
	f.calls = append(f.calls, "ClosedJobs")
	return f.closed, nil
}
func (f *fakeFallback) JobLabels(context.Context, string, []string) (map[string]appjobs.JobLabel, error) {
	f.calls = append(f.calls, "JobLabels")
	return f.labels, nil
}
func (f *fakeFallback) NoticeRecipient(context.Context, string) (string, string, error) {
	f.calls = append(f.calls, "NoticeRecipient")
	return f.email, "Owner Co", nil
}
func (f *fakeFallback) TenantIDs(context.Context) ([]string, error) {
	f.calls = append(f.calls, "TenantIDs")
	return []string{tCompany}, nil
}

func (f *fakeFallback) count(name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func newModel(pool *pgxpool.Pool, fb appjobs.JobSource, readThrough bool) *Model {
	return &Model{Pool: func(string) (DB, error) { return pool, nil }, Fallback: fb, ReadThrough: readThrough}
}

func regional(t *testing.T, m *Model) appjobs.RegionalJobs {
	t.Helper()
	r, err := m.Regional("uk")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestModel_LoadForApply_StatesLikeJobsource(t *testing.T) {
	pool := newDB(t)
	p := NewProjector(pools(pool))
	r := regional(t, newModel(pool, nil, false))
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	open := func() *appjobs.ApplyTarget {
		got, err := r.LoadForApply(ctx, tCompany, "PUB1")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	state := func(a *appjobs.ApplyTarget) formschema.State {
		s, _ := formschema.StateOf(a.Status, a.ClosingDate, now)
		return s
	}

	mustHandle(t, p, ev(t, TypePublished, "a1", snap(1, "published", "open")))
	a := open()
	if a.JobID != tJob || a.JobCode != "JB-7" || a.Title != "Engineer" || a.CompanyName != "Acme Ltd" ||
		a.FormID != tForm || a.FormVersion != 1 || a.RetentionMonths != 6 || a.ClosingDate != "2099-01-01" ||
		a.LawfulBasis != "legitimate_interests" || !reflect.DeepEqual(a.Markets, []string{"GB", "NG"}) ||
		len(a.Questions) != 1 || a.Questions[0].ID != "q1" || len(a.Rules) != 1 || a.Rules[0].QuestionID != "q1" ||
		len(a.Declarations) != 1 || a.Declarations[0].Category != "health" || a.Region != "" || a.TenantID != "" {
		t.Fatalf("unexpected target: %+v", a)
	}
	if state(a) != formschema.StateOpen {
		t.Fatal("published must be open")
	}

	mustHandle(t, p, ev(t, TypePaused, "a2", snap(2, "paused", "paused")))
	if s := state(open()); s != formschema.StatePaused {
		t.Fatalf("paused job state = %s", s)
	}
	mustHandle(t, p, ev(t, TypeResumed, "a3", snap(3, "published", "open")))
	if s := state(open()); s != formschema.StateOpen {
		t.Fatalf("resumed job state = %s", s)
	}
	mustHandle(t, p, ev(t, TypeClosed, "a4", snap(4, "closed", "closed")))
	if s := state(open()); s != formschema.StateClosed {
		t.Fatalf("closed job state = %s", s)
	}
	exp := snap(5, "expired", "closed")
	exp["closing_date"] = "2026-05-01"
	mustHandle(t, p, ev(t, TypeClosed, "a5", exp))
	if got := open(); got.Status != "expired" || state(got) != formschema.StateClosed {
		t.Fatalf("expired: %+v", got)
	}
	// A published job past its closing date is closed to applications before the expiry task runs.
	late := snap(6, "published", "open")
	late["closing_date"] = "2026-05-31"
	mustHandle(t, p, ev(t, TypeUpdated, "a6", late))
	if s := state(open()); s != formschema.StateClosed {
		t.Fatalf("published past closing date state = %s", s)
	}

	// Another company's id, an unknown public id and a job that never published are not found.
	if _, err := r.LoadForApply(ctx, "99999999-9999-4999-8999-999999999999", "PUB1"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("other company: %v", err)
	}
	if _, err := r.LoadForApply(ctx, tCompany, "NOPE"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	draft := snap(7, "draft", "closed")
	delete(draft, "form")
	mustHandle(t, p, ev(t, TypeClosed, "a7", draft))
	if _, err := r.LoadForApply(ctx, tCompany, "PUB1"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("no frozen form must be not found: %v", err)
	}
}

func TestModel_ReadThrough(t *testing.T) {
	pool := newDB(t)
	ctx := context.Background()
	target := &appjobs.ApplyTarget{
		JobID: tJob, JobCode: "JB-9", Title: "Chef", CompanyName: "Eats Ltd", Status: "published", ClosingDate: "2099-01-01",
		LawfulBasis: "contract", RetentionMonths: 12, Markets: []string{"GB"}, FormID: tForm, FormVersion: 3,
		Questions:    []formschema.FormQuestion{{ID: "q9", Type: "text", Label: "Hi", Config: []byte(`{}`)}},
		Declarations: nil, Rules: nil,
	}

	t.Run("off: a missing job is not found and the fallback is not asked", func(t *testing.T) {
		fb := &fakeFallback{target: target}
		r := regional(t, newModel(pool, fb, false))
		if _, err := r.LoadForApply(ctx, tCompany, "PUBX"); !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
		if len(fb.calls) != 0 {
			t.Fatalf("fallback called: %v", fb.calls)
		}
	})

	t.Run("on: asked once, written, then local", func(t *testing.T) {
		fb := &fakeFallback{target: target}
		r := regional(t, newModel(pool, fb, true))
		first, err := r.LoadForApply(ctx, tCompany, "PUBX")
		if err != nil || first.JobID != tJob {
			t.Fatalf("first: %+v %v", first, err)
		}
		second, err := r.LoadForApply(ctx, tCompany, "PUBX")
		if err != nil {
			t.Fatal(err)
		}
		if fb.count("LoadForApply") != 1 {
			t.Fatalf("fallback asked %d times, want 1", fb.count("LoadForApply"))
		}
		if first.Title != second.Title || first.FormVersion != second.FormVersion || second.Questions[0].ID != "q9" ||
			second.Declarations == nil || second.Rules == nil || second.CompanyName != "Eats Ltd" {
			t.Fatalf("local answer differs: %+v vs %+v", first, second)
		}
		var version int64
		var src, public string
		if err := pool.QueryRow(ctx, `SELECT version, source, public_id FROM hiring.app_jobs WHERE job_id=$1`, tJob).Scan(&version, &src, &public); err != nil {
			t.Fatal(err)
		}
		if version != 0 || src != SourceReadThrough || public != "PUBX" {
			t.Fatalf("row: version=%d source=%s public=%s", version, src, public)
		}
		// The form history is kept for FrozenQuestions.
		qs, err := r.FrozenQuestions(ctx, tCompany, tForm, 3)
		if err != nil || len(qs) != 1 || fb.count("FrozenQuestions") != 0 {
			t.Fatalf("frozen: %v %v calls=%v", qs, err, fb.calls)
		}
		// The first real event replaces the read-through row; a later read-through never overwrites it.
		p := NewProjector(pools(pool))
		s := snap(1, "paused", "paused")
		s["public_id"] = "PUBX"
		mustHandle(t, p, ev(t, TypePaused, "rt1", s))
		if v, st, _ := dbVersion(t, pool); v != 1 || st != "paused" {
			t.Fatalf("event must replace the read-through row: %d %s", v, st)
		}
		a, err := r.LoadForApply(ctx, tCompany, "PUBX")
		if err != nil || a.Status != "paused" {
			t.Fatalf("after event: %+v %v", a, err)
		}
		if err := r.(*Regional).Remember(ctx, tCompany, "PUBX", target); err != nil {
			t.Fatal(err)
		}
		if v, st, _ := dbVersion(t, pool); v != 1 || st != "paused" {
			t.Fatalf("read-through overwrote an event row: %d %s", v, st)
		}
	})

	t.Run("on: not found at the fallback stays not found and writes nothing", func(t *testing.T) {
		p2 := newDB(t)
		fb := &fakeFallback{}
		r := regional(t, newModel(p2, fb, true))
		if _, err := r.LoadForApply(ctx, tCompany, "NOPE"); !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
		var n int
		_ = p2.QueryRow(ctx, `SELECT count(*) FROM hiring.app_jobs`).Scan(&n)
		if n != 0 {
			t.Fatalf("rows = %d", n)
		}
	})

	t.Run("on without a fallback behaves as off", func(t *testing.T) {
		r := regional(t, newModel(newDB(t), nil, true))
		if _, err := r.LoadForApply(ctx, tCompany, "NOPE"); !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestModel_OtherMethods(t *testing.T) {
	pool := newDB(t)
	p := NewProjector(pools(pool))
	ctx := context.Background()
	fb := &fakeFallback{email: "owner@acme.test", access: &appjobs.JobAccess{JobID: tJob, MemberRole: appjobs.MemberRecruiter}}
	m := newModel(pool, fb, false)
	r := regional(t, m)

	mustHandle(t, p, ev(t, TypePublished, "o1", snap(1, "published", "open")))

	sum, err := r.JobSummary(ctx, tCompany, tJob)
	if err != nil || *sum != (appjobs.JobSummary{Code: "JB-7", Title: "Engineer", Company: "Acme Ltd", PublicID: "PUB1", RetentionMonths: 6}) {
		t.Fatalf("summary: %+v %v", sum, err)
	}
	if _, err := r.JobSummary(ctx, tCompany, "44444444-4444-4444-8444-444444444444"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("unknown job summary: %v", err)
	}

	labels, err := r.JobLabels(ctx, tCompany, []string{tJob, "44444444-4444-4444-8444-444444444444"})
	if err != nil || !reflect.DeepEqual(labels, map[string]appjobs.JobLabel{tJob: {Code: "JB-7", Title: "Engineer"}}) {
		t.Fatalf("labels: %v %v", labels, err)
	}

	qs, err := r.FrozenQuestions(ctx, tCompany, tForm, 1)
	if err != nil || len(qs) != 1 || qs[0].ID != "q1" {
		t.Fatalf("frozen: %v %v", qs, err)
	}
	if _, err := r.FrozenQuestions(ctx, tCompany, tForm, 2); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("unknown version: %v", err)
	}
	if _, err := r.FrozenQuestions(ctx, "99999999-9999-4999-8999-999999999999", tForm, 1); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("other company's form: %v", err)
	}

	// Closed job: until = closed_at + retention months. Open job is not listed.
	if got, err := r.ClosedJobs(ctx, tCompany, time.Now()); err != nil || len(got) != 0 {
		t.Fatalf("open job listed as closed: %v %v", got, err)
	}
	cl := snap(2, "closed", "closed")
	cl["updated_at"] = "2026-03-10T10:00:00Z"
	mustHandle(t, p, ev(t, TypeClosed, "o2", cl))
	got, err := r.ClosedJobs(ctx, tCompany, time.Now())
	want := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	if err != nil || len(got) != 1 || got[0].JobID != tJob || !got[0].Until.Equal(want) {
		t.Fatalf("closed jobs: %v %v want until %v", got, err, want)
	}
	// No closed_at: the day after the closing date; a job with neither uses now.
	exp := snap(3, "expired", "closed")
	exp["updated_at"] = "2026-03-10T10:00:00Z"
	mustHandle(t, p, ev(t, TypeClosed, "o3", exp))
	if _, err := pool.Exec(ctx, `UPDATE hiring.app_jobs SET closed_at = NULL, closing_date = '2026-01-31'`); err != nil {
		t.Fatal(err)
	}
	got, _ = r.ClosedJobs(ctx, tCompany, time.Now())
	if want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC); len(got) != 1 || !got[0].Until.Equal(want) {
		t.Fatalf("closing date clock: %v want %v", got, want)
	}
	nowT := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	_, _ = pool.Exec(ctx, `UPDATE hiring.app_jobs SET closing_date = NULL`)
	got, _ = r.ClosedJobs(ctx, tCompany, nowT)
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); len(got) != 1 || !got[0].Until.Equal(want) {
		t.Fatalf("now clock: %v want %v", got, want)
	}

	// Privacy contact present: local. Absent: delegated (the owner's email lives with identity/org).
	to, company, err := r.NoticeRecipient(ctx, tCompany)
	if err != nil || to != "privacy@acme.test" || company != "Acme Ltd" || fb.count("NoticeRecipient") != 0 {
		t.Fatalf("notice recipient: %q %q %v calls=%v", to, company, err, fb.calls)
	}
	noPriv := snap(4, "closed", "closed")
	noPriv["company"] = map[string]any{"name": "Acme Ltd"}
	mustHandle(t, p, ev(t, TypeClosed, "o4", noPriv))
	to, company, err = r.NoticeRecipient(ctx, tCompany)
	if err != nil || to != "owner@acme.test" || company != "Owner Co" || fb.count("NoticeRecipient") != 1 {
		t.Fatalf("delegated notice recipient: %q %q %v", to, company, err)
	}

	// Delegated for good: JobAccess, TenantIDs, LookupJob.
	acc, err := r.JobAccess(ctx, tCompany, tJob, "u")
	if err != nil || acc.MemberRole != appjobs.MemberRecruiter {
		t.Fatalf("access: %+v %v", acc, err)
	}
	if ids, err := r.TenantIDs(ctx); err != nil || len(ids) != 1 {
		t.Fatalf("tenants: %v %v", ids, err)
	}
	if ref, err := m.LookupJob(ctx, "PUB1"); err != nil || ref.Region != "uk" {
		t.Fatalf("lookup: %+v %v", ref, err)
	}
	for _, name := range []string{"JobAccess", "TenantIDs", "LookupJob"} {
		if fb.count(name) != 1 {
			t.Errorf("%s not delegated exactly once: %v", name, fb.calls)
		}
	}
}

func TestModel_ReadThroughOnListings(t *testing.T) {
	pool := newDB(t)
	ctx := context.Background()
	p := NewProjector(pools(pool))
	cl := snap(1, "closed", "closed")
	mustHandle(t, p, ev(t, TypeClosed, "l1", cl))
	other := "55555555-5555-4555-8555-555555555555"
	fb := &fakeFallback{
		closed: []appjobs.ClosedJob{{JobID: tJob, Until: time.Unix(1, 0)}, {JobID: other, Until: time.Unix(2, 0)}},
		labels: map[string]appjobs.JobLabel{other: {Code: "JB-1", Title: "Old"}},
	}
	r := regional(t, newModel(pool, fb, true))
	got, _ := r.ClosedJobs(ctx, tCompany, time.Now())
	ids := []string{}
	for _, c := range got {
		ids = append(ids, c.JobID)
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{tJob, other}) {
		t.Fatalf("merged closed jobs: %v", ids)
	}
	for _, c := range got {
		if c.JobID == tJob && c.Until.Unix() == 1 {
			t.Fatal("the model's own clock must win over the fallback's")
		}
	}
	labels, _ := r.JobLabels(ctx, tCompany, []string{tJob, other})
	if len(labels) != 2 || labels[other].Title != "Old" {
		t.Fatalf("labels: %v", labels)
	}
}

func TestModel_NoFallback(t *testing.T) {
	pool := newDB(t)
	m := newModel(pool, nil, true)
	r := regional(t, m)
	ctx := context.Background()
	if _, err := r.JobAccess(ctx, tCompany, tJob, "u"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("JobAccess: %v", err)
	}
	if _, err := r.TenantIDs(ctx); !errors.Is(err, ErrNoFallback) {
		t.Fatalf("TenantIDs: %v", err)
	}
	if _, err := m.LookupJob(ctx, "x"); !errors.Is(err, ErrNoFallback) {
		t.Fatalf("LookupJob: %v", err)
	}
	if _, _, err := r.NoticeRecipient(ctx, tCompany); !errors.Is(err, ErrNoFallback) {
		t.Fatalf("NoticeRecipient: %v", err)
	}
}

func TestModel_UnknownRegion(t *testing.T) {
	m := &Model{Pool: func(string) (DB, error) { return nil, ErrUnknownRegion }}
	if _, err := m.Regional("mars"); !errors.Is(err, ErrUnknownRegion) {
		t.Fatalf("got %v", err)
	}
}
