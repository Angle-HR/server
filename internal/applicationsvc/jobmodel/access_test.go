package jobmodel

import (
	"context"
	"errors"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
)

const (
	tCreator = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	tUserA   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	tUserB   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func members(t testing.TB, id string, version int64, team ...[2]string) events.Event {
	t.Helper()
	list := []map[string]string{}
	for _, m := range team {
		list = append(list, map[string]string{"user_id": m[0], "role": m[1]})
	}
	return ev(t, TypeMembersChanged, id, map[string]any{
		"version": version, "job_id": tJob, "company_id": tCompany, "region": "uk", "members": list,
	})
}

// With events on, JobAccess is answered from app_jobs and app_job_members and never touches the Fallback.
func TestModel_JobAccessFromEvents(t *testing.T) {
	pool := newDB(t)
	p := NewProjector(pools(pool))
	ctx := context.Background()
	fb := &fakeFallback{access: &appjobs.JobAccess{JobID: tJob, MemberRole: "from-hiring-tables"}}
	m := newModel(pool, fb, true)
	m.Stats = &Stats{}
	r := regional(t, m)

	s := snap(2, "published", "open")
	s["created_by"] = tCreator
	mustHandle(t, p, ev(t, TypePublished, "a1", s))

	acc, err := r.JobAccess(ctx, tCompany, tJob, tUserA)
	if err != nil || acc.MemberRole != "" || acc.CreatedBy != tCreator || acc.Code != "JB-7" || acc.Title != "Engineer" ||
		acc.Status != "published" || acc.RetentionMonths != 6 {
		t.Fatalf("no team yet: %+v %v", acc, err)
	}

	mustHandle(t, p, members(t, "a2", 3, [2]string{tUserA, "hiring_manager"}))
	acc, _ = r.JobAccess(ctx, tCompany, tJob, tUserA)
	if acc.MemberRole != "hiring_manager" {
		t.Fatalf("member a: %+v", acc)
	}
	if acc, _ = r.JobAccess(ctx, tCompany, tJob, tUserB); acc.MemberRole != "" {
		t.Fatalf("b is not a member yet: %+v", acc)
	}

	// A second member is added: the complete team replaces the old one.
	mustHandle(t, p, members(t, "a3", 4, [2]string{tUserA, "hiring_manager"}, [2]string{tUserB, "recruiter"}))
	if acc, _ = r.JobAccess(ctx, tCompany, tJob, tUserB); acc.MemberRole != "recruiter" {
		t.Fatalf("b after add: %+v", acc)
	}
	// A stale (lower version) team and a replay change nothing.
	mustHandle(t, p, members(t, "a4", 3, [2]string{tUserA, "hiring_manager"}))
	mustHandle(t, p, members(t, "a3", 4))
	if acc, _ = r.JobAccess(ctx, tCompany, tJob, tUserB); acc.MemberRole != "recruiter" {
		t.Fatalf("stale event applied: %+v", acc)
	}
	// Removing everyone.
	mustHandle(t, p, members(t, "a5", 5))
	if acc, _ = r.JobAccess(ctx, tCompany, tJob, tUserB); acc.MemberRole != "" {
		t.Fatalf("b after removal: %+v", acc)
	}
	// Another company sees nothing.
	_, _ = r.JobAccess(ctx, "99999999-9999-4999-8999-999999999999", tJob, tUserA) // not found locally: asked of hiring

	if fb.count("JobAccess") != 1 { // only the last, wrong-company call went to the fallback
		t.Fatalf("fallback calls: %v", fb.calls)
	}
	if got := m.Stats.Snapshot(); got["job_access:no_creator"] != 0 || got["job_access:no_job"] != 1 || got["total"] != 1 {
		t.Fatalf("stats: %v", got)
	}
}

// A row written before events carried the creator is delegated, and counted.
func TestModel_JobAccessWithoutCreatorIsDelegated(t *testing.T) {
	pool := newDB(t)
	p := NewProjector(pools(pool))
	fb := &fakeFallback{access: &appjobs.JobAccess{JobID: tJob, MemberRole: appjobs.MemberRecruiter}}
	m := newModel(pool, fb, false)
	m.Stats = &Stats{}
	mustHandle(t, p, ev(t, TypePublished, "b1", snap(1, "published", "open")))
	acc, err := regional(t, m).JobAccess(context.Background(), tCompany, tJob, tUserA)
	if err != nil || acc.MemberRole != appjobs.MemberRecruiter || fb.count("JobAccess") != 1 {
		t.Fatalf("%+v %v %v", acc, err, fb.calls)
	}
	if m.Stats.Snapshot()["job_access:no_creator"] != 1 {
		t.Fatalf("stats: %v", m.Stats.Snapshot())
	}
	// The next event with a creator at the same version fills it in.
	s := snap(1, "published", "open")
	s["created_by"] = tCreator
	mustHandle(t, p, ev(t, TypePublished, "b2", s))
	if _, err := regional(t, m).JobAccess(context.Background(), tCompany, tJob, tUserA); err != nil || fb.count("JobAccess") != 1 {
		t.Fatalf("still delegated: %v %v", err, fb.calls)
	}
}

func TestProjector_MembersTerminal(t *testing.T) {
	p := NewProjector(func(string) (inbox.DB, error) { t.Fatal("database must not be used"); return nil, nil })
	bad := map[string]events.Event{
		"no version": ev(t, TypeMembersChanged, "m1", map[string]any{"version": 0, "job_id": tJob, "company_id": tCompany, "region": "uk"}),
		"no job":     ev(t, TypeMembersChanged, "m2", map[string]any{"version": 1, "company_id": tCompany, "region": "uk"}),
		"no user": ev(t, TypeMembersChanged, "m3", map[string]any{"version": 1, "job_id": tJob, "company_id": tCompany,
			"region": "uk", "members": []map[string]string{{"role": "x"}}}),
	}
	for name, e := range bad {
		if err := p.Handle(context.Background(), e); err == nil || !events.IsTerminal(err) {
			t.Errorf("%s: want terminal, got %v", name, err)
		}
	}
}

type fakeIndex struct {
	refs map[string]appjobs.Ref
	put  int
}

func (f *fakeIndex) GetJob(_ context.Context, id string) (appjobs.Ref, error) {
	if r, ok := f.refs[id]; ok {
		return r, nil
	}
	return appjobs.Ref{}, apperror.ErrNotFound
}
func (f *fakeIndex) PutJob(_ context.Context, id, reg, co, _ string, _ int64) error {
	f.put++
	f.refs[id] = appjobs.Ref{PublicID: id, Region: reg, OrganizationID: co}
	return nil
}

func TestModel_LookupJobFromIndex(t *testing.T) {
	fb := &fakeFallback{}
	idx := &fakeIndex{refs: map[string]appjobs.Ref{"PUB1": {PublicID: "PUB1", Region: "uk", OrganizationID: tCompany}}}
	m := &Model{Fallback: fb, Index: idx, Stats: &Stats{}}
	ctx := context.Background()
	if ref, err := m.LookupJob(ctx, "PUB1"); err != nil || ref.Region != "uk" || len(fb.calls) != 0 {
		t.Fatalf("indexed: %+v %v %v", ref, err, fb.calls)
	}
	// Unknown to the index: asked once, remembered.
	if _, err := m.LookupJob(ctx, "PUB2"); err != nil || fb.count("LookupJob") != 1 || idx.put != 1 {
		t.Fatalf("unindexed: %v %v", fb.calls, idx.put)
	}
	if _, err := m.LookupJob(ctx, "PUB2"); err != nil || fb.count("LookupJob") != 1 {
		t.Fatalf("second lookup must be local: %v", fb.calls)
	}
	if m.Stats.Snapshot()["lookup_job:unindexed"] != 1 {
		t.Fatalf("stats %v", m.Stats.Snapshot())
	}
	// Fallback off: unknown id is plain not found.
	m2 := &Model{Index: &fakeIndex{refs: map[string]appjobs.Ref{}}}
	if _, err := m2.LookupJob(ctx, "x"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestModel_TenantsAndNoticeFromOrg(t *testing.T) {
	pool := newDB(t)
	fb := &fakeFallback{email: "owner@acme.test"}
	m := newModel(pool, fb, false)
	m.Stats = &Stats{}
	m.Tenants = func(_ context.Context, reg string) ([]string, error) { return []string{"co-" + reg}, nil }
	var failing bool
	m.Notice = func(context.Context, string, string) (string, string, error) {
		if failing {
			return "", "", errors.New("org down")
		}
		return "contact@acme.test", "Acme", nil
	}
	r := regional(t, m)
	ctx := context.Background()
	if ids, err := r.TenantIDs(ctx); err != nil || len(ids) != 1 || ids[0] != "co-uk" {
		t.Fatalf("tenants: %v %v", ids, err)
	}
	if to, co, err := r.NoticeRecipient(ctx, tCompany); err != nil || to != "contact@acme.test" || co != "Acme" {
		t.Fatalf("notice: %q %q %v", to, co, err)
	}
	if len(fb.calls) != 0 || m.Stats.Snapshot()["total"] != 0 {
		t.Fatalf("fallback used: %v %v", fb.calls, m.Stats.Snapshot())
	}
	// Org down: the privacy contact of the newest event, then the fallback (counted).
	failing = true
	mustHandle(t, NewProjector(pools(pool)), ev(t, TypePublished, "n1", snap(1, "published", "open")))
	if to, _, _ := r.NoticeRecipient(ctx, tCompany); to != "privacy@acme.test" || len(fb.calls) != 0 {
		t.Fatalf("event contact: %q %v", to, fb.calls)
	}
}

// An anywhere job carries its location mode through the event into the apply target.
func TestModel_AnywhereJob(t *testing.T) {
	pool := newDB(t)
	p := NewProjector(pools(pool))
	r := regional(t, newModel(pool, nil, false))
	s := snap(1, "published", "open")
	s["markets"], s["location_mode"] = []string{}, "anywhere"
	mustHandle(t, p, ev(t, TypePublished, "w1", s))
	got, err := r.LoadForApply(context.Background(), tCompany, "PUB1")
	if err != nil || !got.Anywhere || len(got.Markets) != 0 {
		t.Fatalf("anywhere: %+v %v", got, err)
	}
	s2 := snap(2, "published", "open")
	s2["location_mode"] = "specific_area"
	mustHandle(t, p, ev(t, TypeUpdated, "w2", s2))
	if got, _ = r.LoadForApply(context.Background(), tCompany, "PUB1"); got.Anywhere || len(got.Markets) == 0 {
		t.Fatalf("specific area: %+v", got)
	}
}
