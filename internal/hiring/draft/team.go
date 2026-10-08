package draft

import (
	"context"
	"errors"
	"fmt"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/lifecycle"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/hiring/screening"
	"github.com/Angle-HR/server/internal/rbac"
)

// ---- hiring team (the Permissions step) ----

// MaxMembers bounds a job's hiring team.
const MaxMembers = 25

var memberRoles = map[string]bool{
	hiringtypes.MemberHiringManager: true, hiringtypes.MemberRecruiter: true,
	hiringtypes.MemberInterviewer: true, hiringtypes.MemberViewer: true,
}

// MemberInput is one person on the team as the client sends it.
type MemberInput struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// Members lists the hiring team. Only people who may see the access list can.
func (s *Service) Members(ctx context.Context, c Caller, id string) ([]hiringtypes.Member, error) {
	if !c.can(rbac.JobAccessViewList) {
		return nil, forbidden("you cannot see who has access to this job")
	}
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	return s.namedMembers(ctx, c, rec.Members)
}

func (s *Service) namedMembers(ctx context.Context, c Caller, in []hiringtypes.Member) ([]hiringtypes.Member, error) {
	if len(in) == 0 {
		return []hiringtypes.Member{}, nil
	}
	ids := make([]string, len(in))
	for i, m := range in {
		ids[i] = m.UserID
	}
	named, err := s.Store.OrgMembers(ctx, c.OrgID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]hiringtypes.Member, 0, len(in))
	for _, m := range in {
		if n, ok := named[m.UserID]; ok {
			m.Name, m.Email = n.Name, n.Email
		}
		out = append(out, m)
	}
	return out, nil
}

// SetMembers replaces the hiring team. People with job.collaborator.add manage the whole team. People with
// only job.collaborator.add_limited (HR 2) can add and remove viewers on jobs they created, and nothing else:
// they never touch another role.
func (s *Service) SetMembers(
	ctx context.Context, c Caller, id string, ifMatch int, in []MemberInput,
) ([]hiringtypes.Member, error) {
	full, limited := c.can(rbac.JobCollaboratorAdd), c.can(rbac.JobCollaboratorAddLimited)
	if !full && !limited {
		return nil, forbidden("you cannot change who has access to this job")
	}
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if rec.Job.Status == jobs.StatusArchived {
		return nil, ErrNotEditable
	}
	if !full && rec.Job.CreatedBy != c.UserID && !c.can(rbac.JobManageAny) {
		return nil, forbidden("you can only add viewers to jobs you created")
	}

	members, errs := cleanMembers(in, rec.Job.CreatedBy)
	if len(errs) > 0 {
		return nil, invalid(errs...)
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.UserID
	}
	named, err := s.Store.OrgMembers(ctx, c.OrgID, ids)
	if err != nil {
		return nil, err
	}
	for i, m := range members {
		if _, ok := named[m.UserID]; !ok {
			errs = append(errs, fe(fmt.Sprintf("members[%d].user_id", i), "this person is not in your company"))
		}
	}
	if len(errs) > 0 {
		return nil, invalid(errs...)
	}
	if !full {
		if msg := limitedChange(rec.Members, members); msg != "" {
			return nil, forbidden(msg)
		}
	}

	_, err = s.Store.UpdateJob(ctx, c.OrgID, c.UserID, id, ifMatch,
		func(cur *hiringtypes.JobRecord) (*hiringtypes.UpdateResult, error) {
			if !full {
				if msg := limitedChange(cur.Members, members); msg != "" {
					return nil, forbidden(msg)
				}
			}
			step := laterStep(cur.Job.CurrentStep, jobs.StepPermissions)
			if cur.Job.Status != jobs.StatusDraft {
				step = cur.Job.CurrentStep
			}
			return &hiringtypes.UpdateResult{
				Completed: cur.Job.CompletedSections, Step: step, Members: &members,
				AuditAction: "job.members.set", AuditDiff: map[string]any{"count": len(members)},
			}, nil
		})
	if err != nil {
		return nil, mapStoreError(err)
	}
	return s.namedMembers(ctx, c, members)
}

func cleanMembers(in []MemberInput, creator string) ([]hiringtypes.Member, []jobs.FieldError) {
	var errs []jobs.FieldError
	if len(in) > MaxMembers {
		return nil, []jobs.FieldError{fe("members", fmt.Sprintf("a job can have at most %d team members", MaxMembers))}
	}
	out := make([]hiringtypes.Member, 0, len(in))
	seen := map[string]bool{}
	for i, m := range in {
		path := fmt.Sprintf("members[%d]", i)
		switch {
		case !jobs.IsUUID(m.UserID):
			errs = append(errs, fe(path+".user_id", "invalid person"))
		case m.UserID == creator:
			errs = append(errs, fe(path+".user_id", "the person who created the job already has access"))
		case seen[m.UserID]:
			errs = append(errs, fe(path+".user_id", "this person is listed twice"))
		case !memberRoles[m.Role]:
			errs = append(errs, fe(path+".role", "role must be hiring_manager, recruiter, interviewer or viewer"))
		default:
			seen[m.UserID] = true
			out = append(out, hiringtypes.Member{UserID: m.UserID, Role: m.Role})
		}
	}
	return out, errs
}

// limitedChange returns why a caller with only add_limited may not turn the old team into the new one:
// the change must add or remove viewers and leave every other member exactly as it was.
func limitedChange(old, next []hiringtypes.Member) string {
	keep := func(ms []hiringtypes.Member) map[string]string {
		m := map[string]string{}
		for _, x := range ms {
			if x.Role != hiringtypes.MemberViewer {
				m[x.UserID] = x.Role
			}
		}
		return m
	}
	a, b := keep(old), keep(next)
	if len(a) != len(b) {
		return "you can only add or remove viewers"
	}
	for id, role := range a {
		if b[id] != role {
			return "you can only add or remove viewers"
		}
	}
	return ""
}

// ---- screening rules ----

// RulesInput replaces a job's screening rules.
type RulesInput struct {
	Rules []screening.Rule `json:"rules"`
	// DPIAScopeConfirmed is the job owner's confirmation that this job's screening is covered by the company
	// DPIA. Nil leaves it as it is; saving different rules clears it, so it has to be confirmed again.
	DPIAScopeConfirmed *bool `json:"dpia_scope_confirmed"`
}

// RulesView is a job's screening rules and where they stand.
type RulesView struct {
	Rules                     []screening.Rule `json:"rules"`
	DPIAScopeConfirmed        bool             `json:"dpia_scope_confirmed"`
	AutomatedScreeningEnabled bool             `json:"automated_screening_enabled"`
}

// Rules returns a job's screening rules.
func (s *Service) Rules(ctx context.Context, c Caller, id string) (*RulesView, error) {
	if !c.can(rbac.JobEditDraft) {
		return nil, forbidden("you cannot view screening rules")
	}
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	settings, err := s.Store.GetSettings(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	return rulesView(rec, settings), nil
}

func rulesView(rec *hiringtypes.JobRecord, st hiringtypes.Settings) *RulesView {
	rules := rec.Rules
	if rules == nil {
		rules = []screening.Rule{}
	}
	return &RulesView{Rules: rules, DPIAScopeConfirmed: rec.DPIAConfirmedAt != "",
		AutomatedScreeningEnabled: st.AutomatedScreeningEnabled}
}

// SetRules replaces a draft's screening rules. A rule flags a candidate for human review; it never rejects.
// Any rule is automated screening, so none can be saved until Legal has switched screening on.
func (s *Service) SetRules(
	ctx context.Context, c Caller, id string, ifMatch int, in RulesInput,
) (*RulesView, error) {
	if !c.can(rbac.JobEditDraft) {
		return nil, forbidden("you cannot edit screening rules")
	}
	settings, err := s.Store.GetSettings(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	if len(in.Rules) > 0 && !settings.AutomatedScreeningEnabled {
		return nil, invalid(fe("rules", "screening rules need automated screening, which Legal must enable for your company first"))
	}
	var result *hiringtypes.JobRecord
	result, err = s.Store.UpdateJob(ctx, c.OrgID, c.UserID, id, ifMatch,
		func(cur *hiringtypes.JobRecord) (*hiringtypes.UpdateResult, error) {
			if err := draftEditable(c, cur); err != nil {
				return nil, err
			}
			if errs := screening.Validate(in.Rules, cur.Form); len(errs) > 0 {
				out := make([]jobs.FieldError, len(errs))
				for i, e := range errs {
					out[i] = jobs.FieldError{Path: e.Path, Message: e.Message}
				}
				return nil, invalid(out...)
			}
			rules := append([]screening.Rule(nil), in.Rules...) // the store gives new rules their ids
			confirmed := cur.DPIAConfirmedAt != ""
			if in.DPIAScopeConfirmed != nil {
				confirmed = *in.DPIAScopeConfirmed
			} else if !sameRules(cur.Rules, rules) {
				confirmed = false
			}
			if len(rules) == 0 {
				confirmed = false
			}
			completed := cur.Job.CompletedSections
			if len(rules) > 0 {
				completed = jobs.MergeSections(completed, []string{jobs.SectionScreening})
			}
			return &hiringtypes.UpdateResult{
				Completed: completed, Step: cur.Job.CurrentStep, Rules: &rules, DPIAConfirm: &confirmed,
				AuditAction: "job.rules.set", AuditDiff: map[string]any{"count": len(rules)},
			}, nil
		})
	if err != nil {
		return nil, mapStoreError(err)
	}
	return rulesView(result, settings), nil
}

func sameRules(a, b []screening.Rule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].QuestionID != b[i].QuestionID || a[i].Operator != b[i].Operator ||
			string(a[i].Value) != string(b[i].Value) || a[i].Reason != b[i].Reason {
			return false
		}
	}
	return true
}

// ---- bulk, export, duplicate ----

// BulkResult says what a bulk action did. Nothing is all-or-nothing: each job is changed on its own, and the
// ones that could not be are listed with the reason.
type BulkResult struct {
	Action  string              `json:"action"`
	Done    []BulkDone          `json:"done"`
	Skipped []lifecycle.Skipped `json:"skipped"`
}

// BulkDone is one job a bulk action changed, with the status it had before so the client can offer undo.
type BulkDone struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Bulk applies pause, close, archive or to_draft to a selection of jobs.
func (s *Service) Bulk(ctx context.Context, c Caller, action string, ids []string) (*BulkResult, error) {
	a, err := lifecycle.Parse(action)
	if err != nil {
		return nil, mapLifecycleError(err)
	}
	if !lifecycle.BulkActions[a] {
		return nil, invalid(fe("action", "this action is done one job at a time"))
	}
	var items []lifecycle.BulkItem
	recs := map[string]*hiringtypes.JobRecord{}
	var skipped []lifecycle.Skipped
	for _, id := range ids {
		rec, lerr := s.load(ctx, c, id)
		if lerr != nil {
			if errors.Is(lerr, hiringtypes.ErrNotFound) {
				skipped = append(skipped, lifecycle.Skipped{ID: id, Reason: "job not found"})
				continue
			}
			return nil, lerr
		}
		recs[id] = rec
		items = append(items, lifecycle.BulkItem{ID: id, Status: rec.Job.Status, CreatedBy: rec.Job.CreatedBy})
	}
	if len(items) == 0 && len(skipped) == 0 {
		return nil, invalid(fe("ids", fmt.Sprintf("select between 1 and %d jobs", lifecycle.MaxBulk)))
	}
	ok, more, err := lifecycle.BulkPlan(s.lc(c), items, a)
	if err != nil && len(items) > 0 {
		return nil, invalid(fe("ids", fmt.Sprintf("select between 1 and %d jobs", lifecycle.MaxBulk)))
	}
	res := &BulkResult{Action: action, Done: []BulkDone{}, Skipped: append(skipped, more...)}
	for _, it := range ok {
		out, terr := s.transition(ctx, c, it.ID, a, 0)
		if terr != nil {
			res.Skipped = append(res.Skipped, lifecycle.Skipped{ID: it.ID, Reason: terr.Error()})
			continue
		}
		res.Done = append(res.Done, BulkDone{ID: it.ID, Before: it.Status, After: out.Job.Status})
	}
	return res, nil
}

// MaxExportRows bounds one export.
const MaxExportRows = 5000

// Export returns every job the caller may see, newest first, for a spreadsheet.
func (s *Service) Export(ctx context.Context, c Caller, statuses []string) ([]hiringtypes.ListItem, error) {
	if !c.can(rbac.JobExport) {
		return nil, forbidden("you cannot export jobs")
	}
	var out []hiringtypes.ListItem
	cursor := ""
	for len(out) < MaxExportRows {
		page, next, err := s.List(ctx, c, ListQuery{Statuses: statuses, Cursor: cursor, Limit: hiringtypes.MaxListLimit})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	return out, nil
}

// Duplicate copies a job as a new draft owned by the caller. The copy keeps the details and the form, and
// starts with no team, no screening rules, no confirmations and no public id.
func (s *Service) Duplicate(ctx context.Context, c Caller, id string) (*JobView, error) {
	if !c.can(rbac.JobCreate) {
		return nil, forbidden("you cannot create jobs")
	}
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	cat, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}
	d := rec.Job.Details
	if !c.can(rbac.JobSalaryRangeSet) {
		d.Pay = jobs.NewDetails().Pay // do not hand pay to someone who may not set it
	}
	form := make([]questions.FormQuestion, len(rec.Form))
	copy(form, rec.Form)
	for i := range form {
		form[i].ID = ""
	}
	created, err := s.Store.CreateJob(ctx, &hiringtypes.CreateInput{
		TenantID: c.OrgID, UserID: c.UserID, Details: d, Completed: jobs.Completed(&d), Step: jobs.StepDetails,
		Form: form, Decls: append([]questions.Declaration(nil), rec.Declarations...),
	})
	if err != nil {
		return nil, mapStoreError(err)
	}
	if err = s.Store.RecordAudit(ctx, c.OrgID, c.UserID, created.Job.ID, "job.duplicated",
		map[string]any{"from": rec.Job.ID}); err != nil {
		return nil, err
	}
	return s.view(ctx, c, created, cat), nil
}
