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

func TestReactivatedAndRegionMovedEvents(t *testing.T) {
	r1, err := ReactivatedEvent(CompanyReactivated{CompanyID: "c1", Region: "uk", CompanyKind: KindBusiness, ReactivatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := ReactivatedEvent(CompanyReactivated{CompanyID: "c1", Region: "uk", CompanyKind: KindBusiness, ReactivatedAt: t0})
	r3, _ := ReactivatedEvent(CompanyReactivated{CompanyID: "c1", Region: "uk", CompanyKind: KindBusiness, ReactivatedAt: t0.Add(time.Hour)})
	if r1.Type != "org.company.reactivated.v1" || r1.ID != r2.ID || r1.ID == r3.ID || r1.PartitionKey != "c1" {
		t.Fatalf("%+v", r1)
	}
	if err = r1.Validate(); err != nil {
		t.Fatal(err)
	}

	// Two saga attempts for the same user and region emit the same event id: the outbox drops the second.
	m1, err := RegionMovedEvent(RegionMoved{SagaID: "s1", UserID: "u1", CompanyID: "c1", CompanyKind: KindBusiness, FromRegion: "global", ToRegion: "eu", MovedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	m2, _ := RegionMovedEvent(RegionMoved{SagaID: "s2", UserID: "u1", FromRegion: "global", ToRegion: "eu", MovedAt: t0.Add(time.Minute)})
	if m1.Type != "org.region.moved.v1" || m1.ID != m2.ID || m1.PartitionKey != "u1" || m1.Subject != "u1" || m1.Region != "eu" || m1.CompanyID != "c1" || m2.CompanyID != "" {
		t.Fatalf("%+v %+v", m1, m2)
	}
	if err = m1.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m1)
	for _, banned := range []string{"@", "email", "password", "legal_name"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("payload leaks %q: %s", banned, raw)
		}
	}

	tx := &fakeTx{}
	t.Setenv(EnvVar, "on")
	if err = EnqueueRegionMoved(context.Background(), tx, RegionMoved{UserID: "u1", FromRegion: "global", MovedAt: t0}); err != ErrNoRegion {
		t.Fatalf("no region must fail closed: %v", err)
	}
	if err = EnqueueReactivated(context.Background(), tx, CompanyReactivated{CompanyID: "c1", Region: "uk", ReactivatedAt: t0}); err != nil || len(tx.args) != 1 {
		t.Fatalf("%v", err)
	}
}
