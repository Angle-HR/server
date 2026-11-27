package appstore_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/appstore"
)

// Offers and signing against a real database; set TEST_REGIONAL_ADMIN_DB and TEST_REGIONAL_DB as for the pipeline test.
func TestOffersIntegration(t *testing.T) {
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
	o1, o2 := mk("off-1@example.test"), mk("off-2@example.test")
	var co1, co2 string
	for _, c := range []struct {
		owner string
		dst   *string
	}{{o1, &co1}, {o2, &co2}} {
		if err := admin.QueryRow(ctx, `INSERT INTO accounts.organizations (owner_user_id, legal_name, company_role_id)
			VALUES ($1::uuid, 'OFF Ltd', gen_random_uuid()) RETURNING id::text`, c.owner).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, co := range []string{co1, co2} {
			for _, q := range []string{"app_offer_events", "app_offers", "app_offer_templates", "app_events", "app_messages", "applications", "app_job_stages", "job_applicant_counts", "job_postings"} {
				_, _ = admin.Exec(ctx, `DELETE FROM hiring.`+q+` WHERE tenant_id = $1::uuid`, co)
			}
			_, _ = admin.Exec(ctx, `DELETE FROM accounts.organizations WHERE id = $1::uuid`, co)
		}
		_, _ = admin.Exec(ctx, `DELETE FROM accounts.users WHERE id IN ($1::uuid, $2::uuid)`, o1, o2)
	})
	var job string
	if err := admin.QueryRow(ctx, `INSERT INTO hiring.job_postings (tenant_id, created_by, job_number, public_id, status, title)
		VALUES ($1::uuid, $2::uuid, 1, 'off-1', 'published', 'Engineer') RETURNING id::text`, co1, o1).Scan(&job); err != nil {
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
	exp := now.Add(7 * 24 * time.Hour)

	// templates: unique names, per company
	if _, err := st.CreateOfferTemplate(ctx, co1, o1, "Standard", "Dear {{candidate_name}}"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOfferTemplate(ctx, co1, o1, "standard", "x"); !errors.Is(err, applications.ErrExists) {
		t.Fatalf("duplicate template name = %v", err)
	}
	if l, _ := st.OfferTemplates(ctx, co2); len(l) != 0 {
		t.Fatalf("other company sees templates: %v", l)
	}

	// draft, one live offer per application, edits with optimistic concurrency
	mkOffer := func(appID, body string) *applications.Offer {
		o := &applications.Offer{ID: newUUID(t, admin), JobID: job, ApplicationID: appID, Title: "Offer", Body: body, Compensation: "£50k", StartDate: "2027-01-04", Status: applications.OfferDraft}
		if err := st.CreateOffer(ctx, co1, o1, o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	offer := mkOffer(ada, "Welcome Ada")
	if err := st.CreateOffer(ctx, co1, o1, &applications.Offer{ID: newUUID(t, admin), JobID: job, ApplicationID: ada, Title: "T", Body: "B"}); !errors.Is(err, applications.ErrOfferLive) {
		t.Fatalf("second live offer = %v", err)
	}
	offer.Body, offer.Sequence = "Welcome Ada, v2", 1
	if err := st.SaveDraft(ctx, co1, o1, offer, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveDraft(ctx, co1, o1, offer, 0); !errors.Is(err, applications.ErrStale) {
		t.Fatalf("stale save = %v", err)
	}
	if _, err := st.Offer(ctx, co2, job, ada, offer.ID, now); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("other company reads offer: %v", err)
	}

	// send: token, message with the secret, status
	offer.DocHash = applications.OfferDocHash(offer.Title, offer.Body, offer.Compensation, offer.StartDate)
	offer.ExpiresAt = &exp
	token, hash, _ := applications.NewOfferToken("uk", co1)
	msg := &applications.NewMessage{JobID: job, AppID: ada, SenderID: o1, Subject: "Offer", Body: "link {{offer_link}}", Secret: token, Status: applications.MsgQueued, ScheduledAt: now}
	if err := st.SendOffer(ctx, co1, o1, offer, hash, msg, now); err != nil {
		t.Fatal(err)
	}
	var secret string
	if err := admin.QueryRow(ctx, `SELECT secret FROM hiring.app_messages WHERE tenant_id = $1::uuid AND application_id = $2::uuid`, co1, ada).Scan(&secret); err != nil || secret != token {
		t.Fatalf("message secret = %q, %v", secret, err)
	}
	if err := st.SaveDraft(ctx, co1, o1, offer, 1); !errors.Is(err, applications.ErrOfferNotDraft) {
		t.Fatalf("edit after send = %v", err)
	}

	// a wrong token finds nothing; the stored hash is not the secret
	if _, err := st.OfferByToken(ctx, co1, "nope"); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("wrong token = %v", err)
	}
	if _, err := st.OfferByToken(ctx, co2, hash); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("token of another company = %v", err)
	}
	var stored string
	_ = admin.QueryRow(ctx, `SELECT token_hash FROM hiring.app_offers WHERE id = $1::uuid`, offer.ID).Scan(&stored)
	if stored != hash || strings.Contains(token, stored) {
		t.Fatalf("token hash = %q", stored)
	}

	// view
	pub, err := st.ViewOffer(ctx, co1, hash, applications.Meta{IP: "203.0.113.9", UserAgent: "UA"}, now)
	if err != nil || !pub.CanRespond || pub.Status != applications.OfferViewed || pub.CandidateName != "ada" {
		t.Fatalf("view = %+v, %v", pub, err)
	}

	// sign: hires, closes the link, starts retention, writes evidence and a confirmation
	rec := &applications.SignRecord{TokenHash: hash, Name: "Ada Lovelace", Now: now.Add(time.Minute), Meta: applications.Meta{IP: "203.0.113.9", UserAgent: "UA"},
		ConsentHash: applications.ConsentHash(), RetentionMonths: 6,
		Confirmation: &applications.NewMessage{JobID: job, AppID: ada, Subject: "Signed", Body: "thanks", Status: applications.MsgQueued, ScheduledAt: now}}
	signed, err := st.SignOffer(ctx, co1, rec)
	if err != nil || signed.Status != applications.OfferSigned {
		t.Fatalf("sign = %+v, %v", signed, err)
	}
	var appStatus string
	var decided bool
	_ = admin.QueryRow(ctx, `SELECT status, decided_at IS NOT NULL FROM hiring.applications WHERE id = $1::uuid`, ada).Scan(&appStatus, &decided)
	if appStatus != "hired" || !decided {
		t.Fatalf("application after signing = %s decided=%v", appStatus, decided)
	}
	if _, err = st.SignOffer(ctx, co1, rec); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("signing twice = %v", err)
	}
	if _, err = st.ViewOffer(ctx, co1, hash, applications.Meta{}, now); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("link after signing = %v", err)
	}
	var evs []string
	rows, _ := admin.Query(ctx, `SELECT type FROM hiring.app_offer_events WHERE offer_id = $1::uuid ORDER BY id`, offer.ID)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		evs = append(evs, s)
	}
	rows.Close()
	if strings.Join(evs, ",") != "created,edited,sent,viewed,signed" {
		t.Fatalf("audit trail = %v", evs)
	}
	var ip, signedName string
	_ = admin.QueryRow(ctx, `SELECT ip FROM hiring.app_offer_events WHERE offer_id = $1::uuid AND type = 'signed'`, offer.ID).Scan(&ip)
	_ = admin.QueryRow(ctx, `SELECT signed_name FROM hiring.app_offers WHERE id = $1::uuid`, offer.ID).Scan(&signedName)
	if ip != "203.0.113.9" || signedName != "Ada Lovelace" {
		t.Fatalf("evidence ip=%q name=%q", ip, signedName)
	}
	d, err := st.DocumentData(ctx, co1, offer.ID)
	if err != nil || d.SignerIP != "203.0.113.9" || d.CandidateName != "ada" || d.ConsentHash != applications.ConsentHash() {
		t.Fatalf("document data = %+v, %v", d, err)
	}

	// decline, expiry and deciding the application withdraws
	o := mkOffer(bob, "Welcome Bob")
	o.DocHash = applications.OfferDocHash(o.Title, o.Body, o.Compensation, o.StartDate)
	short := now.Add(time.Hour)
	o.ExpiresAt = &short
	tok2, h2, _ := applications.NewOfferToken("uk", co1)
	if err = st.SendOffer(ctx, co1, o1, o, h2, &applications.NewMessage{JobID: job, AppID: bob, Subject: "S", Body: "b {{offer_link}}", Secret: tok2, Status: applications.MsgQueued, ScheduledAt: now}, now); err != nil {
		t.Fatal(err)
	}
	// expired by the clock
	if _, err = st.SignOffer(ctx, co1, &applications.SignRecord{TokenHash: h2, Name: "Bob B", Now: now.Add(2 * time.Hour), ConsentHash: "c"}); !errors.Is(err, applications.ErrOfferClosed) {
		t.Fatalf("signing an expired offer = %v", err)
	}
	var bobStatus string
	_ = admin.QueryRow(ctx, `SELECT status FROM hiring.applications WHERE id = $1::uuid`, bob).Scan(&bobStatus)
	if bobStatus != "in_review" {
		t.Fatalf("expired signing hired the candidate: %s", bobStatus)
	}
	list, _ := st.Offers(ctx, co1, job, bob, now.Add(2*time.Hour))
	if len(list) != 1 || list[0].Status != applications.OfferExpired {
		t.Fatalf("offers = %+v", list)
	}
	if err = st.WithdrawOffer(ctx, co1, o1, job, bob, o.ID, now); err != nil {
		t.Fatal(err)
	}
	// a new offer after expiry; the application is rejected meanwhile: the offer is withdrawn
	o3 := mkOffer(bob, "Second try")
	if err = st.WithdrawOffer(ctx, co1, o1, job, bob, o3.ID, now); err != nil {
		t.Fatal(err)
	}
	if err = st.WithdrawOffer(ctx, co1, o1, job, bob, o3.ID, now); !errors.Is(err, applications.ErrOfferClosed) {
		t.Fatalf("withdraw twice = %v", err)
	}
	o4 := mkOffer(bob, "Third try")
	o4.DocHash = applications.OfferDocHash(o4.Title, o4.Body, o4.Compensation, o4.StartDate)
	o4.ExpiresAt = &exp
	tok4, h4, _ := applications.NewOfferToken("uk", co1)
	if err = st.SendOffer(ctx, co1, o1, o4, h4, &applications.NewMessage{JobID: job, AppID: bob, Subject: "S", Body: "b {{offer_link}}", Secret: tok4, Status: applications.MsgQueued, ScheduledAt: now}, now); err != nil {
		t.Fatal(err)
	}
	if err = st.DeclineOffer(ctx, co1, h4, "found another role", applications.Meta{IP: "198.51.100.1"}, now); err != nil {
		t.Fatal(err)
	}
	if err = st.DeclineOffer(ctx, co1, h4, "", applications.Meta{}, now); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("declining twice = %v", err)
	}
	// application decided while an offer is out
	o5 := mkOffer(bob, "Fourth try")
	if err = st.ApplyChange(ctx, co1, &applications.Change{AppID: bob, JobID: job, ActorID: o1, From: applications.StatusInReview, To: applications.StatusRejected, Now: now}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Offer(ctx, co1, job, bob, o5.ID, now); got == nil || got.Status != applications.OfferWithdrawn {
		t.Fatalf("offer after rejection = %+v", got)
	}
}

func newUUID(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
