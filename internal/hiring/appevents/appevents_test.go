package appevents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/pkg/events"
)

type fakeTx struct{ args [][]any }

func (f *fakeTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.args = append(f.args, args)
	return pgconn.CommandTag{}, nil
}

var t0 = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func TestGate(t *testing.T) {
	tx := &fakeTx{}
	t.Setenv(EnvVar, "")
	if err := EnqueueSubmitted(context.Background(), tx, Submitted{}); err != nil || len(tx.args) != 0 {
		t.Fatalf("off must write nothing: %v %d", err, len(tx.args))
	}
	t.Setenv(EnvVar, "ON")
	if err := EnqueueSubmitted(context.Background(), tx, Submitted{ApplicationID: "a", Region: "uk", SubmittedAt: t0}); err != nil || len(tx.args) != 1 {
		t.Fatalf("on must write one row: %v %d", err, len(tx.args))
	}
	if err := EnqueueStatusChanged(context.Background(), tx, StatusChanged{ApplicationID: "a"}); err != ErrNoRegion {
		t.Fatalf("no region must fail closed: %v", err)
	}
}

func TestEventsAreSmallAndDeterministic(t *testing.T) {
	s := Submitted{ApplicationID: "app1", JobID: "job1", CompanyID: "co1", Region: "uk", Status: "received", Source: "direct", SubmittedAt: t0}
	a, err := SubmittedEvent(s)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SubmittedEvent(s)
	if a.ID != b.ID || a.Type != TypeSubmitted || a.Subject != "app1" || a.CompanyID != "co1" || a.Region != "uk" || a.PartitionKey != "app1" {
		t.Fatalf("%+v", a)
	}
	if err = a.Validate(); err != nil {
		t.Fatal(err)
	}
	c, err := StatusChangedEvent(StatusChanged{ApplicationID: "app1", JobID: "job1", CompanyID: "co1", Region: "uk",
		FromStatus: "received", ToStatus: "in_review", ChangedByUserID: "u1", ChangedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	if c.Type != TypeStatusChanged || c.ID == a.ID {
		t.Fatalf("%+v", c)
	}
	for _, ev := range []events.Event{a, c} {
		var m map[string]any
		if err = json.Unmarshal(ev.Data, &m); err != nil {
			t.Fatal(err)
		}
		for k := range m {
			if strings.Contains(k, "email") || strings.Contains(k, "name") || strings.Contains(k, "answer") {
				t.Errorf("payload must hold no personal data: %s", k)
			}
		}
	}
}
