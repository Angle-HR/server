package appstore_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/appstore"
)

// Candidate email against a real database; set TEST_REGIONAL_ADMIN_DB and TEST_REGIONAL_DB as for the pipeline test.
func TestMessagesIntegration(t *testing.T) {
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
	o1, o2 := mk("msg-1@example.test"), mk("msg-2@example.test")
	var co1, co2 string
	for _, c := range []struct {
		owner string
		dst   *string
	}{{o1, &co1}, {o2, &co2}} {
		if err := admin.QueryRow(ctx, `INSERT INTO accounts.organizations (owner_user_id, legal_name, company_role_id)
			VALUES ($1::uuid, 'MSG Ltd', gen_random_uuid()) RETURNING id::text`, c.owner).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, co := range []string{co1, co2} {
			for _, q := range []string{"app_events", "app_messages", "app_email_templates", "app_email_settings", "applications", "app_job_stages", "job_applicant_counts", "job_postings"} {
				_, _ = admin.Exec(ctx, `DELETE FROM hiring.`+q+` WHERE tenant_id = $1::uuid`, co)
			}
			_, _ = admin.Exec(ctx, `DELETE FROM accounts.organizations WHERE id = $1::uuid`, co)
		}
		_, _ = admin.Exec(ctx, `DELETE FROM accounts.users WHERE id IN ($1::uuid, $2::uuid)`, o1, o2)
	})
	var job string
	if err := admin.QueryRow(ctx, `INSERT INTO hiring.job_postings (tenant_id, created_by, job_number, public_id, status, title)
		VALUES ($1::uuid, $2::uuid, 1, 'msg-1', 'published', 'Engineer') RETURNING id::text`, co1, o1).Scan(&job); err != nil {
		t.Fatal(err)
	}
	newApp := func(name, status string) string {
		var id string
		if err := admin.QueryRow(ctx, `INSERT INTO hiring.applications
			(tenant_id, job_id, form_id, form_version, candidate_name, candidate_email, candidate_country, candidate_market, status,
			 notice_version, notice_acknowledged_at, retention_until, lawful_basis, collection_purpose)
			VALUES ($1::uuid, $2::uuid, gen_random_uuid(), 1, $3, $3 || '@x.test', 'GB', 'UK', $4, 'v1', now(), now() + interval '1 year', 'consent', 'hiring')
			RETURNING id::text`, co1, job, name, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ada, bob := newApp("ada", "in_review"), newApp("bob", "in_review")
	st := &appstore.Store{DB: app, Region: "uk"}
	now := time.Now().UTC()

	// templates: unique names ignoring case, per company
	tpl, err := st.CreateTemplate(ctx, co1, o1, "Rejection", "About {{job_title}}", "Dear {{candidate_name}}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateTemplate(ctx, co1, o1, "rejection", "s", "b"); !errors.Is(err, applications.ErrExists) {
		t.Fatalf("duplicate template name: %v", err)
	}
	if _, err = st.CreateTemplate(ctx, co2, o2, "rejection", "s", "b"); err != nil {
		t.Fatalf("another company can use the name: %v", err)
	}
	if other, _ := st.Templates(ctx, co2); len(other) != 1 || other[0].ID == tpl.ID {
		t.Fatalf("company 2 sees %+v", other)
	}
	if _, err = st.Template(ctx, co2, tpl.ID); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("a template of another company: %v", err)
	}
	if _, err = st.UpdateTemplate(ctx, co1, tpl.ID, "Rejection", "S2", "B2"); err != nil {
		t.Fatal(err)
	}

	// reply-to
	if err = st.SetReplyTo(ctx, co1, o1, "jobs@acme.test"); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.ReplyTo(ctx, co1); r != "jobs@acme.test" {
		t.Fatalf("reply-to %q", r)
	}
	if r, _ := st.ReplyTo(ctx, co2); r != "" {
		t.Fatalf("reply-to of another company %q", r)
	}

	// queue: scheduled and immediate; a message for another company's application finds nothing
	nm := func(appID string, status string, at time.Time) *applications.NewMessage {
		return &applications.NewMessage{JobID: job, AppID: appID, SenderID: o1, TemplateID: tpl.ID, Subject: "Hi", Body: "Body",
			ReplyTo: "jobs@acme.test", Company: "Acme", Status: status, ScheduledAt: at, Enforce: true}
	}
	if _, err = st.QueueMessage(ctx, co2, nm(ada, "queued", now)); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("another company writes to the application: %v", err)
	}
	later, err := st.QueueMessage(ctx, co1, nm(ada, "scheduled", now.Add(2*time.Hour)))
	if err != nil || later.Status != "scheduled" {
		t.Fatalf("schedule: %+v %v", later, err)
	}
	soon, err := st.QueueMessage(ctx, co1, nm(ada, "queued", now))
	if err != nil {
		t.Fatal(err)
	}

	// the sender takes only what is due, once
	due, err := st.LeaseMessages(ctx, co1, now.Add(time.Minute), 10, 10*time.Minute)
	if err != nil || len(due) != 1 || due[0].ID != soon.ID {
		t.Fatalf("lease: %+v %v", due, err)
	}
	if d := due[0]; d.Recipient != "ada@x.test" || d.Name != "ada" || d.Company != "Acme" || d.JobTitle != "Engineer" || d.ReplyTo != "jobs@acme.test" || d.Attempts != 1 {
		t.Errorf("due message %+v", d)
	}
	if again, _ := st.LeaseMessages(ctx, co1, now.Add(time.Minute), 10, 10*time.Minute); len(again) != 0 {
		t.Fatalf("a leased message is not leased twice: %+v", again)
	}
	if other, _ := st.LeaseMessages(ctx, co2, now.Add(time.Minute), 10, time.Minute); len(other) != 0 {
		t.Fatalf("another company's sender sees it: %+v", other)
	}
	if err = st.CancelMessage(ctx, co1, job, ada, soon.ID); !errors.Is(err, applications.ErrNotCancellable) {
		t.Fatalf("a message being sent cannot be cancelled: %v", err)
	}
	if err = st.MarkMessageFailed(ctx, co1, soon.ID, "smtp down", false, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = st.MarkMessageSent(ctx, co1, soon.ID, now); err != nil {
		t.Fatal(err)
	}
	if err = st.CancelMessage(ctx, co1, job, ada, soon.ID); !errors.Is(err, applications.ErrNotCancellable) {
		t.Fatalf("a sent message cannot be cancelled: %v", err)
	}
	// the scheduled one is due two hours on, cancellable before, and a cancelled one is never sent
	if due, _ = st.LeaseMessages(ctx, co1, now.Add(3*time.Hour), 10, time.Minute); len(due) != 1 || due[0].ID != later.ID {
		t.Fatalf("scheduled message due later: %+v", due)
	}
	must0 := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must0(admin.QueryRow(ctx, `UPDATE hiring.app_messages SET lease_until = NULL WHERE id = $1::uuid RETURNING 1`, later.ID).Scan(new(int)))
	must0(st.CancelMessage(ctx, co1, job, ada, later.ID))
	if due, _ = st.LeaseMessages(ctx, co1, now.Add(4*time.Hour), 10, time.Minute); len(due) != 0 {
		t.Fatalf("a cancelled message is not sent: %+v", due)
	}
	if err = st.CancelMessage(ctx, co1, job, ada, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("unknown message: %v", err)
	}

	// a withdrawn candidate is not written to
	wd, _ := st.QueueMessage(ctx, co1, nm(bob, "queued", now))
	must0(admin.QueryRow(ctx, `UPDATE hiring.applications SET status = 'withdrawn', decided_at = now() WHERE id = $1::uuid RETURNING 1`, bob).Scan(new(int)))
	if due, _ = st.LeaseMessages(ctx, co1, now.Add(time.Minute), 10, time.Minute); len(due) != 0 {
		t.Fatalf("withdrawn candidate: %+v (%s)", due, wd.ID)
	}
	must0(admin.QueryRow(ctx, `UPDATE hiring.applications SET status = 'in_review', decided_at = NULL WHERE id = $1::uuid RETURNING 1`, bob).Scan(new(int)))

	// history, newest first, with the timeline entries
	hist, err := st.Messages(ctx, co1, job, ada)
	if err != nil || len(hist) != 2 || hist[0].CreatedAt.Before(hist[1].CreatedAt) {
		t.Fatalf("history: %+v %v", hist, err)
	}
	statuses := map[string]bool{}
	for _, m := range hist {
		statuses[m.Status] = true
	}
	if !statuses["sent"] || !statuses["cancelled"] {
		t.Errorf("statuses %v", statuses)
	}
	if _, err = st.Messages(ctx, co2, job, ada); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("history of another company: %v", err)
	}
	ev, _ := st.Activity(ctx, co1, job, ada, 0, 50)
	types := map[string]bool{}
	for _, e := range ev {
		types[e.Type] = true
		if containsText(string(e.Data), "Body") || containsText(string(e.Data), "Hi") && e.Type == "message_queued" && containsText(string(e.Data), "subject") {
			t.Errorf("message text in the timeline: %s", e.Data)
		}
	}
	for _, want := range []string{"message_queued", "message_sent", "message_cancelled"} {
		if !types[want] {
			t.Errorf("timeline lacks %s: %v", want, types)
		}
	}

	// the daily limit per application
	for i := 0; i < applications.MaxPerApplication; i++ {
		if _, err = st.QueueMessage(ctx, co1, nm(bob, "queued", now)); err != nil {
			if errors.Is(err, applications.ErrQuota) {
				break
			}
			t.Fatal(err)
		}
	}
	if _, err = st.QueueMessage(ctx, co1, nm(bob, "queued", now)); !errors.Is(err, applications.ErrQuota) {
		t.Fatalf("the per-application limit: %v", err)
	}
	// but a message that comes with a decision is not held up by it
	err = st.ApplyChange(ctx, co1, &applications.Change{AppID: bob, JobID: job, ActorID: o1, From: "in_review", To: "rejected", Now: now,
		RetentionMonths: 6, Reason: "not_a_fit", Message: &applications.NewMessage{JobID: job, AppID: bob, SenderID: o1,
			Subject: "Update", Body: "Sorry", Company: "Acme", Status: "queued", ScheduledAt: now}})
	if err != nil {
		t.Fatalf("decision with a message: %v", err)
	}

	// deleting the application takes its messages
	must0(func() error {
		_, e := admin.Exec(ctx, `DELETE FROM hiring.applications WHERE id = $1::uuid`, ada)
		return e
	}())
	var left int
	must0(admin.QueryRow(ctx, `SELECT count(*)::int FROM hiring.app_messages WHERE application_id = $1::uuid`, ada).Scan(&left))
	if left != 0 {
		t.Errorf("messages left after the application went: %d", left)
	}
}
