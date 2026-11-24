package companydeletion

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/applicationsvc/jobmodel"
	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
	"github.com/Angle-HR/server/pkg/platform/blob"
	"github.com/Angle-HR/server/pkg/platform/blob/memory"
)

const (
	coA = "11111111-1111-4111-8111-111111111111"
	coB = "22222222-2222-4222-8222-222222222222"
)

func ev(t *testing.T, typ string, p any) events.Event {
	t.Helper()
	e, err := events.NewJSON("/services/org", typ, p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func isTerminal(err error) bool { return err != nil && events.IsTerminal(err) }

func TestMalformedEventsAreTerminal(t *testing.T) {
	h := &Handler{Pools: func(string) (inbox.DB, error) { return nil, jobmodel.ErrUnknownRegion }}
	for name, e := range map[string]events.Event{
		"wrong type": ev(t, "org.company.created.v1", Payload{CompanyID: coA, Region: "uk"}),
		"no company": ev(t, Type, Payload{Region: "uk"}),
		"bad uuid":   ev(t, Type, Payload{CompanyID: "x'; drop", Region: "uk"}),
		"no region":  ev(t, Type, Payload{CompanyID: coA}),
		"unknown":    ev(t, Type, Payload{CompanyID: coA, Region: "mars"}),
	} {
		if err := h.Handle(context.Background(), e); !isTerminal(err) {
			t.Errorf("%s: want terminal error, got %v", name, err)
		}
	}
}

func seed(t *testing.T, db *pgxpool.Pool, co, job, app string) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, co); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO hiring.applications (id, tenant_id, job_id, form_id, form_version, candidate_name, candidate_email, candidate_country, candidate_market, notice_version, notice_acknowledged_at, lawful_basis, collection_purpose, retention_until)
		 VALUES ($3::uuid, $1::uuid, $2::uuid, gen_random_uuid(), 1, 'Ada', 'ada@example.com', 'GB', 'UK', 'v1', now(), 'consent', 'p', now() + interval '1 year')`,
		`INSERT INTO hiring.job_applicant_counts (job_id, tenant_id, applicant_count, new_applicant_count) SELECT $2::uuid, $1::uuid, 1, 1 WHERE $3::uuid IS NOT NULL`,
		`INSERT INTO hiring.application_files (id, tenant_id, job_id, application_id, question_id, original_filename, content_type, declared_size_bytes, size_bytes, sha256, status, lawful_basis, collection_purpose, retention_until)
		 VALUES (gen_random_uuid(), $1::uuid, $2::uuid, $3::uuid, 'cv', 'cv.pdf', 'application/pdf', 10, 10, 'abc', 'claimed', 'consent', 'p', now() + interval '1 year')`,
		`INSERT INTO hiring.app_audit_events (job_id, tenant_id, action) SELECT $2::uuid, $1::uuid, 'application.received' WHERE $3::uuid IS NOT NULL`,
	} {
		if _, err = tx.Exec(ctx, q, co, job, app); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *pgxpool.Pool, co, table string) int {
	t.Helper()
	ctx := context.Background()
	tx, _ := db.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	_, _ = tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, co)
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1::uuid`, co).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeletesOneCompanyOnly(t *testing.T) {
	db := newDB(t)
	st := memory.New()
	h := &Handler{
		Pools:   func(string) (inbox.DB, error) { return db, nil },
		Storage: func(string) (blob.BlobStore, error) { return st, nil },
	}
	const jobA, jobB = "aaaaaaaa-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000001"
	const appA, appB = "aaaaaaaa-0000-4000-8000-0000000000a1", "bbbbbbbb-0000-4000-8000-0000000000b1"
	seed(t, db, coA, jobA, appA)
	seed(t, db, coB, jobB, appB)

	e := ev(t, Type, Payload{CompanyID: coA, Region: "uk", Reason: "closed by owner"})
	if err := h.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"hiring.applications", "hiring.application_files", "hiring.job_applicant_counts"} {
		if n := count(t, db, coA, tbl); n != 0 {
			t.Errorf("%s of the deleted company: %d rows left", tbl, n)
		}
		if n := count(t, db, coB, tbl); n != 1 {
			t.Errorf("%s of the other company: %d rows, want 1", tbl, n)
		}
	}
	if n := count(t, db, coA, "hiring.app_audit_events"); n != 1 {
		t.Errorf("audit rows are kept: %d", n)
	}
	if n := count(t, db, coA, "hiring.app_compliance_log"); n != 1 {
		t.Errorf("one compliance row records the deletion: %d", n)
	}

	// A redelivery is acknowledged without running again (still one compliance row).
	if err := h.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, coA, "hiring.app_compliance_log"); n != 1 {
		t.Errorf("redelivery wrote again: %d", n)
	}
}

func TestStorageFailureRetriesAndKeepsRows(t *testing.T) {
	db := newDB(t)
	h := &Handler{
		Pools:   func(string) (inbox.DB, error) { return db, nil },
		Storage: func(string) (blob.BlobStore, error) { return nil, errors.New("storage down") },
	}
	const job, app = "aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-0000000000a1"
	seed(t, db, coA, job, app)
	err := h.Handle(context.Background(), ev(t, Type, Payload{CompanyID: coA, Region: "uk"}))
	if err == nil || isTerminal(err) {
		t.Fatalf("a storage failure must be retried, got %v", err)
	}
	if n := count(t, db, coA, "hiring.applications"); n != 1 {
		t.Errorf("rows must stay until the objects are gone: %d", n)
	}
}
