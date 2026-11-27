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

// Attachments on candidate emails against a real database (same env as the other integration tests).
func TestAttachmentsIntegration(t *testing.T) {
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
	var o1, co1 string
	if err := admin.QueryRow(ctx, `INSERT INTO accounts.users (email, password_hash) VALUES ('att-1@example.test', 'x') RETURNING id::text`).Scan(&o1); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO accounts.organizations (owner_user_id, legal_name, company_role_id) VALUES ($1::uuid, 'ATT Ltd', gen_random_uuid()) RETURNING id::text`, o1).Scan(&co1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, q := range []string{"app_message_attachments", "app_events", "app_messages", "applications", "app_job_stages", "job_applicant_counts", "job_postings"} {
			_, _ = admin.Exec(ctx, `DELETE FROM hiring.`+q+` WHERE tenant_id = $1::uuid`, co1)
		}
		_, _ = admin.Exec(ctx, `DELETE FROM accounts.organizations WHERE id = $1::uuid`, co1)
		_, _ = admin.Exec(ctx, `DELETE FROM accounts.users WHERE id = $1::uuid`, o1)
	})
	var job, appID, other string
	if err := admin.QueryRow(ctx, `INSERT INTO hiring.job_postings (tenant_id, created_by, job_number, public_id, status, title)
		VALUES ($1::uuid, $2::uuid, 1, 'att-1', 'published', 'Engineer') RETURNING id::text`, co1, o1).Scan(&job); err != nil {
		t.Fatal(err)
	}
	for _, dst := range []*string{&appID, &other} {
		if err := admin.QueryRow(ctx, `INSERT INTO hiring.applications
			(tenant_id, job_id, form_id, form_version, candidate_name, candidate_email, candidate_country, candidate_market, status,
			 notice_version, notice_acknowledged_at, retention_until, lawful_basis, collection_purpose)
			VALUES ($1::uuid, $2::uuid, gen_random_uuid(), 1, 'ada', gen_random_uuid()::text || '@x.test', 'GB', 'UK', 'in_review', 'v1', now(), now() + interval '1 year', 'consent', 'hiring')
			RETURNING id::text`, co1, job).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	st := &appstore.Store{DB: app, Region: "uk"}
	mk := func(a string) *applications.Attachment {
		id := newUUID(t, admin)
		att := &applications.Attachment{ID: id, Filename: "terms.pdf", ContentType: "application/pdf", Size: 10, SHA256: "h", ObjectKey: applications.AttachmentKey(co1, a, id)}
		if err := st.AddAttachment(ctx, co1, job, a, o1, att); err != nil {
			t.Fatal(err)
		}
		return att
	}
	a1, a2 := mk(appID), mk(other)

	// a file of another application cannot be attached; nothing is written
	msg := &applications.NewMessage{JobID: job, AppID: appID, SenderID: o1, Subject: "S", Body: "B", Status: applications.MsgQueued, ScheduledAt: time.Now().UTC(), AttachmentIDs: []string{a1.ID, a2.ID}}
	if _, err := st.QueueMessage(ctx, co1, msg); !errors.Is(err, applications.ErrUnknownAttachment) {
		t.Fatalf("foreign file = %v", err)
	}
	var n int
	_ = admin.QueryRow(ctx, `SELECT count(*) FROM hiring.app_messages WHERE application_id = $1::uuid`, appID).Scan(&n)
	if n != 0 {
		t.Fatalf("a message was written (%d)", n)
	}
	msg.AttachmentIDs = []string{a1.ID}
	m, err := st.QueueMessage(ctx, co1, msg)
	if err != nil {
		t.Fatal(err)
	}
	// used once only
	if _, err = st.QueueMessage(ctx, co1, msg); !errors.Is(err, applications.ErrUnknownAttachment) {
		t.Fatalf("reusing a file = %v", err)
	}
	if left, _ := st.UnsentAttachments(ctx, co1, job, appID); len(left) != 0 {
		t.Fatalf("unsent = %v", left)
	}
	if _, err = st.DeleteAttachment(ctx, co1, job, appID, a1.ID); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("deleting a sent file = %v", err)
	}
	hist, _ := st.Messages(ctx, co1, job, appID)
	if len(hist) != 1 || len(hist[0].Attachments) != 1 || hist[0].Attachments[0].Filename != "terms.pdf" {
		t.Fatalf("history = %+v", hist)
	}
	due, err := st.LeaseMessages(ctx, co1, time.Now().UTC().Add(time.Minute), 10, time.Minute)
	if err != nil || len(due) != 1 || due[0].ID != m.ID || len(due[0].Attachments) != 1 || due[0].Attachments[0].Key != a1.ObjectKey {
		t.Fatalf("lease = %+v, %v", due, err)
	}
	// the waiting limit
	for i := 0; i < applications.MaxUnsentAttachments-1; i++ {
		mk(other)
	}
	over := &applications.Attachment{ID: newUUID(t, admin), Filename: "x.pdf", ContentType: "application/pdf", Size: 1, SHA256: "h", ObjectKey: "k"}
	if err = st.AddAttachment(ctx, co1, job, other, o1, over); !errors.Is(err, applications.ErrTooManyAttachments) {
		t.Fatalf("limit = %v", err)
	}
	// another company sees nothing
	if _, err = st.UnsentAttachments(ctx, "00000000-0000-4000-8000-000000000000", job, appID); err == nil {
		t.Fatal("another company read the files")
	}
}
