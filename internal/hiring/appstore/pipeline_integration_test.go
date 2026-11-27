package appstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/appstore"
)

// The pipeline against a real database. It runs when these are set and is skipped otherwise:
//
//	TEST_REGIONAL_ADMIN_DB  a superuser connection to a migrated regional database (seeds rows)
//	TEST_REGIONAL_DB        the same database as a role without BYPASSRLS, as the services connect
func TestPipelineIntegration(t *testing.T) {
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
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := func(email string) string {
		var id string
		if err := admin.QueryRow(ctx, `INSERT INTO accounts.users (email, password_hash) VALUES ($1, 'x') RETURNING id::text`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner1, owner2, member := user("pit-1@example.test"), user("pit-2@example.test"), user("pit-3@example.test")
	var co1, co2 string
	for _, c := range []struct {
		owner string
		dst   *string
	}{{owner1, &co1}, {owner2, &co2}} {
		if err := admin.QueryRow(ctx, `INSERT INTO accounts.organizations (owner_user_id, legal_name, company_role_id)
			VALUES ($1::uuid, 'PIT Ltd', gen_random_uuid()) RETURNING id::text`, c.owner).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, co := range []string{co1, co2} {
			_, _ = admin.Exec(ctx, `DELETE FROM hiring.app_events WHERE tenant_id = $1::uuid`, co)
			_, _ = admin.Exec(ctx, `DELETE FROM hiring.applications WHERE tenant_id = $1::uuid`, co)
			_, _ = admin.Exec(ctx, `DELETE FROM hiring.app_tags WHERE tenant_id = $1::uuid`, co)
			_, _ = admin.Exec(ctx, `DELETE FROM hiring.app_job_stages WHERE tenant_id = $1::uuid`, co)
			_, _ = admin.Exec(ctx, `DELETE FROM hiring.job_applicant_counts WHERE tenant_id = $1::uuid`, co)
			_, _ = admin.Exec(ctx, `DELETE FROM hiring.job_postings WHERE tenant_id = $1::uuid`, co)
			_, _ = admin.Exec(ctx, `DELETE FROM accounts.organizations WHERE id = $1::uuid`, co)
		}
		_, _ = admin.Exec(ctx, `DELETE FROM accounts.users WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, owner1, owner2, member)
	})
	var job string
	if err := admin.QueryRow(ctx, `INSERT INTO hiring.job_postings (tenant_id, created_by, job_number, public_id, status, title)
		VALUES ($1::uuid, $2::uuid, 1, 'pit-1', 'published', 'T') RETURNING id::text`, co1, owner1).Scan(&job); err != nil {
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
	a1, a2, a3 := newApp("ada", "received"), newApp("bob", "received"), newApp("cy", "flagged")

	st := &appstore.Store{DB: app, Region: "uk"}

	// A submission lands in Applied and starts the timeline.
	submitted, err := st.InsertApplication(ctx, co1, &apply.NewApplication{
		ID: "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f", JobID: job, FormID: "1f1f1f1f-1f1f-4f1f-8f1f-1f1f1f1f1f1f", FormVersion: 1,
		Name: "Dee", Email: "dee@x.test", Country: "GB", Market: "UK", Status: "received", Source: "direct", Answers: map[string]json.RawMessage{},
		NoticeVersion: "v1", NoticeAcknowledgedAt: time.Now(), LawfulBasis: "consent", CollectionPurpose: "hiring",
		RetentionUntil: time.Now().AddDate(1, 0, 0), SubmittedAt: time.Now(),
	})
	if err != nil || !submitted {
		t.Fatalf("submit: %v %v", submitted, err)
	}
	var placed, firstEvent string
	must(0, admin.QueryRow(ctx, `SELECT s.kind FROM hiring.applications a JOIN hiring.app_job_stages s ON s.id = a.stage_id WHERE a.id = '0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f'`).Scan(&placed))
	must(0, admin.QueryRow(ctx, `SELECT type FROM hiring.app_events WHERE application_id = '0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f'`).Scan(&firstEvent))
	if placed != "applied" || firstEvent != "applied" {
		t.Errorf("a new application is in Applied with an applied event: %s %s", placed, firstEvent)
	}
	other := &appstore.Store{DB: app, Region: "uk"}

	// The default pipeline is created on first use, in order.
	stages, err := st.Stages(ctx, co1, job)
	if err != nil || len(stages) != 6 || stages[0].Kind != "applied" || stages[5].Kind != "disqualified" {
		t.Fatalf("stages: %+v %v", stages, err)
	}
	again, _ := st.Stages(ctx, co1, job)
	if len(again) != 6 {
		t.Fatalf("seeding twice must not duplicate: %d", len(again))
	}
	byKind := map[string]applications.Stage{}
	for _, s := range stages {
		byKind[s.Kind] = s
	}
	// Seeded applications are unplaced until someone places them; place them in Applied as submission does.
	must(admin.Exec(ctx, `UPDATE hiring.applications SET stage_id = $1::uuid WHERE job_id = $2::uuid`, byKind["applied"].ID, job))

	// Row level security: another company sees none of it.
	if s, err := other.Stages(ctx, co2, job); err != nil || len(s) != 6 || s[0].ID == byKind["applied"].ID {
		t.Fatalf("another company gets its own stages for a foreign job id, not this one: %v %v", s, err)
	}
	if _, err := other.Notes(ctx, co2, job, a1, owner2); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("another company reads notes: %v", err)
	}

	// Custom stage: goes before Hired, names are unique, order can change, it must be empty to go.
	phone, err := st.CreateStage(ctx, co1, job, owner1, "Phone screen")
	if err != nil || phone.Kind != "custom" {
		t.Fatalf("create stage: %+v %v", phone, err)
	}
	if _, err = st.CreateStage(ctx, co1, job, owner1, "phone SCREEN"); !errors.Is(err, applications.ErrExists) {
		t.Fatalf("duplicate name: %v", err)
	}
	stages, _ = st.Stages(ctx, co1, job)
	if stages[len(stages)-3].ID != phone.ID {
		t.Errorf("custom stage goes right before Hired: %+v", stages)
	}
	if err = st.RenameStage(ctx, co1, job, byKind["applied"].ID, owner1, "New"); !errors.Is(err, applications.ErrStageLocked) {
		t.Fatalf("Applied cannot be renamed: %v", err)
	}
	if err = st.RenameStage(ctx, co1, job, phone.ID, owner1, "Screen call"); err != nil {
		t.Fatal(err)
	}
	order := []string{byKind["applied"].ID, phone.ID, byKind["shortlisted"].ID, byKind["interview"].ID, byKind["offer"].ID}
	if err = st.ReorderStages(ctx, co1, job, owner1, order); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if err = st.ReorderStages(ctx, co1, job, owner1, []string{phone.ID, byKind["applied"].ID, byKind["shortlisted"].ID, byKind["interview"].ID, byKind["offer"].ID}); !errors.Is(err, applications.ErrInvalidOrder) {
		t.Fatalf("Applied must stay first: %v", err)
	}
	if err = st.ReorderStages(ctx, co1, job, owner1, order[:4]); !errors.Is(err, applications.ErrInvalidOrder) {
		t.Fatalf("every open stage must be listed: %v", err)
	}
	stages, _ = st.Stages(ctx, co1, job)
	if stages[1].ID != phone.ID || stages[4].Kind != "offer" || stages[5].Kind != "hired" {
		t.Errorf("order after reorder: %+v", stages)
	}

	// Moving to an open stage: received becomes in_review, a stale move is refused.
	now := time.Now().UTC()
	mv := &applications.OpenMove{AppID: a1, JobID: job, ActorID: owner1, FromStatus: "received", ToStatus: "in_review",
		FromStageID: byKind["applied"].ID, To: *phone, Now: now}
	if err = st.MoveToOpenStage(ctx, co1, mv); err != nil {
		t.Fatalf("move: %v", err)
	}
	if err = st.MoveToOpenStage(ctx, co1, mv); !errors.Is(err, applications.ErrStale) {
		t.Fatalf("the same move again is stale: %v", err)
	}
	if err = st.DeleteStage(ctx, co1, job, phone.ID, owner1); !errors.Is(err, applications.ErrStageInUse) {
		t.Fatalf("a stage with applications cannot go: %v", err)
	}
	if err = st.DeleteStage(ctx, co1, job, byKind["offer"].ID, owner1); !errors.Is(err, applications.ErrStageLocked) {
		t.Fatalf("default stages cannot go: %v", err)
	}
	ms, err := st.MoveState(ctx, co1, job, a1, byKind["hired"].ID)
	if err != nil || ms.Status != "in_review" || ms.StageID != phone.ID || ms.Target.Kind != "hired" {
		t.Fatalf("move state: %+v %v", ms, err)
	}
	if _, err = st.MoveState(ctx, co1, job, a1, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("unknown stage: %v", err)
	}

	// A disqualification sets the stage, the reason and the timeline.
	err = st.ApplyChange(ctx, co1, &applications.Change{AppID: a2, JobID: job, ActorID: owner1, From: "received", To: "rejected",
		Note: "n", Now: now, RetentionMonths: 6, StageID: byKind["disqualified"].ID, Reason: "not_a_fit"})
	if err != nil {
		t.Fatalf("disqualify: %v", err)
	}
	// A hire through the plain status path finds Hired by itself.
	err = st.ApplyChange(ctx, co1, &applications.Change{AppID: a1, JobID: job, ActorID: owner1, From: "in_review", To: "hired", Now: now, RetentionMonths: 6})
	if err != nil {
		t.Fatalf("hire: %v", err)
	}
	var stageKind, reason string
	must(0, admin.QueryRow(ctx, `SELECT s.kind, COALESCE(a.disqualify_reason, '') FROM hiring.applications a JOIN hiring.app_job_stages s ON s.id = a.stage_id WHERE a.id = $1::uuid`, a2).Scan(&stageKind, &reason))
	if stageKind != "disqualified" || reason != "not_a_fit" {
		t.Errorf("disqualified: %s %q", stageKind, reason)
	}
	must(0, admin.QueryRow(ctx, `SELECT s.kind FROM hiring.applications a JOIN hiring.app_job_stages s ON s.id = a.stage_id WHERE a.id = $1::uuid`, a1).Scan(&stageKind))
	if stageKind != "hired" {
		t.Errorf("hired: %s", stageKind)
	}

	// Notes: anyone adds, only the author edits or deletes.
	n, err := st.AddNote(ctx, co1, job, a3, member, "strong on systems")
	if err != nil || !n.Mine {
		t.Fatalf("add note: %+v %v", n, err)
	}
	if _, err = st.EditNote(ctx, co1, job, a3, n.ID, owner1, "x"); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("someone else's note: %v", err)
	}
	if err = st.DeleteNote(ctx, co1, job, a3, n.ID, owner1); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("someone else's delete: %v", err)
	}
	notes, _ := st.Notes(ctx, co1, job, a3, owner1)
	if len(notes) != 1 || notes[0].Mine {
		t.Fatalf("notes as another viewer: %+v", notes)
	}
	if _, err = st.EditNote(ctx, co1, job, a3, n.ID, member, "stronger"); err != nil {
		t.Fatal(err)
	}

	// Tags and follows.
	tag, err := st.CreateTag(ctx, co1, owner1, "Referral", "green")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateTag(ctx, co1, owner1, "referral", "red"); !errors.Is(err, applications.ErrExists) {
		t.Fatalf("tag names are unique ignoring case: %v", err)
	}
	if _, err = st.SetTags(ctx, co1, job, a3, owner1, []string{"00000000-0000-4000-8000-000000000000"}); !errors.Is(err, applications.ErrUnknownTag) {
		t.Fatalf("unknown tag: %v", err)
	}
	if got, err := st.SetTags(ctx, co1, job, a3, owner1, []string{tag.ID}); err != nil || len(got) != 1 {
		t.Fatalf("set tags: %v %v", got, err)
	}
	if err = st.Follow(ctx, co1, job, a3, owner1, true); err != nil {
		t.Fatal(err)
	}
	if err = st.Follow(ctx, co1, job, a3, owner1, true); err != nil {
		t.Fatalf("following twice is harmless: %v", err)
	}

	// The list carries stage, follow, notes and tags, and filters by them.
	items, counts, err := st.List(ctx, co1, job, applications.StoreListQuery{ActorID: owner1, Limit: 10})
	if err != nil || len(items) != 4 {
		t.Fatalf("list: %v %d", err, len(items))
	}
	for _, it := range items {
		if it.ID == a3 && (!it.Followed || it.NoteCount != 1 || len(it.TagIDs) != 1 || it.StageID != byKind["applied"].ID) {
			t.Errorf("row of a3: %+v", it)
		}
		if it.TagIDs == nil {
			t.Errorf("tag ids must be an empty list, not null: %+v", it)
		}
	}
	if counts.Stage[byKind["hired"].ID] != 1 || counts.Stage[byKind["disqualified"].ID] != 1 || counts.Stage[byKind["applied"].ID] != 2 {
		t.Errorf("stage counts: %+v", counts.Stage)
	}
	if counts.Status["hired"] != 1 || counts.Status["rejected"] != 1 || counts.Status["flagged"] != 1 {
		t.Errorf("status counts: %+v", counts.Status)
	}
	for name, q := range map[string]applications.StoreListQuery{
		"stage":    {ActorID: owner1, Limit: 10, StageID: byKind["hired"].ID},
		"tag":      {ActorID: owner1, Limit: 10, TagID: tag.ID},
		"followed": {ActorID: owner1, Limit: 10, FollowedOnly: true},
	} {
		got, _, err := st.List(ctx, co1, job, q)
		if err != nil || len(got) != 1 {
			t.Errorf("filter %s: %d %v", name, len(got), err)
		}
	}
	if got, _, _ := st.List(ctx, co1, job, applications.StoreListQuery{ActorID: member, Limit: 10, FollowedOnly: true}); len(got) != 0 {
		t.Errorf("follows are per person: %d", len(got))
	}

	// The timeline: newest first, paged, with no note text in it.
	ev, err := st.Activity(ctx, co1, job, a1, 0, 50)
	if err != nil || len(ev) < 3 {
		t.Fatalf("activity of a1: %v %v", ev, err)
	}
	if ev[0].Type != "stage_changed" && ev[0].Type != "status_changed" {
		t.Errorf("newest first: %+v", ev[0])
	}
	page, _ := st.Activity(ctx, co1, job, a1, ev[0].ID, 50)
	if len(page) != len(ev)-1 {
		t.Errorf("paging by id: %d vs %d", len(page), len(ev))
	}
	ev3, _ := st.Activity(ctx, co1, job, a3, 0, 50)
	for _, e := range ev3 {
		if string(e.Data) != "" && containsText(string(e.Data), "strong on systems") {
			t.Errorf("a timeline entry carries note text: %s", e.Data)
		}
	}
	types := map[string]bool{}
	for _, e := range ev3 {
		types[e.Type] = true
	}
	for _, want := range []string{"note_added", "note_edited", "tags_changed"} {
		if !types[want] {
			t.Errorf("a3 timeline lacks %s: %v", want, types)
		}
	}

	// Deleting a tag removes it everywhere; deleting the application takes everything that belongs to it.
	must(0, st.DeleteTag(ctx, co1, tag.ID))
	if got, _ := st.SetTags(ctx, co1, job, a3, owner1, nil); len(got) != 0 {
		t.Errorf("tags left: %v", got)
	}
	must(admin.Exec(ctx, `DELETE FROM hiring.applications WHERE id = $1::uuid`, a3))
	var left int
	must(0, admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM hiring.app_notes WHERE application_id = $1::uuid)
		+ (SELECT count(*) FROM hiring.app_events WHERE application_id = $1::uuid)
		+ (SELECT count(*) FROM hiring.app_follows WHERE application_id = $1::uuid)`, a3).Scan(&left))
	if left != 0 {
		t.Errorf("rows left after the application went: %d", left)
	}
}

func containsText(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
