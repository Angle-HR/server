package appstore_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/appstore"
)

func TestInterviewsIntegration(t *testing.T) {
	adminURL, appURL := os.Getenv("TEST_REGIONAL_ADMIN_DB"), os.Getenv("TEST_REGIONAL_DB")
	if adminURL == "" || appURL == "" {
		t.Skip("TEST_REGIONAL_ADMIN_DB and TEST_REGIONAL_DB are not set")
	}
	ctx := context.Background()
	open := func(url string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	admin, app := open(adminURL), open(appURL)
	mk := func(email string) string {
		var id string
		if err := admin.QueryRow(ctx, `INSERT INTO accounts.users (email, password_hash) VALUES ($1, 'x') RETURNING id::text`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	o1, o2, member := mk("iv-1@example.test"), mk("iv-2@example.test"), mk("iv-3@example.test")
	var co1, co2 string
	for _, c := range []struct {
		owner string
		dst   *string
	}{{o1, &co1}, {o2, &co2}} {
		if err := admin.QueryRow(ctx, `INSERT INTO accounts.organizations (owner_user_id, legal_name, company_role_id)
			VALUES ($1::uuid, 'IV Ltd', gen_random_uuid()) RETURNING id::text`, c.owner).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, co := range []string{co1, co2} {
			for _, q := range []string{"app_events", "app_interviews", "app_messages", "applications", "app_job_stages", "job_applicant_counts", "job_postings"} {
				_, _ = admin.Exec(ctx, `DELETE FROM hiring.`+q+` WHERE tenant_id = $1::uuid`, co)
			}
			_, _ = admin.Exec(ctx, `DELETE FROM accounts.organizations WHERE id = $1::uuid`, co)
		}
		_, _ = admin.Exec(ctx, `DELETE FROM accounts.users WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, o1, o2, member)
	})
	var job string
	if err := admin.QueryRow(ctx, `INSERT INTO hiring.job_postings (tenant_id, created_by, job_number, public_id, status, title)
		VALUES ($1::uuid, $2::uuid, 1, 'iv-1', 'published', 'Engineer') RETURNING id::text`, co1, o1).Scan(&job); err != nil {
		t.Fatal(err)
	}
	newApp := func(name string) string {
		var id string
		if err := admin.QueryRow(ctx, `INSERT INTO hiring.applications
			(tenant_id, job_id, form_id, form_version, candidate_name, candidate_email, candidate_country, candidate_market, status,
			 notice_version, notice_acknowledged_at, retention_until, lawful_basis, collection_purpose)
			VALUES ($1::uuid, $2::uuid, gen_random_uuid(), 1, $3, $3 || '@x.test', 'GB', 'UK', 'in_review', 'v1', now(), now() + interval '1 year', 'consent', 'hiring')
			RETURNING id::text`, co1, job, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ada, bob := newApp("ada"), newApp("bob")
	st := &appstore.Store{DB: app, Region: "uk"}
	now := time.Now().UTC().Truncate(time.Second)
	newIV := func(appID string, at time.Time, who ...string) *applications.Interview {
		return &applications.Interview{ID: uuid.NewString(), JobID: job, ApplicationID: appID, StartsAt: at, DurationMin: 45,
			Timezone: "Europe/London", Kind: "video", Location: "https://meet.example/x", Status: "scheduled", InterviewerIDs: who}
	}
	invite := &applications.NewMessage{JobID: job, AppID: ada, SenderID: o1, Subject: "Interview", Body: "Hi", ReplyTo: "jobs@acme.test", Company: "Acme",
		ICS: "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nEND:VCALENDAR\r\n", Status: "queued", ScheduledAt: now}

	iv1 := newIV(ada, now.Add(24*time.Hour), member, o1)
	if err := st.CreateInterview(ctx, co1, o1, iv1, invite); err != nil {
		t.Fatal(err)
	}
	iv2 := newIV(bob, now.Add(48*time.Hour), member)
	if err := st.CreateInterview(ctx, co1, o1, iv2, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInterview(ctx, co2, o2, newIV(ada, now.Add(time.Hour)), nil); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("another company schedules on the application: %v", err)
	}

	// the invite is queued with its calendar event, in the same step
	due, err := st.LeaseMessages(ctx, co1, now.Add(time.Minute), 10, time.Minute)
	if err != nil || len(due) != 1 || due[0].ICS != invite.ICS {
		t.Fatalf("invite message: %+v %v", due, err)
	}

	got, err := st.Interview(ctx, co1, job, ada, iv1.ID)
	if err != nil || got.DurationMin != 45 || got.Timezone != "Europe/London" || len(got.InterviewerIDs) != 2 || !got.EndsAt.Equal(got.StartsAt.Add(45*time.Minute)) {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err = st.Interview(ctx, co2, job, ada, iv1.ID); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("another company reads it: %v", err)
	}
	if _, err = st.Interview(ctx, co1, job, bob, iv1.ID); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("on another application: %v", err)
	}

	// edits are guarded by the sequence
	got.Location, got.Sequence = "https://meet.example/new", got.Sequence+1
	if err = st.SaveInterview(ctx, co1, o1, got, 0, applications.EventInterviewRescheduled, nil); err != nil {
		t.Fatal(err)
	}
	stale := *got
	stale.Sequence = 1
	if err = st.SaveInterview(ctx, co1, o1, &stale, 0, applications.EventInterviewRescheduled, nil); !errors.Is(err, applications.ErrStale) {
		t.Fatalf("a second edit from the same version: %v", err)
	}

	// a person's schedule
	mine, err := st.MyInterviews(ctx, co1, member, now, now.Add(72*time.Hour), 10)
	if err != nil || len(mine) != 2 || mine[0].ID != iv1.ID || mine[0].CandidateName != "ada" {
		t.Fatalf("schedule: %+v %v", mine, err)
	}
	if mine, _ = st.MyInterviews(ctx, co1, o1, now, now.Add(72*time.Hour), 10); len(mine) != 1 {
		t.Fatalf("schedule of the other interviewer: %+v", mine)
	}
	if mine, _ = st.MyInterviews(ctx, co1, member, now, now.Add(30*time.Hour), 10); len(mine) != 1 {
		t.Fatalf("range: %+v", mine)
	}
	if mine, _ = st.MyInterviews(ctx, co2, member, now, now.Add(72*time.Hour), 10); len(mine) != 0 {
		t.Fatalf("another company's schedule: %+v", mine)
	}
	// a decided application drops out of the schedule; a cancelled or held interview does too
	if _, err = admin.Exec(ctx, `UPDATE hiring.applications SET status = 'rejected', decided_at = now() WHERE id = $1::uuid`, bob); err != nil {
		t.Fatal(err)
	}
	if mine, _ = st.MyInterviews(ctx, co1, member, now, now.Add(72*time.Hour), 10); len(mine) != 1 {
		t.Fatalf("decided application: %+v", mine)
	}
	got.Status, got.Sequence = "cancelled", got.Sequence+1
	if err = st.SaveInterview(ctx, co1, o1, got, 1, applications.EventInterviewCancelled, nil); err != nil {
		t.Fatal(err)
	}
	if mine, _ = st.MyInterviews(ctx, co1, member, now, now.Add(72*time.Hour), 10); len(mine) != 0 {
		t.Fatalf("cancelled: %+v", mine)
	}

	// the limit of scheduled interviews per application
	for i := 0; i < applications.MaxActiveInterviews; i++ {
		if err = st.CreateInterview(ctx, co1, o1, newIV(ada, now.Add(time.Duration(i+1)*time.Hour)), nil); err != nil {
			t.Fatalf("interview %d: %v", i, err)
		}
	}
	if err = st.CreateInterview(ctx, co1, o1, newIV(ada, now.Add(99*time.Hour)), nil); !errors.Is(err, applications.ErrTooManyInterviews) {
		t.Fatalf("limit: %v", err)
	}

	// the timeline records them without personal data
	ev, _ := st.Activity(ctx, co1, job, ada, 0, 100)
	types := map[string]bool{}
	for _, e := range ev {
		types[e.Type] = true
	}
	for _, want := range []string{"interview_scheduled", "interview_updated", "interview_cancelled", "message_queued"} {
		if !types[want] {
			t.Errorf("timeline lacks %s: %v", want, types)
		}
	}
	list, _ := st.Interviews(ctx, co1, job, ada)
	if len(list) != applications.MaxActiveInterviews+1 {
		t.Errorf("interviews of ada: %d", len(list))
	}

	// deleting the application takes its interviews
	if _, err = admin.Exec(ctx, `DELETE FROM hiring.applications WHERE id = $1::uuid`, ada); err != nil {
		t.Fatal(err)
	}
	var left int
	if err = admin.QueryRow(ctx, `SELECT count(*)::int FROM hiring.app_interviews WHERE application_id = $1::uuid`, ada).Scan(&left); err != nil || left != 0 {
		t.Errorf("interviews left: %d %v", left, err)
	}
}
