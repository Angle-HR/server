package jobevents_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/jobevents"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/formschema/screening"
)

var now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func input(status string) jobevents.Input {
	j := jobs.Job{
		ID: "job-1", JobNumber: 7, JobCode: "JB-7", PublicID: "jabc", Status: status, Revision: 5,
		PublishedAt: "2026-11-01T10:00:00Z", UpdatedAt: "2026-11-23T11:00:00Z",
	}
	j.Title, j.ClosingDate, j.RetentionMonths, j.LawfulBasis = "Engineer", "2026-12-31", 12, "contract"
	j.Markets = []jobs.MarketSel{{MarketCode: "GB"}, {MarketCode: "GB", City: "Leeds"}, {MarketCode: "DE"}}
	return jobevents.Input{
		Region: "uk", CompanyID: "co-1", Job: j, Now: now,
		Form:    &jobevents.Form{ID: "f1", Version: 3, Schema: json.RawMessage(`{"form_revision":2,"questions":[]}`)},
		Rules:   []screening.Rule{{ID: "r1", QuestionID: "q1", Operator: "eq", Value: json.RawMessage(`"no"`), Reason: "x"}},
		Decls:   []formschema.Declaration{{Category: "health_disability", Purpose: "adjustments"}},
		Company: jobevents.Company{Name: "Acme", PrivacyEmail: "privacy@acme.test", DPOContact: "dpo@acme.test"},
	}
}

func TestSnapshotCarriesWhatApplyNeeds(t *testing.T) {
	p := jobevents.Snapshot(input(jobs.StatusPublished))
	if p.Version != 5 || p.JobID != "job-1" || p.PublicID != "jabc" || p.CompanyID != "co-1" || p.Region != "uk" {
		t.Fatalf("identity: %+v", p)
	}
	if p.Status != "published" || p.PublicState != "open" || p.ClosingDate != "2026-12-31" || p.RetentionMonths != 12 {
		t.Fatalf("state: %+v", p)
	}
	if p.JobCode != "JB-7" || p.Title != "Engineer" || len(p.Markets) != 2 || p.Markets[0] != "GB" || p.Markets[1] != "DE" {
		t.Fatalf("job: %+v", p)
	}
	if p.Form == nil || p.Form.Version != 3 || string(p.Form.Schema) != `{"form_revision":2,"questions":[]}` {
		t.Fatalf("form: %+v", p.Form)
	}
	if len(p.Rules) != 1 || p.Company.Name != "Acme" || p.Company.PrivacyEmail != "privacy@acme.test" {
		t.Fatalf("rules/company: %+v", p)
	}
	if p.Notice.Version == "" || !p.Notice.ScreeningActive || len(p.Notice.Declarations) != 1 || p.Notice.LawfulBasis != "contract" {
		t.Fatalf("notice: %+v", p.Notice)
	}
}

func TestPublicState(t *testing.T) {
	for status, want := range map[string]string{
		jobs.StatusPublished: "open", jobs.StatusPaused: "paused", jobs.StatusClosed: "closed",
		jobs.StatusExpired: "closed", jobs.StatusDraft: "closed",
	} {
		if got := jobevents.Snapshot(input(status)).PublicState; got != want {
			t.Errorf("%s: %s, want %s", status, got, want)
		}
	}
	in := input(jobs.StatusPublished)
	in.Job.ClosingDate = "2026-11-22" // passed, expiry task has not run yet
	if got := jobevents.Snapshot(in).PublicState; got != "closed" {
		t.Errorf("past closing date: %s", got)
	}
}

func TestSnapshotNeverHasNullLists(t *testing.T) {
	in := input(jobs.StatusPaused)
	in.Rules, in.Decls, in.Form, in.Job.Markets = nil, nil, nil, nil
	raw, err := json.Marshal(jobevents.Snapshot(in))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"rules", "declarations", "markets"} {
		if _, ok := m[k].([]any); !ok {
			t.Errorf("%s is %v, want []", k, m[k])
		}
	}
	if _, ok := m["form"]; ok {
		t.Error("form should be omitted when the job never published")
	}
}

func TestBuildIsPartitionedByJobAndIDIsDeterministic(t *testing.T) {
	p := jobevents.Snapshot(input(jobs.StatusPublished))
	a, err := jobevents.Build(jobevents.TypePublished, p, now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := jobevents.Build(jobevents.TypePublished, p, now.Add(time.Hour))
	if a.ID != b.ID || a.ID == "" {
		t.Fatalf("ids differ: %s %s", a.ID, b.ID)
	}
	p2 := p
	p2.Version++
	c, _ := jobevents.Build(jobevents.TypePublished, p2, now)
	d, _ := jobevents.Build(jobevents.TypePaused, p, now)
	if c.ID == a.ID || d.ID == a.ID {
		t.Fatal("version and type must change the id")
	}
	if a.PartitionKey != "job-1" || a.Subject != "job-1" || a.CompanyID != "co-1" || a.Region != "uk" ||
		a.Source != jobevents.Source || a.Type != "hiring.job.published.v1" {
		t.Fatalf("envelope: %+v", a)
	}
	if err = a.Validate(); err != nil {
		t.Fatal(err)
	}
	var back jobevents.Payload
	if err = a.DecodeJSON(&back); err != nil || back.Version != 5 {
		t.Fatalf("decode: %v %+v", err, back)
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name        string
		c           jobevents.Change
		typ, reason string
		ok          bool
	}{
		{"publish", jobevents.Change{From: jobs.StatusDraft, To: jobs.StatusPublished}, jobevents.TypePublished, "", true},
		{"approve", jobevents.Change{From: jobs.StatusPendingApproval, To: jobs.StatusPublished}, jobevents.TypePublished, "", true},
		{"reopen", jobevents.Change{From: jobs.StatusClosed, To: jobs.StatusPublished}, jobevents.TypePublished, "", true},
		{"pause", jobevents.Change{From: jobs.StatusPublished, To: jobs.StatusPaused}, jobevents.TypePaused, "", true},
		{"resume", jobevents.Change{From: jobs.StatusPaused, To: jobs.StatusPublished}, jobevents.TypeResumed, "", true},
		{"close published", jobevents.Change{From: jobs.StatusPublished, To: jobs.StatusClosed}, jobevents.TypeClosed, "closed", true},
		{"close paused", jobevents.Change{From: jobs.StatusPaused, To: jobs.StatusClosed}, jobevents.TypeClosed, "closed", true},
		{"expire", jobevents.Change{From: jobs.StatusPublished, To: jobs.StatusExpired}, jobevents.TypeClosed, "expired", true},
		{"to draft", jobevents.Change{From: jobs.StatusPublished, To: jobs.StatusDraft}, jobevents.TypeClosed, "to_draft", true},
		{"edit live", jobevents.Change{From: jobs.StatusPublished, To: jobs.StatusPublished, Content: true}, jobevents.TypeUpdated, "", true},
		{"edit paused", jobevents.Change{From: jobs.StatusPaused, To: jobs.StatusPaused, Content: true}, jobevents.TypeUpdated, "", true},
		{"edit draft", jobevents.Change{From: jobs.StatusDraft, To: jobs.StatusDraft, Content: true}, "", "", false},
		{"progress only", jobevents.Change{From: jobs.StatusPublished, To: jobs.StatusPublished}, "", "", false},
		{"submit", jobevents.Change{From: jobs.StatusDraft, To: jobs.StatusPendingApproval}, "", "", false},
		{"archive closed", jobevents.Change{From: jobs.StatusClosed, To: jobs.StatusArchived}, "", "", false},
	}
	for _, tc := range cases {
		typ, reason, ok := jobevents.Decide(tc.c)
		if typ != tc.typ || reason != tc.reason || ok != tc.ok {
			t.Errorf("%s: got %q %q %v", tc.name, typ, reason, ok)
		}
	}
}

func TestEnabled(t *testing.T) {
	for v, want := range map[string]bool{"": false, "off": false, "on": true, "ON": true, " on ": true, "true": false} {
		t.Setenv(jobevents.EnvVar, v)
		if jobevents.Enabled() != want {
			t.Errorf("%q: want %v", v, want)
		}
	}
}
