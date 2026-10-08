package draft

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/drafttest"
	"github.com/Angle-HR/server/internal/hiring/gates"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/hiring/screening"
	"github.com/Angle-HR/server/internal/rbac"
)

type fakeCompany struct {
	state CompanyState
	err   error
}

func (f *fakeCompany) State(context.Context, string) (CompanyState, error) { return f.state, f.err }

var okCompany = CompanyState{Verified: true, DPAAccepted: true, PrivacyContactSet: true}

const publishable = `{
 "title":"Senior Go Engineer","department_id":"` + dept + `","location_mode":"specific_area",
 "markets":[{"market_code":"UK"}],"travel_frequency":"none","visa_sponsorship":"no","employment_type":"full_time",
 "pay":{"type":"range","currency":"GBP","period":"year","min":50000,"max":70000,"visible":true},
 "lawful_basis":"contract"}`

// publishEnv is a service whose company is verified and whose catalog has one required UK gate.
func publishEnv(t *testing.T) (*Service, *drafttest.Store, *drafttest.Ref, *fakeCompany) {
	t.Helper()
	s, st := newService()
	ref := s.Ref.(*drafttest.Ref)
	ref.GateList = []gates.Gate{
		{ID: "uk-lawful", MarketCode: "UK", Severity: gates.SeverityRequired, Requirement: "Lawful basis recorded.", Version: 1,
			Trigger: json.RawMessage(`{"type":"always"}`)},
		{ID: "uk-works", MarketCode: "UK", Severity: gates.SeverityAdvisory, Requirement: "Works council informed.", Version: 1,
			Trigger: json.RawMessage(`{"type":"always"}`)},
	}
	co := &fakeCompany{state: okCompany}
	s.Company = co
	n := 0
	s.NewPublicID = func() string { n++; return "jpub" + string(rune('a'+n-1)) }
	return s, st, ref, co
}

func blockedCodes(t *testing.T, err error) map[string]bool {
	t.Helper()
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("want BlockedError, got %v", err)
	}
	m := map[string]bool{}
	for _, i := range be.Report.Blocking {
		m[i.Code] = true
	}
	return m
}

func confirmUK(t *testing.T, s *Service, c Caller, id string) {
	t.Helper()
	if _, err := s.ConfirmGate(context.Background(), c, id, "uk-lawful", true, 0); err != nil {
		t.Fatalf("confirm: %v", err)
	}
}

func readyJob(t *testing.T, s *Service, c Caller) *JobView {
	t.Helper()
	v := newDraft(t, s, c, publishable)
	confirmUK(t, s, c, v.ID)
	return v
}

func TestPublishBlockedUntilEverythingIsInPlace(t *testing.T) {
	s, _, _, co := publishEnv(t)
	co.state = CompanyState{}
	founder := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, founder, publishable)

	_, err := s.Publish(context.Background(), founder, v.ID, 0)
	got := blockedCodes(t, err)
	for _, c := range []string{gates.CodeCompanyNotVerified, gates.CodeDPANotAccepted, gates.CodePrivacyContactMissing, gates.CodeGateUnconfirmed} {
		if !got[c] {
			t.Errorf("missing %s in %v", c, got)
		}
	}
	// The dry run reports the same thing and writes nothing.
	r, err := s.PublishCheck(context.Background(), founder, v.ID)
	if err != nil || r.OK() {
		t.Fatalf("dry run: %v %+v", err, r)
	}
	if after, _ := s.Get(context.Background(), founder, v.ID); after.Status != jobs.StatusDraft {
		t.Errorf("status = %s", after.Status)
	}
}

func TestPublishHappyPath(t *testing.T) {
	s, st, ref, _ := publishEnv(t)
	founder := caller(userA, rbac.RoleFounder)
	founder.Region = "uk"
	v := readyJob(t, s, founder)

	res, err := s.Publish(context.Background(), founder, v.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Job.Status != jobs.StatusPublished || res.Job.PublicID != "jpuba" {
		t.Errorf("job = %s %q", res.Job.Status, res.Job.PublicID)
	}
	rec := st.Jobs[drafttest.Key(org, v.ID)]
	if rec.FormVersion != 1 || rec.Job.PublishedAt == "" {
		t.Errorf("form version %d, published_at %q", rec.FormVersion, rec.Job.PublishedAt)
	}
	if e := ref.Registry["jpuba"]; e.Region != "uk" || e.Status != "published" || e.OrganizationID != org {
		t.Errorf("registry = %+v", e)
	}
	has := map[string]bool{}
	for _, l := range st.Log {
		has[l] = true
	}
	for _, want := range []string{"uk-lawful|confirmed", "uk-works|unconfirmed", "system.publish|published"} {
		if !has[want] {
			t.Errorf("compliance log lacks %s: %v", want, st.Log)
		}
	}
	if len(res.Warnings) == 0 {
		t.Error("the unconfirmed advisory gate should come back as a warning")
	}
}

func TestPublishRegistryFailureLeavesDraft(t *testing.T) {
	s, st, ref, _ := publishEnv(t)
	founder := caller(userA, rbac.RoleFounder)
	v := readyJob(t, s, founder)
	ref.FailRegister = errors.New("global db down")
	if _, err := s.Publish(context.Background(), founder, v.ID, 0); err == nil {
		t.Fatal("expected an error")
	}
	if st.Jobs[drafttest.Key(org, v.ID)].Job.Status != jobs.StatusDraft {
		t.Error("a failed registry write must leave the job a draft")
	}
}

func TestHR2SubmitsForApprovalAndHR1Approves(t *testing.T) {
	s, _, _, _ := publishEnv(t)
	hr2, hr1 := caller(userB, rbac.RoleHR2), caller(userA, rbac.RoleHR1)
	// HR 2 cannot set pay or markets' lawful basis? It can draft; pay needs a role that sets it.
	v := newDraft(t, s, hr1, publishable)
	hr1own := v
	confirmUK(t, s, hr1, hr1own.ID)

	// HR 2 did not create this job, so HR 2 cannot submit it.
	if _, err := s.Publish(context.Background(), hr2, hr1own.ID, 0); err == nil {
		t.Fatal("HR 2 acted on another person's draft")
	}

	// A draft HR 2 created goes to pending approval.
	mine, err := s.Create(context.Background(), hr2, CreateRequest{Patch: patch(t, `{"title":"Cook"}`)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Publish(context.Background(), hr2, mine.ID, 0)
	if err != nil || res.Job.Status != jobs.StatusPendingApproval {
		t.Fatalf("submit: %v %+v", err, res)
	}
	// HR 2 cannot publish it afterwards either.
	if _, err = s.Publish(context.Background(), hr2, mine.ID, 0); !isForbidden(err) {
		t.Errorf("hr2 publish: %v", err)
	}
	// HR 1 sees the same checks: this one is incomplete, so approval is blocked.
	if _, err = s.Publish(context.Background(), hr1, mine.ID, 0); blockedCodes(t, err)[gates.CodeDetailsIncomplete] == false {
		t.Errorf("hr1 approval of an incomplete job: %v", err)
	}
	// Sent back.
	back, err := s.Transition(context.Background(), hr2, mine.ID, "withdraw", 0)
	if err != nil || back.Job.Status != jobs.StatusDraft {
		t.Fatalf("withdraw: %v", err)
	}
}

func TestLifecycleCycle(t *testing.T) {
	s, st, ref, co := publishEnv(t)
	hr1 := caller(userA, rbac.RoleHR1)
	v := readyJob(t, s, hr1)
	ctx := context.Background()
	if _, err := s.Publish(ctx, hr1, v.ID, 0); err != nil {
		t.Fatal(err)
	}
	pub := st.Jobs[drafttest.Key(org, v.ID)].Job.PublicID

	step := func(action, want string) {
		t.Helper()
		r, err := s.Transition(ctx, hr1, v.ID, action, 0)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if r.Job.Status != want {
			t.Fatalf("%s -> %s, want %s", action, r.Job.Status, want)
		}
	}
	step("pause", jobs.StatusPaused)
	if ref.Registry[pub].Status != "paused" {
		t.Errorf("registry after pause: %s", ref.Registry[pub].Status)
	}

	// Resuming re-runs the checks: a company that lost verification cannot resume.
	co.state.Verified = false
	if _, err := s.Transition(ctx, hr1, v.ID, "resume", 0); !blockedCodes(t, err)[gates.CodeCompanyNotVerified] {
		t.Errorf("resume without verification: %v", err)
	}
	co.state.Verified = true
	step("resume", jobs.StatusPublished)
	if ref.Registry[pub].Status != "published" {
		t.Errorf("registry after resume: %s", ref.Registry[pub].Status)
	}
	if st.Jobs[drafttest.Key(org, v.ID)].FormVersion != 2 {
		t.Error("every publish freezes a new form version")
	}
	step("close", jobs.StatusClosed)
	step("reopen", jobs.StatusPublished)
	step("close", jobs.StatusClosed)
	step("archive", jobs.StatusArchived)
	if _, err := s.Transition(ctx, hr1, v.ID, "reopen", 0); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("archived reopen: %v", err)
	}
	if _, err := s.Transition(ctx, hr1, v.ID, "expire", 0); !isInvalid(err) {
		t.Errorf("expire is system only: %v", err)
	}
}

func isInvalid(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

func TestDraftCannotBePaused(t *testing.T) {
	s, _, _, _ := publishEnv(t)
	hr1 := caller(userA, rbac.RoleHR1)
	v := newDraft(t, s, hr1, `{}`)
	if _, err := s.Transition(context.Background(), hr1, v.ID, "pause", 0); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("pause draft: %v", err)
	}
}

func TestHR2ChangesStatusOnlyOnOwnJobs(t *testing.T) {
	s, _, _, _ := publishEnv(t)
	ctx := context.Background()
	hr1, hr2 := caller(userA, rbac.RoleHR1), caller(userB, rbac.RoleHR2)
	v := readyJob(t, s, hr1)
	if _, err := s.Publish(ctx, hr1, v.ID, 0); err != nil {
		t.Fatal(err)
	}
	// HR 2 sees all jobs (job.view.all) but cannot pause HR 1's.
	if _, err := s.Transition(ctx, hr2, v.ID, "pause", 0); !isForbidden(err) {
		t.Errorf("hr2 pause of another's job: %v", err)
	}
	// Line managers can do nothing here, and a payroll user cannot even see it.
	if _, err := s.Transition(ctx, caller(userC, rbac.RolePayroll), v.ID, "pause", 0); !errors.Is(err, hiringtypes.ErrNotFound) {
		t.Errorf("payroll: %v", err)
	}
}

func TestConfirmGateRules(t *testing.T) {
	s, _, _, _ := publishEnv(t)
	ctx := context.Background()
	hr1 := caller(userA, rbac.RoleHR1)
	v := newDraft(t, s, hr1, publishable)

	if _, err := s.ConfirmGate(ctx, hr1, v.ID, "nope", true, 0); !isInvalid(err) {
		t.Errorf("unknown gate: %v", err)
	}
	if _, err := s.ConfirmGate(ctx, caller(userB, rbac.RoleHR2), v.ID, "uk-lawful", true, 0); !isForbidden(err) {
		t.Errorf("hr2 confirm: %v", err)
	}
	states, err := s.ConfirmGate(ctx, hr1, v.ID, "uk-lawful", true, 0)
	if err != nil || !stateOf(states, "uk-lawful").Confirmed {
		t.Fatalf("confirm: %v %+v", err, states)
	}
	states, err = s.ConfirmGate(ctx, hr1, v.ID, "uk-lawful", false, 0)
	if err != nil || stateOf(states, "uk-lawful").Confirmed {
		t.Fatalf("withdraw: %v %+v", err, states)
	}
}

func stateOf(states []gates.State, id string) gates.State {
	for _, s := range states {
		if s.ID == id {
			return s
		}
	}
	return gates.State{}
}

func TestConfirmingAutoGateIsRefused(t *testing.T) {
	s, _, ref, _ := publishEnv(t)
	ref.GateList = append(ref.GateList, gates.Gate{ID: "us-salary-history", MarketCode: "UK", Severity: gates.SeverityRequired,
		Version: 1, Trigger: json.RawMessage(`{"type":"always"}`)})
	hr1 := caller(userA, rbac.RoleHR1)
	v := newDraft(t, s, hr1, publishable)
	if _, err := s.ConfirmGate(context.Background(), hr1, v.ID, "us-salary-history", true, 0); !isInvalid(err) {
		t.Errorf("auto gate: %v", err)
	}
}

// ---- hiring team ----

func TestMembersFullAndLimited(t *testing.T) {
	s, st, _, _ := publishEnv(t)
	ctx := context.Background()
	st.People[userB] = hiringtypes.Member{UserID: userB, Name: "Bea", Email: "bea@acme.test"}
	st.People[userC] = hiringtypes.Member{UserID: userC, Name: "Cal", Email: "cal@acme.test"}
	hr1 := caller(userA, rbac.RoleHR1)
	v := newDraft(t, s, hr1, `{"title":"Cook"}`)

	got, err := s.SetMembers(ctx, hr1, v.ID, 0, []MemberInput{{userB, "hiring_manager"}, {userC, "viewer"}})
	if err != nil || len(got) != 2 || got[0].Name != "Bea" {
		t.Fatalf("set: %v %+v", err, got)
	}

	// Validation.
	for name, in := range map[string][]MemberInput{
		"creator":   {{userA, "viewer"}},
		"bad role":  {{userB, "boss"}},
		"duplicate": {{userB, "viewer"}, {userB, "viewer"}},
		"stranger":  {{"cccccccc-0000-4000-8000-000000000009", "viewer"}},
	} {
		if _, err := s.SetMembers(ctx, hr1, v.ID, 0, in); !isInvalid(err) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// HR 2 owns a job of its own and may add viewers there, but cannot touch other roles.
	hr2 := caller(userB, rbac.RoleHR2)
	mine := newDraft(t, s, hr2, `{"title":"Chef"}`)
	if _, err := s.SetMembers(ctx, hr2, mine.ID, 0, []MemberInput{{userC, "viewer"}}); err != nil {
		t.Fatalf("limited viewer add: %v", err)
	}
	if _, err := s.SetMembers(ctx, hr2, mine.ID, 0, []MemberInput{{userC, "hiring_manager"}}); !isForbidden(err) {
		t.Errorf("limited role change: %v", err)
	}
	if _, err := s.SetMembers(ctx, hr2, v.ID, 0, []MemberInput{{userC, "viewer"}}); !isForbidden(err) {
		t.Errorf("limited on another's job: %v", err)
	}
	// Line manager cannot manage the team.
	if _, err := s.SetMembers(ctx, caller(userC, rbac.RoleLineManager), v.ID, 0, nil); !isForbidden(err) {
		t.Errorf("line manager: %v", err)
	}
}

func TestMemberGainsVisibilityOnly(t *testing.T) {
	s, st, _, _ := publishEnv(t)
	ctx := context.Background()
	st.People[userC] = hiringtypes.Member{UserID: userC, Name: "Cal"}
	hr1, lm := caller(userA, rbac.RoleHR1), caller(userC, rbac.RoleLineManager)
	v := newDraft(t, s, hr1, `{"title":"Cook"}`)

	if _, err := s.Get(ctx, lm, v.ID); !errors.Is(err, hiringtypes.ErrNotFound) {
		t.Fatalf("before: %v", err)
	}
	if _, err := s.SetMembers(ctx, hr1, v.ID, 0, []MemberInput{{userC, "hiring_manager"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, lm, v.ID); err != nil {
		t.Errorf("a team member sees the job: %v", err)
	}
	if _, err := s.Save(ctx, lm, v.ID, 0, patch(t, `{"title":"X"}`), false); !isForbidden(err) {
		t.Errorf("membership must not grant edit rights: %v", err)
	}
	items, _, err := s.List(ctx, lm, ListQuery{})
	if err != nil || len(items) != 1 {
		t.Errorf("list: %v %+v", err, items)
	}
	// The access list is for people who may see it.
	if _, err := s.Members(ctx, lm, v.ID); err != nil {
		t.Errorf("line managers hold job.access.view_list: %v", err)
	}
	if _, err := s.Members(ctx, caller(userB, rbac.RoleEmployee), v.ID); !isForbidden(err) {
		t.Errorf("employee: %v", err)
	}
}

// ---- screening rules ----

func TestRulesNeedScreeningEnabledAndMatchTheForm(t *testing.T) {
	s, st, _, _ := publishEnv(t)
	ctx := context.Background()
	hr1 := caller(userA, rbac.RoleHR1)
	v := newDraft(t, s, hr1, publishable)
	form, err := s.GetForm(ctx, hr1, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Add a yes/no question through the form builder (screening enabled only for the form step).
	st.Screening = true
	in := FormInput{Questions: append(form.Questions, questionYesNo("Do you need sponsorship?"))}
	saved, err := s.SaveForm(ctx, hr1, v.ID, 0, in)
	if err != nil {
		t.Fatalf("save form: %v", err)
	}
	qid := saved.Questions[len(saved.Questions)-1].ID

	st.Screening = false
	rule := screening.Rule{QuestionID: qid, Operator: screening.OpIn, Value: json.RawMessage(`["yes"]`), Reason: "Needs sponsorship"}
	if _, err = s.SetRules(ctx, hr1, v.ID, 0, RulesInput{Rules: []screening.Rule{rule}}); !isInvalid(err) {
		t.Fatalf("screening off: %v", err)
	}
	st.Screening = true
	bad := rule
	bad.Value = json.RawMessage(`["maybe"]`)
	if _, err = s.SetRules(ctx, hr1, v.ID, 0, RulesInput{Rules: []screening.Rule{bad}}); !isInvalid(err) {
		t.Fatalf("bad option: %v", err)
	}
	view, err := s.SetRules(ctx, hr1, v.ID, 0, RulesInput{Rules: []screening.Rule{rule}})
	if err != nil || len(view.Rules) != 1 || view.Rules[0].ID == "" || view.DPIAScopeConfirmed {
		t.Fatalf("set: %v %+v", err, view)
	}
	yes := true
	view, err = s.SetRules(ctx, hr1, v.ID, 0, RulesInput{Rules: view.Rules, DPIAScopeConfirmed: &yes})
	if err != nil || !view.DPIAScopeConfirmed {
		t.Fatalf("confirm scope: %v %+v", err, view)
	}
	// Changing the rules clears the confirmation.
	changed := view.Rules
	changed[0].Reason = "Sponsorship needed"
	view, err = s.SetRules(ctx, hr1, v.ID, 0, RulesInput{Rules: changed})
	if err != nil || view.DPIAScopeConfirmed {
		t.Fatalf("changed rules must clear scope: %v %+v", err, view)
	}
}

func TestPublishNeedsScreeningPrerequisites(t *testing.T) {
	s, st, _, co := publishEnv(t)
	ctx := context.Background()
	hr1 := caller(userA, rbac.RoleHR1)
	v := newDraft(t, s, hr1, publishable)
	st.Screening = true
	form, _ := s.GetForm(ctx, hr1, v.ID)
	saved, err := s.SaveForm(ctx, hr1, v.ID, 0, FormInput{Questions: append(form.Questions, questionYesNo("Sponsorship?"))})
	if err != nil {
		t.Fatal(err)
	}
	qid := saved.Questions[len(saved.Questions)-1].ID
	if _, err = s.SetRules(ctx, hr1, v.ID, 0, RulesInput{Rules: []screening.Rule{
		{QuestionID: qid, Operator: screening.OpIn, Value: json.RawMessage(`["yes"]`), Reason: "r"}}}); err != nil {
		t.Fatal(err)
	}
	confirmUK(t, s, hr1, v.ID)

	_, err = s.Publish(ctx, hr1, v.ID, 0)
	if !blockedCodes(t, err)[gates.CodeDPIAMissing] {
		t.Errorf("no DPIA: %v", err)
	}
	co.state.DPIARecorded = true
	_, err = s.Publish(ctx, hr1, v.ID, 0)
	if !blockedCodes(t, err)[gates.CodeScreeningOutOfScope] {
		t.Errorf("scope: %v", err)
	}
	yes := true
	view, _ := s.Rules(ctx, hr1, v.ID)
	if _, err = s.SetRules(ctx, hr1, v.ID, 0, RulesInput{Rules: view.Rules, DPIAScopeConfirmed: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(ctx, hr1, v.ID, 0); err != nil {
		t.Errorf("publish with every prerequisite: %v", err)
	}
	// Legal switching screening off afterwards blocks the next resume.
	st.Screening = false
	if _, err = s.Transition(ctx, hr1, v.ID, "pause", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Transition(ctx, hr1, v.ID, "resume", 0); !blockedCodes(t, err)[gates.CodeScreeningDisabled] {
		t.Errorf("resume with screening off: %v", err)
	}
}

// ---- bulk, export, duplicate ----

func TestBulkPause(t *testing.T) {
	s, _, _, _ := publishEnv(t)
	ctx := context.Background()
	hr1 := caller(userA, rbac.RoleHR1)
	a, b := readyJob(t, s, hr1), readyJob(t, s, hr1)
	if _, err := s.Publish(ctx, hr1, a.ID, 0); err != nil {
		t.Fatal(err)
	}
	// b stays a draft: pausing it is not allowed, but the rest of the selection still goes through.
	res, err := s.Bulk(ctx, hr1, "pause", []string{a.ID, b.ID, "00000000-0000-4000-8000-0000000000ff"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Done) != 1 || res.Done[0].ID != a.ID || res.Done[0].Before != "published" || res.Done[0].After != "paused" {
		t.Errorf("done = %+v", res.Done)
	}
	if len(res.Skipped) != 2 {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	if _, err = s.Bulk(ctx, hr1, "publish", []string{a.ID}); !isInvalid(err) {
		t.Errorf("publish in bulk: %v", err)
	}
	if _, err = s.Bulk(ctx, hr1, "pause", nil); !isInvalid(err) {
		t.Errorf("empty selection: %v", err)
	}
}

func TestExportNeedsPermission(t *testing.T) {
	s, _, _, _ := publishEnv(t)
	ctx := context.Background()
	hr1 := caller(userA, rbac.RoleHR1)
	newDraft(t, s, hr1, `{"title":"Cook"}`)
	if _, err := s.Export(ctx, caller(userB, rbac.RoleHR2), nil); !isForbidden(err) {
		t.Errorf("hr2 export: %v", err)
	}
	rows, err := s.Export(ctx, hr1, nil)
	if err != nil || len(rows) != 1 {
		t.Errorf("export: %v %+v", err, rows)
	}
}

func TestDuplicateCopiesDetailsNotState(t *testing.T) {
	s, st, _, _ := publishEnv(t)
	ctx := context.Background()
	hr1 := caller(userA, rbac.RoleHR1)
	v := readyJob(t, s, hr1)
	if _, err := s.Publish(ctx, hr1, v.ID, 0); err != nil {
		t.Fatal(err)
	}
	cp, err := s.Duplicate(ctx, hr1, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID == v.ID || cp.Status != jobs.StatusDraft || cp.PublicID != "" || cp.Title != v.Title {
		t.Errorf("copy = %+v", cp.Job)
	}
	rec := st.Jobs[drafttest.Key(org, cp.ID)]
	if len(rec.Confirmations) != 0 || len(rec.Members) != 0 || rec.FormVersion != 0 || len(rec.Form) == 0 {
		t.Errorf("copy state: %+v", rec)
	}
	if _, err = s.Duplicate(ctx, caller(userC, rbac.RoleEmployee), v.ID); !isForbidden(err) {
		t.Errorf("employee duplicate: %v", err)
	}
}

func questionYesNo(label string) questions.FormQuestion {
	return questions.FormQuestion{Section: "screening", Type: "single_choice", Label: label, Required: true,
		Config: json.RawMessage(yesNoConfig)}
}
