package jobmodel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
)

const (
	tCompany = "11111111-1111-4111-8111-111111111111"
	tJob     = "22222222-2222-4222-8222-222222222222"
	tForm    = "33333333-3333-4333-8333-333333333333"
)

// snap returns a published job snapshot as a map so tests change single fields.
func snap(version int64, status, state string) map[string]any {
	return map[string]any{
		"version": version, "job_id": tJob, "public_id": "PUB1", "company_id": tCompany, "region": "uk",
		"status": status, "public_state": state, "closing_date": "2099-01-01", "retention_months": 6,
		"job_number": 7, "job_code": "JB-7", "title": "Engineer", "lawful_basis": "legitimate_interests",
		"markets": []string{"NG", "GB", "GB"}, "published_at": "2026-01-02T03:04:05Z", "updated_at": "2026-01-02T03:04:05Z",
		"form": map[string]any{"id": tForm, "version": 1, "schema": map[string]any{"form_revision": 1,
			"questions": []map[string]any{{"id": "q1", "type": "text", "label": "Why?", "required": true}}}},
		"rules":        []map[string]any{{"id": "r1", "question_id": "q1", "operator": "eq", "value": "x", "reason": "no"}},
		"declarations": []map[string]any{{"category": "health", "legal_condition": "c", "purpose": "p"}},
		"company":      map[string]any{"name": "Acme Ltd", "privacy_email": "privacy@acme.test", "dpo_contact": "DPO"},
		"notice":       map[string]any{"version": "v", "declarations": []any{}, "screening_active": true},
	}
}

func ev(t testing.TB, typ, id string, p map[string]any) events.Event {
	t.Helper()
	e, err := events.NewJSON("/services/hiring", typ, p)
	if err != nil {
		t.Fatal(err)
	}
	e.ID = id
	e.Time = time.Now()
	return e.WithSubject(tJob).WithRegion("uk").WithCompany(tCompany)
}

func pools(pool *pgxpool.Pool) Pools {
	return func(region string) (inbox.DB, error) {
		if region != "uk" {
			return nil, ErrUnknownRegion
		}
		return pool, nil
	}
}

func mustHandle(t testing.TB, p *Projector, e events.Event) {
	t.Helper()
	if err := p.Handle(context.Background(), e); err != nil {
		t.Fatalf("handle %s: %v", e.Type, err)
	}
}

func dbVersion(t testing.TB, pool *pgxpool.Pool) (int64, string, string) {
	t.Helper()
	var v int64
	var status, state string
	err := pool.QueryRow(context.Background(), `SELECT version, status, public_state FROM hiring.app_jobs WHERE job_id = $1`, tJob).Scan(&v, &status, &state)
	if err != nil {
		t.Fatal(err)
	}
	return v, status, state
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }
