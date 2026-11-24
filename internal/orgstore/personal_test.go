package orgstore

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/region"
)

const (
	testUser = "11111111-1111-4111-8111-111111111111"
	testOrg  = "22222222-2222-4222-8222-222222222222"
)

type fakeQueue struct{ cases []string }

func (f *fakeQueue) EnqueueCase(_ context.Context, org, country, caseType string) error {
	f.cases = append(f.cases, org+"/"+country+"/"+caseType)
	return nil
}

func storeFor(m pgxmock.PgxPoolIface) *Store {
	return &Store{Regional: func(region.Region) (DB, error) { return m, nil }}
}

var verifCols = []string{
	"organization_id", "country_code", "registration_number", "identifiers", "legal_name_submitted",
	"submitted_address", "address_type", "tier", "failure_reason", "registry_name", "registered_address",
	"attempts", "checked_at", "verified_at", "reviewer_id", "notices_sent", "kyb_status",
}

func expectExistingOrg(m pgxmock.PgxPoolIface, kind, status string, deleted bool) {
	m.ExpectExec(regexp.QuoteMeta("INSERT INTO accounts.organizations")).
		WithArgs(testUser, "Ada Lovelace", PersonalCompanyRoleID, "", "ind-1", 0, "c-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	m.ExpectQuery(regexp.QuoteMeta("WHERE owner_user_id")).WithArgs(testUser).
		WillReturnRows(pgxmock.NewRows([]string{"id", "kind", "kyb_status", "deleted"}).AddRow(testOrg, kind, status, deleted))
}

func personal() Personal {
	return Personal{UserID: testUser, Name: " Ada Lovelace ", CountryID: "c-1", CountrySlug: "nigeria", IndustryID: "ind-1"}
}

func TestEnsurePersonal_pendingOnlyReopensTheCase(t *testing.T) {
	m, _ := pgxmock.NewPool()
	expectExistingOrg(m, "individual", "pending", false)
	pending := "pending"
	checked := time.Now().UTC()
	m.ExpectQuery(regexp.QuoteMeta("FROM accounts.organization_verifications v")).WithArgs(testOrg).
		WillReturnRows(pgxmock.NewRows(verifCols).AddRow(testOrg, "nigeria", "", []byte(`{}`), "Ada Lovelace", []byte(`{}`),
			"", int16(2), "", "", "", int32(1), &checked, (*time.Time)(nil), "", int16(0), &pending))
	m.ExpectQuery(regexp.QuoteMeta("FROM accounts.organizations o WHERE o.id")).WithArgs(testOrg).
		WillReturnRows(pgxmock.NewRows([]string{"legal_name", "country_code"}).AddRow("Ada Lovelace", "nigeria"))

	q := &fakeQueue{}
	out, err := storeFor(m).EnsurePersonal(context.Background(), region.RegionUK, personal(), q)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != testOrg || out.Status != "pending" || out.Created {
		t.Fatalf("unexpected outcome %+v", out)
	}
	if len(q.cases) != 1 || q.cases[0] != testOrg+"/nigeria/individual" {
		t.Fatalf("cases = %v", q.cases)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsurePersonal_deletedIsLeftAlone(t *testing.T) {
	m, _ := pgxmock.NewPool()
	expectExistingOrg(m, "individual", "pending", true)
	q := &fakeQueue{}
	out, err := storeFor(m).EnsurePersonal(context.Background(), region.RegionUK, personal(), q)
	if err != nil || !out.Deleted || len(q.cases) != 0 {
		t.Fatalf("out %+v err %v cases %v", out, err, q.cases)
	}
}

func TestEnsurePersonal_businessOwnerRefused(t *testing.T) {
	m, _ := pgxmock.NewPool()
	expectExistingOrg(m, "business", "verified", false)
	if _, err := storeFor(m).EnsurePersonal(context.Background(), region.RegionUK, personal(), &fakeQueue{}); !errors.Is(err, ErrNotPersonal) {
		t.Fatalf("err = %v, want ErrNotPersonal", err)
	}
}

func TestEnsurePersonal_verifiedDoesNothingMore(t *testing.T) {
	m, _ := pgxmock.NewPool()
	expectExistingOrg(m, "individual", "verified", false)
	q := &fakeQueue{}
	out, err := storeFor(m).EnsurePersonal(context.Background(), region.RegionUK, personal(), q)
	if err != nil || out.Status != "verified" || len(q.cases) != 0 {
		t.Fatalf("out %+v err %v cases %v", out, err, q.cases)
	}
}

func TestEnsurePersonal_needsNameAndUser(t *testing.T) {
	m, _ := pgxmock.NewPool()
	if _, err := storeFor(m).EnsurePersonal(context.Background(), region.RegionUK, Personal{UserID: testUser}, &fakeQueue{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestPersonalReview_verifiedRefused(t *testing.T) {
	m, _ := pgxmock.NewPool()
	verified := "verified"
	at := time.Now().UTC()
	m.ExpectQuery(regexp.QuoteMeta("FROM accounts.organization_verifications v")).WithArgs(testOrg).
		WillReturnRows(pgxmock.NewRows(verifCols).AddRow(testOrg, "nigeria", "", []byte(`{}`), "Ada", []byte(`{}`),
			"", int16(2), "", "", "", int32(1), &at, &at, "", int16(0), &verified))
	m.ExpectQuery(regexp.QuoteMeta("FROM accounts.organizations o WHERE o.id")).WithArgs(testOrg).
		WillReturnRows(pgxmock.NewRows([]string{"legal_name", "country_code"}).AddRow("Ada", "nigeria"))
	_, err := storeFor(m).RequestPersonalReview(context.Background(), region.RegionUK, testOrg, testUser, &fakeQueue{})
	if !errors.Is(err, kyb.ErrAlreadyVerified) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestPersonalReview_failedGoesBackToPending(t *testing.T) {
	m, _ := pgxmock.NewPool()
	failed := "failed"
	at := time.Now().UTC()
	m.ExpectQuery(regexp.QuoteMeta("FROM accounts.organization_verifications v")).WithArgs(testOrg).
		WillReturnRows(pgxmock.NewRows(verifCols).AddRow(testOrg, "nigeria", "", []byte(`{}`), "Ada", []byte(`{}`),
			"", int16(2), "name_mismatch", "", "", int32(1), &at, (*time.Time)(nil), "rev", int16(0), &failed))
	m.ExpectQuery(regexp.QuoteMeta("FROM accounts.organizations o WHERE o.id")).WithArgs(testOrg).
		WillReturnRows(pgxmock.NewRows([]string{"legal_name", "country_code"}).AddRow("Ada", "nigeria"))
	m.ExpectBegin()
	any16 := make([]any, 16)
	for i := range any16 {
		any16[i] = pgxmock.AnyArg()
	}
	m.ExpectExec("INSERT INTO accounts.organization_verifications").WithArgs(any16...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE accounts.organizations").WithArgs(testOrg, "pending", pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectExec("INSERT INTO accounts.organization_verification_events").
		WithArgs(testOrg, "queued_for_review", "pending", "", testUser, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectCommit()

	q := &fakeQueue{}
	rec, err := storeFor(m).RequestPersonalReview(context.Background(), region.RegionUK, testOrg, testUser, q)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != kyb.StatusPending || rec.FailureReason != "" || rec.Attempts != 2 || len(q.cases) != 1 {
		t.Fatalf("rec %+v cases %v", rec, q.cases)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
