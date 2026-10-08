package draft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/rbac"
)

// CreateRequest starts a draft. Every field is optional: the page can create an empty draft on first save.
type CreateRequest struct {
	jobs.Patch
	// TemplateID starts the draft from a saved job-details template instead of the caller's default.
	TemplateID *string `json:"template_id"`
	// FormTemplateID starts the application form from a saved form template.
	FormTemplateID *string `json:"form_template_id"`
}

var stepOrder = map[string]int{
	jobs.StepDetails: 0, jobs.StepApplicationForm: 1, jobs.StepPermissions: 2, jobs.StepShare: 3,
}

func laterStep(a, b string) string {
	if stepOrder[b] > stepOrder[a] {
		return b
	}
	return a
}

func has(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// keepSections are the completed sections the details step does not decide.
func keepSections(cur []string) []string {
	var out []string
	for _, s := range cur {
		if s == jobs.SectionScreening || s == jobs.SectionDocuments {
			out = append(out, s)
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// guardPatch checks the permissions a patch needs.
func guardPatch(c Caller, p jobs.Patch) error {
	if p.TouchesPay() && !c.can(rbac.JobSalaryRangeSet) {
		return forbidden("you cannot set pay on a job")
	}
	if p.TouchesRetention() && !c.can(rbac.JobRetentionSet) {
		return forbidden("you cannot change how long applicant data is kept")
	}
	return nil
}

// ---- catalog references ----

// refIDs collects the catalog ids a patch points at. A malformed id is reported straight away.
func refIDs(p jobs.Patch) (skills []string, industry, seniority, experience string, bad *jobs.FieldError) {
	if p.Skills != nil {
		for i, sk := range *p.Skills {
			if sk.SkillID == "" {
				continue
			}
			if !jobs.IsUUID(sk.SkillID) {
				e := fe(fmt.Sprintf("skills[%d].skill_id", i), "invalid skill")
				return nil, "", "", "", &e
			}
			skills = append(skills, sk.SkillID)
		}
	}
	pick := func(v *string) string {
		if v == nil || !jobs.IsUUID(*v) {
			return ""
		}
		return *v
	}
	return skills, pick(p.IndustryID), pick(p.SeniorityLevelID), pick(p.ExperienceRangeID), nil
}

func refErrors(rc *hiringtypes.RefCheck) []jobs.FieldError {
	var errs []jobs.FieldError
	if len(rc.UnknownSkills) > 0 {
		errs = append(errs, fe("skills", "one or more skills do not exist"))
	}
	if !rc.IndustryOK {
		errs = append(errs, fe("industry_id", "unknown industry"))
	}
	if !rc.SeniorityOK {
		errs = append(errs, fe("seniority_level_id", "unknown seniority level"))
	}
	if !rc.ExperienceOK {
		errs = append(errs, fe("experience_range_id", "unknown experience range"))
	}
	return errs
}

// checkRefs confirms the catalog ids a patch points at exist. It runs before the row is locked, because the
// catalogs are in another database.
func (s *Service) checkRefs(ctx context.Context, p jobs.Patch) []jobs.FieldError {
	skills, industry, seniority, experience, bad := refIDs(p)
	if bad != nil {
		return []jobs.FieldError{*bad}
	}
	if len(skills) == 0 && industry == "" && seniority == "" && experience == "" {
		return nil
	}
	rc, err := s.Ref.CheckRefs(ctx, skills, industry, seniority, experience)
	if err != nil {
		// Cannot confirm: do not block the save on a catalog outage; the ids are well-formed.
		slog.WarnContext(ctx, "draft: reference check failed", "error", err)
		return nil
	}
	return refErrors(rc)
}

// ---- saving details ----

// settle merges a patch into the current details and applies every rule that does not depend on the database:
// market rules, the retention minimum, format checks and, for "Save & continue", completeness.
func (s *Service) settle(
	c Caller, cur jobs.Details, p jobs.Patch, cat jobs.MarketCatalog, strict bool,
) (jobs.Details, []jobs.FieldError) {
	merged := jobs.Merge(cur, p)
	if p.LocationMode != nil && merged.LocationMode == jobs.LocationAnywhere && p.Markets == nil {
		merged.Markets = nil // an anywhere job covers every open market; there is nothing to pick
	}
	var errs []jobs.FieldError
	if p.Markets != nil || p.LocationMode != nil {
		clean, _, merrs := jobs.ValidateMarkets(merged.LocationMode, merged.Markets, cat)
		errs = append(errs, merrs...)
		if len(merrs) == 0 {
			merged.Markets = clean
		}
	}
	months, rerrs := retentionFor(&merged, p, cat)
	merged.RetentionMonths = months
	errs = append(errs, rerrs...)

	target := merged
	if target.ClosingDate == cur.ClosingDate {
		target.ClosingDate = "" // an old closing date already on the draft must not block other edits
	}
	errs = append(errs, jobs.Check(&target, s.now())...)
	if strict {
		errs = append(errs, jobs.Required(&merged, jobs.Options{RequirePay: c.can(rbac.JobSalaryRangeSet)})...)
	}
	return merged, errs
}

// retentionFor raises the retention period to the strictest minimum of the markets the job covers. Asking for
// less than that minimum is an error; leaving it alone raises it silently.
func retentionFor(d *jobs.Details, p jobs.Patch, cat jobs.MarketCatalog) (int, []jobs.FieldError) {
	min := jobs.MinRetention(d.Markets, d.LocationMode, cat)
	if d.RetentionMonths >= min {
		return d.RetentionMonths, nil
	}
	if p.RetentionMonths != nil && *p.RetentionMonths < min {
		return min, []jobs.FieldError{fe("retention_months",
			fmt.Sprintf("applicant data must be kept at least %d months for these markets", min))}
	}
	return min, nil
}

func (s *Service) catalog(ctx context.Context) (jobs.MarketCatalog, error) {
	cat, _, err := s.Ref.Markets(ctx)
	return cat, err
}

// draftEditable is editable plus the rule that only drafts change here.
func draftEditable(c Caller, j *jobs.Job) error {
	if err := editable(c, j); err != nil {
		return err
	}
	if j.Status != jobs.StatusDraft {
		return ErrNotEditable
	}
	return nil
}

// progress works out the completed sections and wizard step after the details changed. "Save & continue"
// (strict) moves the wizard forward; it never moves it back unless the basics are no longer complete.
func progress(cur *hiringtypes.JobRecord, merged *jobs.Details, strict bool) ([]string, string) {
	completed := jobs.MergeSections(jobs.Completed(merged), keepSections(cur.Job.CompletedSections))
	step := cur.Job.CurrentStep
	if strict {
		step = laterStep(step, jobs.NextStep(completed, has(completed, jobs.SectionScreening)))
	}
	if !has(completed, jobs.SectionBasics) || !has(completed, jobs.SectionMarkets) {
		step = jobs.StepDetails
	}
	return completed, step
}

// applyPatch decides the change for one save, under the row lock. It returns nil when nothing changes.
func (s *Service) applyPatch(
	c Caller, cur *hiringtypes.JobRecord, p jobs.Patch, cat jobs.MarketCatalog, strict bool,
) (*hiringtypes.UpdateResult, error) {
	if err := draftEditable(c, &cur.Job); err != nil {
		return nil, err
	}
	merged, errs := s.settle(c, cur.Job.Details, p, cat, strict)
	if merged.LawfulBasis == "" && len(cur.Declarations) > 0 {
		errs = append(errs, fe("lawful_basis", "a lawful basis is needed while special category questions are declared"))
	}
	if len(errs) > 0 {
		return nil, invalid(errs...)
	}
	completed, step := progress(cur, &merged, strict)
	diff := jobs.Diff(&cur.Job.Details, &merged)
	if len(diff) == 0 && step == cur.Job.CurrentStep && sameStrings(completed, cur.Job.CompletedSections) {
		return nil, nil
	}
	return &hiringtypes.UpdateResult{
		Details: &merged, Completed: completed, Step: step, AuditAction: "job.updated", AuditDiff: diff,
	}, nil
}

// Save changes a draft. strict is false for "Save as draft" (PATCH: partial and lenient) and true for
// "Save & continue" (PUT: the required fields must be there and the wizard moves on).
func (s *Service) Save(
	ctx context.Context, c Caller, id string, ifMatch int, p jobs.Patch, strict bool,
) (*JobView, error) {
	if !jobs.IsUUID(id) {
		return nil, hiringtypes.ErrNotFound
	}
	if !c.can(rbac.JobEditDraft) {
		return nil, forbidden("you cannot edit jobs")
	}
	if err := guardPatch(c, p); err != nil {
		return nil, err
	}
	if errs := s.checkRefs(ctx, p); len(errs) > 0 {
		return nil, invalid(errs...)
	}
	cat, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := s.Store.UpdateJob(ctx, c.OrgID, c.UserID, id, ifMatch,
		func(cur *hiringtypes.JobRecord) (*hiringtypes.UpdateResult, error) {
			return s.applyPatch(c, cur, p, cat, strict)
		})
	if err != nil {
		return nil, mapStoreError(err)
	}
	return s.view(ctx, c, rec, cat), nil
}

// mapStoreError turns store errors into the service's field errors where the caller can fix them.
func mapStoreError(err error) error {
	switch {
	case errors.Is(err, hiringtypes.ErrBadReference):
		return invalid(fe("department_id", "unknown department"))
	case errors.Is(err, hiringtypes.ErrLawfulBasisRequired):
		return invalid(fe("lawful_basis", "a lawful basis is needed before special category questions are declared"))
	}
	return err
}

// ---- creating ----

// Create starts a new draft for the caller.
func (s *Service) Create(ctx context.Context, c Caller, req CreateRequest) (*JobView, error) {
	if !c.can(rbac.JobCreate) {
		return nil, forbidden("you cannot create jobs")
	}
	if err := guardPatch(c, req.Patch); err != nil {
		return nil, err
	}
	if errs := s.checkRefs(ctx, req.Patch); len(errs) > 0 {
		return nil, invalid(errs...)
	}
	cat, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}
	base := jobs.NewDetails()
	tpl, err := s.startingPatch(ctx, c, deref(req.TemplateID))
	if err != nil {
		return nil, err
	}
	if tpl != nil {
		base = jobs.Merge(base, *tpl)
	}
	settings, err := s.Store.GetSettings(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	d, errs := s.settle(c, base, req.Patch, cat, false)
	if len(errs) > 0 {
		return nil, invalid(errs...)
	}
	form, decls, err := s.startingForm(ctx, c, deref(req.FormTemplateID), &d, settings.AutomatedScreeningEnabled)
	if err != nil {
		return nil, err
	}
	rec, err := s.Store.CreateJob(ctx, &hiringtypes.CreateInput{
		TenantID: c.OrgID, UserID: c.UserID, Details: d, Completed: jobs.Completed(&d), Step: jobs.StepDetails,
		Form: form, Decls: decls,
	})
	if err != nil {
		return nil, mapStoreError(err)
	}
	return s.view(ctx, c, rec, cat), nil
}

// pickTemplate finds the template a new draft starts from: the one asked for by id (explicit) or else the
// caller's own default of that kind. A nil template with no error means there is none to use.
func (s *Service) pickTemplate(
	ctx context.Context, c Caller, id, kind, field string,
) (t *hiringtypes.Template, explicit bool, err error) {
	if id == "" {
		list, listErr := s.Store.ListTemplates(ctx, c.OrgID, c.UserID, kind)
		if listErr != nil {
			return nil, false, listErr
		}
		for i := range list {
			if list[i].IsDefault {
				return &list[i], false, nil
			}
		}
		return nil, false, nil
	}
	if !c.can(permTemplateUse) && !c.can(permTemplateCreate) {
		return nil, true, forbidden("you cannot use templates")
	}
	if !jobs.IsUUID(id) {
		return nil, true, invalid(fe(field, "unknown template"))
	}
	t, err = s.Store.GetTemplate(ctx, c.OrgID, c.UserID, id)
	if errors.Is(err, hiringtypes.ErrNotFound) {
		return nil, true, invalid(fe(field, "unknown template"))
	}
	if err != nil {
		return nil, true, err
	}
	if t.Kind != kind {
		return nil, true, invalid(fe(field, "this template is for something else"))
	}
	return t, true, nil
}

// startingPatch loads the job details a new draft starts from.
func (s *Service) startingPatch(ctx context.Context, c Caller, templateID string) (*jobs.Patch, error) {
	t, explicit, err := s.pickTemplate(ctx, c, templateID, hiringtypes.KindJobDetails, "template_id")
	if err != nil || t == nil {
		return nil, err
	}
	p, err := jobs.DecodePatch(t.Payload)
	if err != nil {
		if explicit {
			return nil, invalid(fe("template_id", "this template cannot be used"))
		}
		slog.WarnContext(ctx, "draft: ignoring unreadable default template", "error", err)
		return nil, nil
	}
	// A template never carries what the caller could not have set themselves.
	p.Title, p.ClosingDate = nil, nil
	if !c.can(rbac.JobSalaryRangeSet) {
		p.Pay = nil
	}
	if !c.can(rbac.JobRetentionSet) {
		p.RetentionMonths = nil
	}
	return &p, nil
}

type formPayload struct {
	Questions    []questions.FormQuestion `json:"questions"`
	Declarations []questions.Declaration  `json:"declarations"`
}

// formFromTemplate builds a form out of a saved template and reports what is wrong with it.
func formFromTemplate(
	t *hiringtypes.Template, d *jobs.Details, pol questions.Policy,
) ([]questions.FormQuestion, []questions.Declaration, []jobs.FieldError) {
	var fp formPayload
	if err := json.Unmarshal(t.Payload, &fp); err != nil {
		return nil, nil, []jobs.FieldError{fe("form_template_id", "this template cannot be used")}
	}
	for i := range fp.Questions {
		fp.Questions[i].ID = ""
	}
	form, decls, qerrs := questions.Build(fp.Questions, fp.Declarations, pol)
	errs := fromQuestionErrors(qerrs)
	if len(errs) == 0 && len(decls) > 0 && d.LawfulBasis == "" {
		errs = append(errs, fe("form_template_id",
			"this template asks for special category data, which needs a lawful basis first"))
	}
	return form, decls, errs
}

func standardForm(pol questions.Policy) ([]questions.FormQuestion, []questions.Declaration, error) {
	form, decls, errs := questions.Build(questions.DefaultForm(), nil, pol)
	if len(errs) > 0 {
		return nil, nil, fmt.Errorf("draft: standard form is invalid: %v", errs)
	}
	return form, decls, nil
}

// startingForm returns the form a new draft starts with: a saved form template or the standard form.
func (s *Service) startingForm(
	ctx context.Context, c Caller, templateID string, d *jobs.Details, knockout bool,
) ([]questions.FormQuestion, []questions.Declaration, error) {
	pol := questions.Policy{AllowKnockout: knockout}
	t, explicit, err := s.pickTemplate(ctx, c, templateID, hiringtypes.KindApplicationForm, "form_template_id")
	if err != nil {
		return nil, nil, err
	}
	if t == nil {
		return standardForm(pol)
	}
	form, decls, errs := formFromTemplate(t, d, pol)
	switch {
	case len(errs) == 0:
		return form, decls, nil
	case explicit:
		return nil, nil, invalid(errs...)
	}
	slog.WarnContext(ctx, "draft: default form template not usable, using the standard form", "problems", len(errs))
	return standardForm(pol)
}

// ---- application form ----

// FormInput replaces the whole application form.
type FormInput struct {
	Questions    []questions.FormQuestion `json:"questions"`
	Declarations []questions.Declaration  `json:"declarations"`
}

// GetForm returns the application form of a job the caller may see.
func (s *Service) GetForm(ctx context.Context, c Caller, id string) (*FormView, error) {
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	settings, err := s.Store.GetSettings(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	return formView(rec, settings.AutomatedScreeningEnabled), nil
}

func hasKnockout(form []questions.FormQuestion) bool {
	for _, q := range form {
		if len(q.Knockout) > 0 && string(q.Knockout) != "null" {
			return true
		}
	}
	return false
}

func formView(rec *hiringtypes.JobRecord, screening bool) *FormView {
	v := &FormView{
		JobID: rec.Job.ID, JobRevision: rec.Job.Revision, Questions: rec.Form, Declarations: rec.Declarations,
		AutomatedScreeningEnabled: screening, Warnings: []jobs.Warning{},
	}
	if v.Questions == nil {
		v.Questions = []questions.FormQuestion{}
	}
	if v.Declarations == nil {
		v.Declarations = []questions.Declaration{}
	}
	if hasKnockout(rec.Form) {
		v.Warnings = append(v.Warnings, jobs.Warning{Code: "knockout_review", Severity: "info",
			Message: "Knockout answers flag a candidate for a person to review. They never reject anyone automatically."})
	}
	return v
}

// applyForm decides the change for one form save, under the row lock.
func applyForm(
	c Caller, cur *hiringtypes.JobRecord, in FormInput, pol questions.Policy,
) (*hiringtypes.UpdateResult, error) {
	if err := draftEditable(c, &cur.Job); err != nil {
		return nil, err
	}
	form, decls, qerrs := questions.Build(in.Questions, in.Declarations, pol)
	errs := fromQuestionErrors(qerrs)
	if len(decls) > 0 && cur.Job.LawfulBasis == "" {
		errs = append(errs, fe("declarations", "choose a lawful basis on the job details before asking for special data"))
	}
	if len(errs) > 0 {
		return nil, invalid(errs...)
	}
	completed := jobs.MergeSections(cur.Job.CompletedSections, []string{jobs.SectionScreening})
	step := laterStep(cur.Job.CurrentStep, jobs.NextStep(completed, true))
	if !has(completed, jobs.SectionBasics) || !has(completed, jobs.SectionMarkets) {
		step = jobs.StepDetails
	}
	return &hiringtypes.UpdateResult{
		Completed: completed, Step: step, Form: &form, Decls: &decls,
		AuditAction: "job.form_updated",
		AuditDiff: map[string]any{
			"questions":    map[string]any{"from": len(cur.Form), "to": len(form)},
			"declarations": map[string]any{"from": len(cur.Declarations), "to": len(decls)},
		},
	}, nil
}

// SaveForm replaces the application form of a draft ("Save & continue" on the form step).
func (s *Service) SaveForm(ctx context.Context, c Caller, id string, ifMatch int, in FormInput) (*FormView, error) {
	if !jobs.IsUUID(id) {
		return nil, hiringtypes.ErrNotFound
	}
	if !c.can(rbac.JobEditDraft) {
		return nil, forbidden("you cannot edit jobs")
	}
	settings, err := s.Store.GetSettings(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	pol := questions.Policy{AllowKnockout: settings.AutomatedScreeningEnabled}
	rec, err := s.Store.UpdateJob(ctx, c.OrgID, c.UserID, id, ifMatch,
		func(cur *hiringtypes.JobRecord) (*hiringtypes.UpdateResult, error) {
			return applyForm(c, cur, in, pol)
		})
	if err != nil {
		return nil, mapStoreError(err)
	}
	return formView(rec, settings.AutomatedScreeningEnabled), nil
}

// Preview renders the candidate-facing view of a job, exactly as the live page will.
func (s *Service) Preview(ctx context.Context, c Caller, id string) (*jobs.PublicJob, error) {
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	j := rec.Job
	in := jobs.PublicInput{Job: &j, Company: c.CompanyName, Form: rec.Form}
	names := s.skillNames(ctx, j.Skills)
	for _, sk := range j.Skills {
		in.SkillLabels = append(in.SkillLabels, firstNonEmpty(names[sk.SkillID], sk.CustomLabel))
	}
	in.Industry = nameOf(ctx, s.Ref.Industries, j.IndustryID)
	in.Seniority = nameOf(ctx, s.Ref.SeniorityLevels, j.SeniorityLevelID)
	in.Experience = nameOf(ctx, s.Ref.ExperienceRanges, j.ExperienceRangeID)
	out := jobs.BuildPublic(in)
	return &out, nil
}

// nameOf looks up the display name of a catalog id; a missing id or a catalog outage gives "".
func nameOf(ctx context.Context, list func(context.Context) ([]hiringtypes.CatalogItem, error), id string) string {
	if id == "" {
		return ""
	}
	items, err := list(ctx)
	if err != nil {
		return ""
	}
	for _, it := range items {
		if it.ID == id {
			return it.Name
		}
	}
	return ""
}
