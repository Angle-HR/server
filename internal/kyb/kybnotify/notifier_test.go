package kybnotify

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/mailer"
)

const testOrgID = "22222222-2222-4222-8222-222222222222"

type fakeEnqueuer struct {
	args []fluvio.JobArgs
	err  error
}

func (f *fakeEnqueuer) EnqueueTx(
	_ context.Context, _ fluvio.Tx, args fluvio.JobArgs, _ ...fluvio.EnqueueOption,
) (*fluvio.JobRow, error) {
	f.args = append(f.args, args)
	return &fluvio.JobRow{}, f.err
}

func newNotifier(t *testing.T) (*Notifier, pgxmock.PgxPoolIface, pgxmock.PgxPoolIface, *fakeEnqueuer) {
	t.Helper()
	regional, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	global, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	enq := &fakeEnqueuer{}
	return &Notifier{Regional: regional, Global: global, Enqueuer: enq}, regional, global, enq
}

func expectOwner(regional pgxmock.PgxPoolIface) {
	regional.ExpectQuery(regexp.QuoteMeta("FROM accounts.organizations o")).WithArgs(testOrgID).
		WillReturnRows(pgxmock.NewRows([]string{"email", "name", "legal_name"}).
			AddRow("ada@example.com", "Ada", "Acme Ltd"))
}

func TestNotify_queuesEmailWithReason(t *testing.T) {
	n, regional, global, enq := newNotifier(t)
	expectOwner(regional)
	global.ExpectBegin()
	global.ExpectCommit()

	if err := n.Notify(context.Background(), testOrgID, kyb.NoticeFailed, kyb.ReasonNameMismatch); err != nil {
		t.Fatal(err)
	}
	if len(enq.args) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(enq.args))
	}
	got, ok := enq.args[0].(mailer.EmailArgs)
	if !ok || got.Type != mailer.TypeKYBFailed || got.Recipient != "ada@example.com" ||
		got.FullName != "Ada" || got.OrganizationName != "Acme Ltd" || got.FailureReason != "name_mismatch" {
		t.Fatalf("unexpected email args: %+v", enq.args[0])
	}
	if err := global.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNotify_mapsEveryNotice(t *testing.T) {
	want := map[kyb.Notice]string{
		kyb.NoticeFailed:      mailer.TypeKYBFailed,
		kyb.NoticeReviewQueue: mailer.TypeKYBReviewQueued,
		kyb.NoticeNudge1:      mailer.TypeKYBNudge1,
		kyb.NoticeNudge2:      mailer.TypeKYBNudge2,
	}
	for notice, emailType := range want {
		if emailTypes[notice] != emailType {
			t.Errorf("%s: got %q, want %q", notice, emailTypes[notice], emailType)
		}
	}
}

func TestNotify_noOwner(t *testing.T) {
	n, regional, _, enq := newNotifier(t)
	regional.ExpectQuery(regexp.QuoteMeta("FROM accounts.organizations o")).
		WithArgs(testOrgID).WillReturnError(pgx.ErrNoRows)

	err := n.Notify(context.Background(), testOrgID, kyb.NoticeNudge1, kyb.ReasonNumberNotFound)
	if !errors.Is(err, ErrNoRecipient) || len(enq.args) != 0 {
		t.Fatalf("got %v, enqueued %d", err, len(enq.args))
	}
}

func TestNotify_enqueueFailureRollsBack(t *testing.T) {
	n, regional, global, enq := newNotifier(t)
	boom := errors.New("boom")
	enq.err = boom
	expectOwner(regional)
	global.ExpectBegin()
	global.ExpectRollback()

	err := n.Notify(context.Background(), testOrgID, kyb.NoticeReviewQueue, "")
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want wrapped boom", err)
	}
	if err := global.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNotify_unknownNotice(t *testing.T) {
	n, _, _, _ := newNotifier(t)
	if err := n.Notify(context.Background(), testOrgID, kyb.Notice("nope"), ""); err == nil {
		t.Fatal("want error")
	}
}
