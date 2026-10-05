package kybstore

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/kyb"
)

const testOrgID = "22222222-2222-4222-8222-222222222222"

func TestLoad_noRecord(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("FROM accounts.organization_verifications")).
		WithArgs(testOrgID).WillReturnError(pgx.ErrNoRows)

	rec, err := (&Store{DB: mock}).Load(context.Background(), testOrgID)
	if err != nil || rec != nil {
		t.Fatalf("got %v, %v; want nil, nil", rec, err)
	}
}

func TestLoad_record(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	checked := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	status := "failed"
	mock.ExpectQuery(regexp.QuoteMeta("FROM accounts.organization_verifications")).
		WithArgs(testOrgID).
		WillReturnRows(pgxmock.NewRows([]string{
			"organization_id", "country_code", "registration_number", "identifiers", "legal_name_submitted",
			"submitted_address", "address_type", "tier", "failure_reason", "registry_name", "registered_address",
			"attempts", "checked_at", "verified_at", "reviewer_id", "notices_sent", "kyb_status",
		}).AddRow(testOrgID, "GB", "01234567", []byte(`{"vat":"GB1"}`), "Acme Ltd",
			[]byte(`{"Line1":"1 High St","PostCode":"AB1 2CD"}`), "", int16(1), "name_mismatch", "ACME LIMITED",
			"1 High St", int32(2), &checked, (*time.Time)(nil), "", int16(1), &status))

	rec, err := (&Store{DB: mock}).Load(context.Background(), testOrgID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != kyb.StatusFailed || rec.FailureReason != kyb.ReasonNameMismatch || rec.Tier != 1 ||
		rec.Attempts != 2 || rec.NoticesSent != 1 || rec.Address.PostCode != "AB1 2CD" ||
		rec.Identifiers["vat"] != "GB1" || rec.CheckedAt == nil || !rec.CheckedAt.Equal(checked) {
		t.Fatalf("unexpected record: %+v", rec)
	}
}

func TestSave_commitsRecordStatusAndEvents(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO accounts.organization_verifications").WithArgs(
		testOrgID, "GB", "01234567", pgxmock.AnyArg(), "Acme Ltd", pgxmock.AnyArg(), "", 1, "",
		"", "", 1, &now, &now, "", 0).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE accounts.organizations").
		WithArgs(testOrgID, "verified", &now).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("INSERT INTO accounts.organization_verification_events").WithArgs(
		testOrgID, "verified", "verified", "", "", pgxmock.AnyArg(), now).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	rec := &kyb.Record{OrganizationID: testOrgID, CountryCode: "GB", RegistrationNumber: "01234567",
		LegalName: "Acme Ltd", Tier: 1, Status: kyb.StatusVerified, Attempts: 1, CheckedAt: &now, VerifiedAt: &now}
	ev := kyb.Event{OrganizationID: testOrgID, Name: "verified", Status: kyb.StatusVerified, At: now}
	if err := (&Store{DB: mock}).Save(context.Background(), rec, ev); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSave_rollsBackOnError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO accounts.organization_verifications").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).WillReturnError(boom)
	mock.ExpectRollback()

	rec := &kyb.Record{OrganizationID: testOrgID, Status: kyb.StatusPending}
	if err := (&Store{DB: mock}).Save(context.Background(), rec); !errors.Is(err, boom) {
		t.Fatalf("got %v, want wrapped boom", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQueue_enqueueAndResolve(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	q := &Queue{DB: mock, Region: "africa"}
	mock.ExpectExec("INSERT INTO admin.verification_queue").
		WithArgs(testOrgID, "africa", "KE").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE admin.verification_queue").
		WithArgs(testOrgID, "approved", "rev-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE admin.verification_queue").
		WithArgs(testOrgID, "rejected", "rev-1").WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	ctx := context.Background()
	if err := q.Enqueue(ctx, testOrgID, "KE"); err != nil {
		t.Fatal(err)
	}
	if err := q.Resolve(ctx, testOrgID, "rev-1", true); err != nil {
		t.Fatal(err)
	}
	if err := q.Resolve(ctx, testOrgID, "rev-1", false); !errors.Is(err, ErrNoPendingItem) {
		t.Fatalf("got %v, want ErrNoPendingItem", err)
	}
}
