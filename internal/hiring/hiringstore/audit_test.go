package hiringstore

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/internal/hiring/draft"
)

const auditTenant = "bbbbbbbb-0000-4000-8000-000000000001"

func TestAuditEventsReadsBothTablesInTenantTx(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	from := at.Add(-time.Hour)
	after := &draft.AuditCursor{CreatedAt: at, Source: "hiring", ID: 7}
	actor := "aaaaaaaa-0000-4000-8000-000000000001"

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config('app.tenant_id', $1, true)`).WithArgs(auditTenant).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(auditEventsSQL).
		WithArgs(auditTenant, "", actor, "job.updated", &from, (*time.Time)(nil), at, "hiring", int64(7), 3).
		WillReturnRows(pgxmock.NewRows([]string{"source", "id", "job_id", "actor_id", "action", "diff", "created_at"}).
			AddRow("applications", int64(9), "job1", &actor, "application.viewed", []byte(`{"a":1}`), at).
			AddRow("hiring", int64(5), "job1", nil, "job.updated", nil, at))
	mock.ExpectCommit()

	s := &Store{DB: mock}
	got, err := s.AuditEvents(context.Background(), auditTenant, draft.AuditFilter{
		ActorID: actor, Action: "job.updated", From: &from, After: after, Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Source != "applications" || string(got[0].Diff) != `{"a":1}` || got[1].ActorID != nil {
		t.Fatalf("rows = %+v", got)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
