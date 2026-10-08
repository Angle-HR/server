package draft

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/drafttest"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/rbac"
)

const dept = "44444444-4444-4444-8444-444444444444"

func patch(t *testing.T, raw string) jobs.Patch {
	t.Helper()
	p, err := jobs.DecodePatch([]byte(raw))
	if err != nil {
		t.Fatalf("bad patch %s: %v", raw, err)
	}
	return p
}

func fields(err error) []string {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return nil
	}
	var out []string
	for _, f := range ve.Fields {
		out = append(out, f.Path)
	}
	return out
}

func wantField(t *testing.T, err error, path string) {
	t.Helper()
	for _, p := range fields(err) {
		if p == path {
			return
		}
	}
	t.Fatalf("want a validation error at %q, got %v (fields %v)", path, err, fields(err))
}

const yesNoConfig = `{"options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}`

func knockoutQuestion(label string) questions.FormQuestion {
	return questions.FormQuestion{Section: "screening", Type: "single_choice", Label: label, Required: true,
		Config: json.RawMessage(yesNoConfig), Knockout: json.RawMessage(`{"option_ids":["no"]}`)}
}

const noPayDetails = `{
 "title":"Senior Go Engineer","department_id":"` + dept + `","location_mode":"specific_area",
 "markets":[{"market_code":"UK"}],"travel_frequency":"none","visa_sponsorship":"no","employment_type":"full_time"}`

const fullDetails = `{
 "title":"Senior Go Engineer","department_id":"` + dept + `","location_mode":"specific_area",
 "markets":[{"market_code":"UK"}],"travel_frequency":"none","visa_sponsorship":"no","employment_type":"full_time",
 "pay":{"type":"range","currency":"GBP","period":"year","min":50000,"max":70000,"visible":true}}`

func newDraft(t *testing.T, s *Service, c Caller, raw string) *JobView {
	t.Helper()
	v, err := s.Create(context.Background(), c, CreateRequest{Patch: patch(t, raw)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return v
}

func TestCreateNeedsPermissionAndStartsWithStandardForm(t *testing.T) {
	s, st := newService()
	if _, err := s.Create(context.Background(), caller(userA, rbac.RoleEmployee), CreateRequest{}); !isForbidden(err) {
		t.Fatalf("employee create: %v", err)
	}
	v := newDraft(t, s, caller(userA, rbac.RoleFounder), `{"title":"Cook"}`)
	if v.Status != jobs.StatusDraft || v.CurrentStep != jobs.StepDetails || v.Revision != 1 || v.JobCode != "JB-1" {
		t.Fatalf("unexpected draft: %+v", v.Job)
	}
	rec, _ := st.GetJob(context.Background(), org, v.ID)
	if len(rec.Form) != len(questions.DefaultForm()) {
		t.Fatalf("form has %d questions", len(rec.Form))
	}
	if v.RetentionMonths != jobs.DefaultRetentionMonths || !v.ShowOnCareerPage {
		t.Fatalf("defaults wrong: %+v", v.Details)
	}
}

func isForbidden(err error) bool {
	var fe *ForbiddenError
	return errors.As(err, &fe)
}

func TestSaveAsDraftIsLenientButChecksFormat(t *testing.T) {
	s, _ := newService()
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, `{}`)
	got, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{"title":"Cook"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Cook" || got.Revision != 2 || got.CurrentStep != jobs.StepDetails {
		t.Fatalf("unexpected: %+v", got.Job)
	}
	_, err = s.Save(context.Background(), c, v.ID, 0, patch(t, `{"title":"<script>"}`), false)
	wantField(t, err, "title")
}

func TestSaveAndContinueNeedsRequiredFieldsThenAdvances(t *testing.T) {
	s, _ := newService()
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, `{"title":"Cook"}`)
	_, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{}`), true)
	for _, p := range []string{"department_id", "location_mode", "travel_frequency", "visa_sponsorship", "employment_type", "pay.type"} {
		wantField(t, err, p)
	}
	got, err := s.Save(context.Background(), c, v.ID, 0, patch(t, fullDetails), true)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentStep != jobs.StepApplicationForm {
		t.Fatalf("step = %s", got.CurrentStep)
	}
	for _, sec := range []string{"basics", "markets", "compensation"} {
		if !has(got.CompletedSections, sec) {
			t.Fatalf("missing section %s in %v", sec, got.CompletedSections)
		}
	}
}

func TestPayIsOptionalForRolesThatCannotSetIt(t *testing.T) {
	s, _ := newService()
	hr := Caller{UserID: userA, OrgID: org, Perms: rbac.NewSet(rbac.JobCreate, rbac.JobEditDraft, rbac.JobViewAll)}
	v := newDraft(t, s, hr, `{}`)
	noPay := strings.Replace(fullDetails, `,
 "pay":{"type":"range","currency":"GBP","period":"year","min":50000,"max":70000,"visible":true}`, "", 1)
	if _, err := s.Save(context.Background(), hr, v.ID, 0, patch(t, noPay), true); err != nil {
		t.Fatalf("strict save without pay: %v", err)
	}
	_, err := s.Save(context.Background(), hr, v.ID, 0, patch(t, `{"pay":{"type":"exact"}}`), false)
	if !isForbidden(err) {
		t.Fatalf("pay without permission: %v", err)
	}
	_, err = s.Save(context.Background(), hr, v.ID, 0, patch(t, `{"retention_months":12}`), false)
	if !isForbidden(err) {
		t.Fatalf("retention without permission: %v", err)
	}
}

func TestMarketRules(t *testing.T) {
	s, _ := newService()
	c := caller(userA, rbac.RoleFounder)
	c.Perms[rbac.JobRetentionSet] = struct{}{}
	v := newDraft(t, s, c, `{"location_mode":"specific_area"}`)

	_, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{"markets":[{"market_code":"NG"}]}`), false)
	wantField(t, err, "markets[0].market_code")

	got, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{"markets":[{"market_code":"us","subdivision":"ca"}]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Markets[0].MarketCode != "US" || got.Markets[0].Subdivision != "CA" {
		t.Fatalf("markets not normalised: %+v", got.Markets)
	}
	if !hasWarning(got.Warnings, "us_pay_transparency") {
		t.Fatalf("US warning missing: %+v", got.Warnings)
	}

	got, err = s.Save(context.Background(), c, v.ID, 0, patch(t, `{"markets":[{"market_code":"KE"}]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionMonths != 12 {
		t.Fatalf("retention = %d, want bump to 12 for Kenya", got.RetentionMonths)
	}
	_, err = s.Save(context.Background(), c, v.ID, 0, patch(t, `{"retention_months":6}`), false)
	wantField(t, err, "retention_months")

	got, err = s.Save(context.Background(), c, v.ID, 0, patch(t, `{"location_mode":"anywhere"}`), false)
	if err != nil {
		t.Fatalf("switching to anywhere: %v", err)
	}
	if len(got.Markets) != 0 {
		t.Fatalf("anywhere kept markets: %+v", got.Markets)
	}
}

func hasWarning(ws []jobs.Warning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestStaleRevisionAndNoOpSave(t *testing.T) {
	s, _ := newService()
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, `{"title":"Cook"}`)
	_, err := s.Save(context.Background(), c, v.ID, 99, patch(t, `{"title":"Chef"}`), false)
	var sr *hiringtypes.StaleRevisionError
	if !errors.As(err, &sr) || sr.Current != 1 {
		t.Fatalf("want stale revision 1, got %v", err)
	}
	got, err := s.Save(context.Background(), c, v.ID, 1, patch(t, `{"title":"Cook"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 {
		t.Fatalf("a save that changes nothing bumped the revision to %d", got.Revision)
	}
}

func TestAccessRules(t *testing.T) {
	s, _ := newService()
	owner := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, owner, `{"title":"Cook"}`)

	assigned := Caller{UserID: userB, OrgID: org, Perms: rbac.NewSet(rbac.JobViewAssigned)}
	if _, err := s.Get(context.Background(), assigned, v.ID); !errors.Is(err, hiringtypes.ErrNotFound) {
		t.Fatalf("assigned-only user saw another's job: %v", err)
	}
	if _, err := s.Get(context.Background(), assigned, "not-a-uuid"); !errors.Is(err, hiringtypes.ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	items, _, err := s.List(context.Background(), assigned, ListQuery{})
	if err != nil || len(items) != 0 {
		t.Fatalf("assigned list: %v %v", items, err)
	}
	items, _, _ = s.List(context.Background(), owner, ListQuery{})
	if len(items) != 1 {
		t.Fatalf("owner list: %v", items)
	}

	viewer := Caller{UserID: userB, OrgID: org, Perms: rbac.NewSet(rbac.JobViewAll, rbac.JobEditDraft)}
	if _, err := s.Get(context.Background(), viewer, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), viewer, v.ID, 0, patch(t, `{"title":"Chef"}`), false); !isForbidden(err) {
		t.Fatalf("editing someone else's draft without job.edit.published: %v", err)
	}
	other := Caller{UserID: userB, OrgID: "bbbbbbbb-0000-4000-8000-0000000000ff", Perms: rbac.NewSet(rbac.JobViewAll)}
	if _, err := s.Get(context.Background(), other, v.ID); !errors.Is(err, hiringtypes.ErrNotFound) {
		t.Fatalf("another company saw the job: %v", err)
	}
	if _, _, err := s.List(context.Background(), caller(userB, rbac.RoleEmployee), ListQuery{}); !isForbidden(err) {
		t.Fatalf("employee list: %v", err)
	}
	_, _, err = s.List(context.Background(), owner, ListQuery{Statuses: []string{"nope"}, Cursor: "x"})
	wantField(t, err, "status")
	wantField(t, err, "cursor")
}

func TestOnlyDraftsAreEditableAndDeletable(t *testing.T) {
	s, st := newService()
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, `{"title":"Cook"}`)
	st.Jobs[drafttest.Key(org, v.ID)].Job.Status = jobs.StatusPublished
	if _, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{"title":"Chef"}`), false); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("save published: %v", err)
	}
	if err := s.Delete(context.Background(), c, v.ID, 0); !errors.Is(err, hiringtypes.ErrNotDraft) {
		t.Fatalf("delete published: %v", err)
	}
	st.Jobs[drafttest.Key(org, v.ID)].Job.Status = jobs.StatusDraft
	if err := s.Delete(context.Background(), c, v.ID, 0); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceChecks(t *testing.T) {
	s, _ := newService()
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, `{}`)
	_, err := s.Save(context.Background(), c, v.ID, 0, patch(t,
		`{"skills":[{"skill_id":"55555555-5555-4555-8555-555555555555"}],"industry_id":"66666666-6666-4666-8666-666666666666"}`), false)
	wantField(t, err, "skills")
	wantField(t, err, "industry_id")

	got, err := s.Save(context.Background(), c, v.ID, 0, patch(t,
		`{"skills":[{"skill_id":"`+skillGo+`"},{"custom_label":"Kubernetes"}],"industry_id":"`+indTech+`"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Skills[0].Name != "Go" || got.Skills[1].Name != "Kubernetes" {
		t.Fatalf("skill names not resolved: %+v", got.Skills)
	}
}

func TestDuplicateWarningOnlyShowsJobsTheCallerMaySee(t *testing.T) {
	s, _ := newService()
	a, b := caller(userA, rbac.RoleFounder), caller(userB, rbac.RoleHR2)
	newDraft(t, s, a, noPayDetails)
	v2 := newDraft(t, s, b, noPayDetails)
	if !hasWarning(v2.Warnings, "duplicate_job") {
		t.Fatalf("HR (view.all) should be warned: %+v", v2.Warnings)
	}
	onlyOwn := Caller{UserID: userC, OrgID: org, Perms: rbac.NewSet(rbac.JobCreate, rbac.JobEditDraft, rbac.JobViewAssigned)}
	v3 := newDraft(t, s, onlyOwn, noPayDetails)
	if hasWarning(v3.Warnings, "duplicate_job") {
		t.Fatalf("assigned-only caller must not learn about another user's job: %+v", v3.Warnings)
	}
}

func TestFormRules(t *testing.T) {
	s, st := newService()
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, fullDetails)
	form := questions.DefaultForm()
	form = append(form, knockoutQuestion("Can you work in the UK?"))

	_, err := s.SaveForm(context.Background(), c, v.ID, 0, FormInput{Questions: form})
	if len(fields(err)) == 0 {
		t.Fatalf("knockout accepted while automated screening is off: %v", err)
	}

	decl := []questions.Declaration{{Category: "health_disability", LegalCondition: "explicit_consent", Purpose: "Adjustments"}}
	_, err = s.SaveForm(context.Background(), c, v.ID, 0, FormInput{Questions: questions.DefaultForm(), Declarations: decl})
	wantField(t, err, "declarations")

	got, err := s.SaveForm(context.Background(), c, v.ID, 0, FormInput{Questions: questions.DefaultForm()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Questions) != len(questions.DefaultForm()) || got.AutomatedScreeningEnabled {
		t.Fatalf("form view: %+v", got)
	}
	rec, _ := st.GetJob(context.Background(), org, v.ID)
	if !has(rec.Job.CompletedSections, jobs.SectionScreening) || rec.Job.CurrentStep != jobs.StepPermissions {
		t.Fatalf("after form save: step %s sections %v", rec.Job.CurrentStep, rec.Job.CompletedSections)
	}

	st.Screening = true
	if _, err := s.SaveForm(context.Background(), c, v.ID, 0, FormInput{Questions: form}); err != nil {
		t.Fatalf("knockout with screening on: %v", err)
	}
}

func TestFormEditsDoNotLoseDetailsAndDeclarationsNeedLawfulBasis(t *testing.T) {
	s, _ := newService()
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, fullDetails)
	if _, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{"lawful_basis":"legitimate_interests"}`), false); err != nil {
		t.Fatal(err)
	}
	form := append(questions.DefaultForm(), questions.FormQuestion{Section: "eligibility", Type: "single_choice",
		Label: "Do you have a disability?", SpecialCategory: "health_disability",
		Config: json.RawMessage(yesNoConfig)})
	decl := []questions.Declaration{{Category: "health_disability", LegalCondition: "explicit_consent", Purpose: "Adjustments"}}
	if _, err := s.SaveForm(context.Background(), c, v.ID, 0, FormInput{Questions: form, Declarations: decl}); err != nil {
		t.Fatalf("form with declaration: %v %v", err, fields(err))
	}
	// Removing the lawful basis while a declaration exists must be refused.
	_, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{"lawful_basis":""}`), false)
	wantField(t, err, "lawful_basis")
}

func TestTemplatesCarryTheSetupButNotTheTitleOrUnauthorisedPay(t *testing.T) {
	s, _ := newService()
	founder := caller(userA, rbac.RoleFounder)
	src := newDraft(t, s, founder, fullDetails)

	tpl, err := s.SaveTemplate(context.Background(), founder, TemplateInput{Kind: "job_details", FromJob: src.ID, AsMyDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(tpl.Payload, &payload)
	if _, ok := payload["title"]; ok {
		t.Fatal("template payload carries the title")
	}

	fresh := newDraft(t, s, founder, `{"title":"Cook"}`)
	if fresh.Title != "Cook" || fresh.EmploymentType != "full_time" || fresh.Pay.Type != "range" {
		t.Fatalf("default template not applied: %+v", fresh.Details)
	}

	// A caller who cannot set pay starts from the same template without the pay.
	hr := Caller{UserID: userA, OrgID: org, Perms: rbac.NewSet(rbac.JobCreate, rbac.JobEditDraft, rbac.JobViewAll)}
	noPay := newDraft(t, s, hr, `{"title":"Barista"}`)
	if noPay.Pay.Type != "" {
		t.Fatalf("pay leaked through a template: %+v", noPay.Pay)
	}

	// Named company templates need job.template.create, and names are unique.
	if _, err := s.SaveTemplate(context.Background(), hr, TemplateInput{Kind: "job_details", Name: "Eng", FromJob: src.ID}); !isForbidden(err) {
		t.Fatalf("template without permission: %v", err)
	}
	if _, err := s.SaveTemplate(context.Background(), founder, TemplateInput{Kind: "job_details", Name: "Eng", FromJob: src.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveTemplate(context.Background(), founder, TemplateInput{Kind: "job_details", Name: "eng", FromJob: src.ID})
	wantField(t, err, "name")
	_, err = s.SaveTemplate(context.Background(), founder, TemplateInput{Kind: "x", FromJob: "nope"})
	wantField(t, err, "kind")
	wantField(t, err, "from_job_id")
}

func TestFormTemplateRoundTripDropsQuestionIDs(t *testing.T) {
	s, st := newService()
	c := caller(userA, rbac.RoleFounder)
	src := newDraft(t, s, c, fullDetails)
	form := append(questions.DefaultForm(), questions.FormQuestion{Section: "profile", Type: "short_text", Label: "Portfolio link", Config: json.RawMessage(`{}`)})
	if _, err := s.SaveForm(context.Background(), c, src.ID, 0, FormInput{Questions: form}); err != nil {
		t.Fatal(err)
	}
	tpl, err := s.SaveTemplate(context.Background(), c, TemplateInput{Kind: "application_form", Name: "Standard+", FromJob: src.ID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tpl.Payload), `"id":"00000000`) {
		t.Fatalf("template payload kept question ids: %s", tpl.Payload)
	}
	id := tpl.ID
	v, err := s.Create(context.Background(), c, CreateRequest{FormTemplateID: &id})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := st.GetJob(context.Background(), org, v.ID)
	if len(rec.Form) != len(form) {
		t.Fatalf("form from template has %d questions, want %d", len(rec.Form), len(form))
	}
	bad := "00000000-0000-4000-8000-00000000ffff"
	if _, err := s.Create(context.Background(), c, CreateRequest{FormTemplateID: &bad}); err == nil {
		t.Fatal("unknown form template accepted")
	}
}

func TestSettingsOnlyLegalCanChange(t *testing.T) {
	s, _ := newService()
	if _, err := s.SetAutomatedScreening(context.Background(), caller(userA, rbac.RoleFounder), true); !isForbidden(err) {
		t.Fatalf("founder: %v", err)
	}
	got, err := s.SetAutomatedScreening(context.Background(), caller(userB, rbac.RoleLegal), true)
	if err != nil || !got.AutomatedScreeningEnabled {
		t.Fatalf("legal: %v %v", got, err)
	}
}

func TestDepartments(t *testing.T) {
	s, _ := newService()
	c := caller(userA, rbac.RoleFounder)
	d1, err := s.AddDepartment(context.Background(), c, "  Customer   Support ")
	if err != nil || d1.Name != "Customer Support" {
		t.Fatalf("%v %v", d1, err)
	}
	d2, _ := s.AddDepartment(context.Background(), c, "customer support")
	if d2.ID != d1.ID {
		t.Fatal("same name created twice")
	}
	if _, err := s.AddDepartment(context.Background(), c, "   "); err == nil {
		t.Fatal("blank name accepted")
	}
	if _, err := s.AddDepartment(context.Background(), caller(userA, rbac.RoleEmployee), "X"); !isForbidden(err) {
		t.Fatalf("employee: %v", err)
	}
}

func TestPreviewHidesKnockoutAndShowsPayOnlyWhenVisible(t *testing.T) {
	s, st := newService()
	st.Screening = true
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, fullDetails)
	form := append(questions.DefaultForm(), knockoutQuestion("Right to work?"))
	if _, err := s.SaveForm(context.Background(), c, v.ID, 0, FormInput{Questions: form}); err != nil {
		t.Fatal(err)
	}
	pv, err := s.Preview(context.Background(), c, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(pv)
	if strings.Contains(string(raw), "knockout") || strings.Contains(string(raw), "lawful") || strings.Contains(string(raw), "retention") {
		t.Fatalf("preview leaks internal fields: %s", raw)
	}
	if pv.Company != "Acme Ltd" || pv.Pay == nil || pv.Pay.Min != 50000 {
		t.Fatalf("preview: %+v", pv)
	}
	if _, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{"pay":{"visible":false}}`), false); err != nil {
		t.Fatal(err)
	}
	pv, _ = s.Preview(context.Background(), c, v.ID)
	if pv.Pay != nil {
		t.Fatal("hidden pay shown in preview")
	}
}

func TestPayIsRedactedForReadersWithoutPayPermissions(t *testing.T) {
	s, _ := newService()
	v := newDraft(t, s, caller(userA, rbac.RoleFounder), fullDetails)
	reader := Caller{UserID: userB, OrgID: org, Perms: rbac.NewSet(rbac.JobViewAll)}
	got, err := s.Get(context.Background(), reader, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pay.Min != nil || got.Pay.Type != "" {
		t.Fatalf("pay visible to a reader without pay permission: %+v", got.Pay)
	}
}

func TestCatalogAndDuplicateAcknowledge(t *testing.T) {
	s, st := newService()
	cat, err := s.Catalog(context.Background())
	if err != nil || len(cat.Markets) != 4 || len(cat.Currencies) != 6 {
		t.Fatalf("catalog: %v %+v", err, cat)
	}
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, `{"title":"Cook"}`)
	if err := s.AcknowledgeDuplicate(context.Background(), c, v.ID); err != nil {
		t.Fatal(err)
	}
	if st.Audits[len(st.Audits)-1] != "job.duplicate_acknowledged" {
		t.Fatalf("audits: %v", st.Audits)
	}
}

func TestOldClosingDateDoesNotBlockOtherEdits(t *testing.T) {
	s, st := newService()
	c := caller(userA, rbac.RoleFounder)
	v := newDraft(t, s, c, `{"title":"Cook","closing_date":"2026-12-01"}`)
	st.Jobs[drafttest.Key(org, v.ID)].Job.ClosingDate = "2026-01-01" // time passed
	if _, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{"title":"Chef"}`), false); err != nil {
		t.Fatalf("stale closing date blocked an unrelated edit: %v", err)
	}
	_, err := s.Save(context.Background(), c, v.ID, 0, patch(t, `{"closing_date":"2026-02-01"}`), false)
	wantField(t, err, "closing_date")
}
