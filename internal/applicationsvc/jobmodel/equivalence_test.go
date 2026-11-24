package jobmodel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/jobsource"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/events"
)

// seedJob describes one hiring job to seed with plain SQL into the monolith tables.
type seedJob struct {
	number       int
	publicID     string
	status       string
	closingDate  string // "" for none
	closedAt     string // "" for none
	markets      []string
	withRules    bool
	withDecls    bool
	formVersions int // frozen versions, the newest is the form's current one
}

const (
	seedRole = "60000000-0000-4000-8000-000000000001"
	orgA     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" // has a privacy contact
	orgB     = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" // none: notices go to the owner
	userA    = "a0000000-0000-4000-8000-00000000000a"
	userB    = "b0000000-0000-4000-8000-00000000000b"
)

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}

func seedOrg(t *testing.T, pool *pgxpool.Pool, org, user, name, email, privacy string) {
	mustExec(t, pool, `INSERT INTO accounts.users (id, email, password_hash) VALUES ($1, $2, 'x')`, user, email)
	mustExec(t, pool, `INSERT INTO accounts.organizations (id, owner_user_id, legal_name, company_role_id, privacy_contact_email, dpo_contact)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($5, ''))`, org, user, name, seedRole, privacy)
}

func seedHiringJob(t *testing.T, pool *pgxpool.Pool, org, user string, j seedJob) (jobID string) {
	t.Helper()
	ctx := context.Background()
	// Seed as published so the backfill covers it; the closed ones are turned into events by hand afterwards.
	err := pool.QueryRow(ctx, `INSERT INTO hiring.job_postings (tenant_id, created_by, job_number, public_id, status, revision, title,
		closing_date, lawful_basis, retention_months, published_at, closed_at)
		VALUES ($1, $2, $3, $4, $5, 3, $6, NULLIF($7,'')::date, 'legitimate_interests', 12, now(), NULLIF($8,'')::timestamptz)
		RETURNING id::text`, org, user, j.number, j.publicID, j.status, "Role "+j.publicID, j.closingDate, j.closedAt).Scan(&jobID)
	if err != nil {
		t.Fatal(err)
	}
	var formID string
	if err := pool.QueryRow(ctx, `INSERT INTO hiring.application_forms (job_id, tenant_id) VALUES ($1, $2) RETURNING id::text`, jobID, org).Scan(&formID); err != nil {
		t.Fatal(err)
	}
	for v := 1; v <= j.formVersions; v++ {
		schema := map[string]any{"form_revision": v, "questions": []map[string]any{
			{"id": "q1", "section": "about", "position": 1, "type": "text", "label": "Name", "required": true, "config": map[string]any{}, "locked": true, "system_key": "full_name"},
			{"id": "q2", "section": "about", "position": 2, "type": "text", "label": "Why v" + string(rune('0'+v)), "required": false, "config": map[string]any{"max": 5}, "locked": false},
		}}
		b, _ := json.Marshal(schema)
		mustExec(t, pool, `INSERT INTO hiring.form_versions (form_id, version, schema, tenant_id) VALUES ($1, $2, $3::jsonb, $4)`, formID, v, string(b), org)
	}
	for _, m := range j.markets {
		mustExec(t, pool, `INSERT INTO hiring.job_markets (job_id, market_code, tenant_id) VALUES ($1, $2, $3)`, jobID, m, org)
	}
	if j.withRules {
		mustExec(t, pool, `INSERT INTO hiring.disqualification_rules (job_id, field, operator, value, reason, tenant_id, position)
			VALUES ($1, 'q1', 'eq', '"x"', 'no', $2, 1), ($1, 'q2', 'neq', '{"a":[1,2]}', 'other', $2, 2)`, jobID, org)
	}
	if j.withDecls {
		mustExec(t, pool, `INSERT INTO hiring.special_category_declarations (job_id, category, legal_condition, purpose, declared_by, tenant_id, retention_until, lawful_basis, collection_purpose)
			VALUES ($1, 'health_disability', 'cond', 'purp', $2, $3, now() + interval '1 year', 'consent', 'x'),
			       ($1, 'race_ethnicity', 'cond2', 'purp2', $2, $3, now() + interval '1 year', 'consent', 'x')`, jobID, user, org)
	}
	mustExec(t, pool, `INSERT INTO hiring.job_members (job_id, user_id, role, tenant_id, retention_until, lawful_basis, collection_purpose)
		VALUES ($1, $2, 'recruiter', $3, now() + interval '1 year', 'legitimate_interests', 'x')`, jobID, user, org)
	return jobID
}

// outboxEvents reads the events the real producer path wrote (hiringstore.EnqueueLiveJobEvents).
func outboxEvents(t *testing.T, pool *pgxpool.Pool) []events.Event {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT envelope FROM events_outbox ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		e, err := events.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func norm(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestModelEqualsJobsource seeds hiring tables, produces the events with hiring's own backfill code, projects
// them, then asks the Model and the real jobsource adapter the same questions on the same database.
func TestModelEqualsJobsource(t *testing.T) {
	pool := newDB(t)
	ctx := context.Background()
	seedOrg(t, pool, orgA, userA, "Acme Ltd", "owner-a@example.com", "privacy@acme.test")
	seedOrg(t, pool, orgB, userB, "Beta Ltd", "owner-b@example.com", "")

	jobs := []struct {
		org, user string
		j         seedJob
	}{
		{orgA, userA, seedJob{number: 1, publicID: "A-OPEN", status: "published", closingDate: "2099-12-31", markets: []string{"GB", "NG", "GB2"}, withRules: true, withDecls: true, formVersions: 3}},
		{orgA, userA, seedJob{number: 2, publicID: "A-PAUSED", status: "paused", markets: []string{"KE"}, formVersions: 1}},
		{orgA, userA, seedJob{number: 3, publicID: "A-CLOSED", status: "published", formVersions: 2, withRules: true}},
		{orgA, userA, seedJob{number: 4, publicID: "A-EXPIRED", status: "published", closingDate: "2026-01-31", formVersions: 1}},
		{orgA, userA, seedJob{number: 5, publicID: "A-PASTDUE", status: "published", closingDate: "2020-01-01", formVersions: 1}},
		{orgB, userB, seedJob{number: 1, publicID: "B-OPEN", status: "published", markets: []string{"US"}, formVersions: 1, withDecls: true}},
	}
	ids := map[string]string{}
	for _, j := range jobs {
		ids[j.j.publicID] = seedHiringJob(t, pool, j.org, j.user, j.j)
	}
	// Seeded closed rows: turn them into closed/expired in hiring, and into closed events below.
	closedAt := "2026-03-10T10:00:00Z"
	mustExec(t, pool, `UPDATE hiring.job_postings SET status = 'closed', closed_at = $1, revision = 4, updated_at = $1 WHERE public_id = 'A-CLOSED'`, closedAt)
	mustExec(t, pool, `UPDATE hiring.job_postings SET status = 'expired', closed_at = $1, revision = 4, updated_at = $1 WHERE public_id = 'A-EXPIRED'`, closedAt)

	// The producer path: hiring's backfill writes snapshots of every live job into the regional outbox. Closed jobs
	// are not live, so their closed events are built from the published ones the way the close would.
	for _, org := range []string{orgA, orgB} {
		mustExec(t, pool, `UPDATE hiring.job_postings SET status = 'published', revision = 3 WHERE tenant_id = $1 AND public_id IN ('A-CLOSED','A-EXPIRED')`, org)
		st := &hiringstore.Store{DB: pool, Region: "uk"}
		if _, err := st.EnqueueLiveJobEvents(ctx, org); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, pool, `UPDATE hiring.job_postings SET status = 'closed', closed_at = $1, revision = 4, updated_at = $1 WHERE public_id = 'A-CLOSED'`, closedAt)
	mustExec(t, pool, `UPDATE hiring.job_postings SET status = 'expired', closed_at = $1, revision = 4, updated_at = $1 WHERE public_id = 'A-EXPIRED'`, closedAt)

	proj := NewProjector(pools(pool))
	for _, e := range outboxEvents(t, pool) {
		var p map[string]any
		if err := e.DecodeJSON(&p); err != nil {
			t.Fatal(err)
		}
		if p["public_id"] == "A-CLOSED" || p["public_id"] == "A-EXPIRED" {
			p["status"], p["public_state"], p["version"], p["updated_at"] = "closed", "closed", int64(4), closedAt
			if p["public_id"] == "A-EXPIRED" {
				p["status"] = "expired"
			}
			e = ev(t, TypeClosed, e.ID+"-closed", p)
		}
		if err := proj.Handle(ctx, e); err != nil {
			t.Fatalf("project %s: %v", e.Type, err)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM hiring.app_jobs`).Scan(&n)
	if n != len(jobs) {
		t.Fatalf("model holds %d jobs, want %d", n, len(jobs))
	}

	hs := &jobsource.Source{Global: nil, Pool: func(string) (hiringstore.DB, error) { return pool, nil }}
	m := &Model{Pool: func(string) (DB, error) { return pool, nil }, Fallback: hs}
	want, err := hs.Regional("uk")
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Regional("uk")
	if err != nil {
		t.Fatal(err)
	}

	same := func(what string, a, b any, ea, eb error) {
		t.Helper()
		if errors.Is(ea, apperror.ErrNotFound) != errors.Is(eb, apperror.ErrNotFound) || (ea == nil) != (eb == nil) {
			t.Errorf("%s: errors differ: jobsource=%v model=%v", what, ea, eb)
			return
		}
		if norm(a) != norm(b) {
			t.Errorf("%s differs:\n jobsource: %s\n model:     %s", what, norm(a), norm(b))
		}
	}

	rt := &Model{Pool: m.Pool, Fallback: hs, ReadThrough: true}
	throughRegional, err := rt.Regional("uk")
	if err != nil {
		t.Fatal(err)
	}
	var labelIDs []string
	for pub, id := range ids {
		org := orgA
		if pub[0] == 'B' {
			org = orgB
		}
		wa, ea := want.LoadForApply(ctx, org, pub)
		ga, eb := got.LoadForApply(ctx, org, pub)
		same("LoadForApply "+pub, wa, ga, ea, eb)
		if ea == nil && wa.FormVersion > 0 {
			if !reflect.DeepEqual(wa.Markets, ga.Markets) {
				t.Errorf("%s markets %v vs %v", pub, wa.Markets, ga.Markets)
			}
			// The current version is local and equal. Older versions predate the model's first event: not local
			// (documented limitation), but identical once read through the fallback.
			for v := 1; v <= wa.FormVersion; v++ {
				wq, ea := want.FrozenQuestions(ctx, org, wa.FormID, v)
				gq, eb := got.FrozenQuestions(ctx, org, wa.FormID, v)
				if v == wa.FormVersion {
					same("FrozenQuestions "+pub, wq, gq, ea, eb)
					continue
				}
				if !errors.Is(eb, apperror.ErrNotFound) {
					t.Errorf("%s v%d: older version must not be local, got %v", pub, v, eb)
				}
				rq, er := throughRegional.FrozenQuestions(ctx, org, wa.FormID, v)
				same("FrozenQuestions read-through "+pub, wq, rq, ea, er)
			}
		}
		ws, ea := want.JobSummary(ctx, org, id)
		gs, eb := got.JobSummary(ctx, org, id)
		same("JobSummary "+pub, ws, gs, ea, eb)
		labelIDs = append(labelIDs, id)
	}
	// Another company's id is not visible through either.
	wa, ea := want.LoadForApply(ctx, orgB, "A-OPEN")
	ga, eb := got.LoadForApply(ctx, orgB, "A-OPEN")
	same("LoadForApply across companies", wa, ga, ea, eb)

	for _, org := range []string{orgA, orgB} {
		wl, ea := want.JobLabels(ctx, org, labelIDs)
		gl, eb := got.JobLabels(ctx, org, labelIDs)
		same("JobLabels", wl, gl, ea, eb)

		now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		wc, ea := want.ClosedJobs(ctx, org, now)
		gc, eb := got.ClosedJobs(ctx, org, now)
		sort.Slice(wc, func(i, j int) bool { return wc[i].JobID < wc[j].JobID })
		sort.Slice(gc, func(i, j int) bool { return gc[i].JobID < gc[j].JobID })
		same("ClosedJobs", wc, gc, ea, eb)
		if org == orgA && len(gc) != 2 {
			t.Errorf("expected two closed jobs for company A, got %v", gc)
		}

		wt, wco, ea := want.NoticeRecipient(ctx, org)
		gt, gco, eb := got.NoticeRecipient(ctx, org)
		same("NoticeRecipient", []string{wt, wco}, []string{gt, gco}, ea, eb)
	}
	wacc, ea := want.JobAccess(ctx, orgA, ids["A-OPEN"], userA)
	gacc, eb := got.JobAccess(ctx, orgA, ids["A-OPEN"], userA)
	same("JobAccess", wacc, gacc, ea, eb)
	if wacc == nil || wacc.MemberRole != appjobs.MemberRecruiter {
		t.Errorf("JobAccess: %+v", wacc)
	}
}
