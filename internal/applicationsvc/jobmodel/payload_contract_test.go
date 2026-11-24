package jobmodel

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/jobevents"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/formschema/screening"
)

// The consumer-side payload must read what the producer (internal/hiring/jobevents) writes. This test is the only
// place the package imports hiring.
func TestPayloadMatchesProducer(t *testing.T) {
	if jobevents.TypePublished != TypePublished || jobevents.TypeUpdated != TypeUpdated || jobevents.TypePaused != TypePaused ||
		jobevents.TypeResumed != TypeResumed || jobevents.TypeClosed != TypeClosed {
		t.Fatal("event type constants drifted from jobevents")
	}
	now := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	job := jobs.Job{ID: tJob, PublicID: "PUB1", Status: "closed", JobNumber: 7, JobCode: "JB-7", Revision: 9,
		UpdatedAt: "2026-05-06T07:08:09Z", PublishedAt: "2026-01-01T00:00:00Z"}
	job.LocationMode, job.CreatedBy = "anywhere", tCreator
	job.ClosingDate, job.RetentionMonths, job.Title, job.LawfulBasis = "2026-05-01", 12, "Engineer", "consent"
	job.Markets = []jobs.MarketSel{{MarketCode: "GB"}, {MarketCode: "NG"}}
	in := jobevents.Input{
		Job:    job,
		Region: "uk", CompanyID: tCompany, Now: now, Reason: jobevents.ReasonClosed,
		Form:    &jobevents.Form{ID: tForm, Version: 4, Schema: json.RawMessage(`{"form_revision":4,"questions":[]}`)},
		Rules:   []screening.Rule{{ID: "r", QuestionID: "q", Operator: "eq", Value: json.RawMessage(`1`), Reason: "x"}},
		Decls:   []formschema.Declaration{{Category: "health", LegalCondition: "c", Purpose: "p"}},
		Company: jobevents.Company{Name: "Acme", PrivacyEmail: "p@acme.test", DPOContact: "dpo"},
	}
	e, err := jobevents.Build(jobevents.TypeClosed, jobevents.Snapshot(in), now)
	if err != nil {
		t.Fatal(err)
	}
	var p payload
	if err := e.DecodeJSON(&p); err != nil {
		t.Fatal(err)
	}
	r, err := p.toRow(now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 9 || r.JobID != tJob || r.PublicID != "PUB1" || r.CompanyID != tCompany || r.Region != "uk" ||
		r.Status != "closed" || r.PublicState != "closed" || r.ClosingDate != "2026-05-01" || r.RetentionMonths != 12 ||
		r.JobCode != "JB-7" || r.Title != "Engineer" || r.CompanyName != "Acme" || r.PrivacyEmail != "p@acme.test" ||
		r.DPOContact != "dpo" || r.FormID == nil || *r.FormID != tForm || r.FormVersion != 4 || r.CloseReason != "closed" ||
		r.LocationMode != "anywhere" || r.CreatedBy != tCreator || len(r.Markets) != 2 || r.ClosedAt == nil || jsonOf(json.RawMessage(r.Declarations)) == "" {
		t.Fatalf("producer payload not read correctly: %+v", r)
	}
}
