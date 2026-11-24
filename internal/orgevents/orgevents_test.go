package orgevents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeTx struct{ args [][]any }

func (f *fakeTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.args = append(f.args, args)
	return pgconn.CommandTag{}, nil
}

var t0 = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func TestGateAndFailClosed(t *testing.T) {
	tx := &fakeTx{}
	t.Setenv(EnvVar, "")
	if err := EnqueueDeleted(context.Background(), tx, CompanyDeleted{CompanyID: "c"}); err != nil || len(tx.args) != 0 {
		t.Fatalf("off must write nothing: %v", err)
	}
	t.Setenv(EnvVar, "ON")
	if err := EnqueueDeleted(context.Background(), tx, CompanyDeleted{CompanyID: "c", Region: "uk", DeletedAt: t0}); err != nil || len(tx.args) != 1 {
		t.Fatalf("on must write one row: %v", err)
	}
	if err := EnqueueVerified(context.Background(), tx, CompanyVerified{CompanyID: "c"}); err != ErrNoRegion {
		t.Fatalf("no region must fail closed: %v", err)
	}
}

func TestPayloadsCarryNoPII(t *testing.T) {
	a, err := AgreementAcceptedEvent(AgreementAccepted{CompanyID: "c1", Region: "uk", Kind: "terms_dpa", Version: "v1", AcceptedByUserID: "u1", AcceptedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := AgreementAcceptedEvent(AgreementAccepted{CompanyID: "c1", Region: "uk", Kind: "terms_dpa", Version: "v1", AcceptedByUserID: "u1", AcceptedAt: t0})
	if a.ID != b.ID || a.Type != TypeAgreementAccepted || a.CompanyID != "c1" || a.PartitionKey != "c1" {
		t.Fatalf("%+v", a)
	}
	if err = a.Validate(); err != nil {
		t.Fatal(err)
	}
	p, _ := ProfileUpdatedEvent(CompanyProfileUpdated{CompanyID: "c1", Region: "uk", Changed: []string{"privacy_contact"}, UpdatedAt: t0})
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "@") {
		t.Fatalf("payload must not carry addresses: %s", raw)
	}
}

type kindRow struct{ kind string }

func (k kindRow) Scan(dest ...any) error { *(dest[0].(*string)) = k.kind; return nil }

type kindQuerier struct {
	kind  string
	calls int
}

func (k *kindQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	k.calls++
	return kindRow{k.kind}
}

func TestEventsCarryCompanyKind(t *testing.T) {
	q := &kindQuerier{kind: KindIndividual}
	t.Setenv(EnvVar, "")
	if kind, err := KindOf(context.Background(), q, "c1"); err != nil || kind != "" || q.calls != 0 {
		t.Fatalf("off must not read: %q %v %d", kind, err, q.calls)
	}
	t.Setenv(EnvVar, "on")
	kind, err := KindOf(context.Background(), q, "c1")
	if err != nil || kind != KindIndividual {
		t.Fatalf("kind = %q err %v", kind, err)
	}
	ev, err := VerifiedEvent(CompanyVerified{CompanyKind: kind, CompanyID: "c1", Region: "uk", Status: "verified", VerifiedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err = json.Unmarshal(ev.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["company_kind"] != "individual" {
		t.Fatalf("payload = %v", payload)
	}
	for _, name := range []string{"name", "legal_name", "email"} {
		if _, ok := payload[name]; ok {
			t.Fatalf("payload must not carry %s", name)
		}
	}
}
