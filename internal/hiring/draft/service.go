// Package draft is the job-draft service: it decides who may do what, applies the validation rules to a
// change and asks the store to write it. It depends on small interfaces instead of the database, so every rule
// can be tested with a fake store.
package draft

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/rbac"
)

// Store is the regional database as the service sees it.
type Store interface {
	CreateJob(ctx context.Context, in *hiringtypes.CreateInput) (*hiringtypes.JobRecord, error)
	GetJob(ctx context.Context, tenantID, id string) (*hiringtypes.JobRecord, error)
	UpdateJob(ctx context.Context, tenantID, actorID, id string, ifMatch int,
		fn func(cur *hiringtypes.JobRecord) (*hiringtypes.UpdateResult, error)) (*hiringtypes.JobRecord, error)
	ListJobs(ctx context.Context, tenantID string, f *hiringtypes.ListFilter) ([]hiringtypes.ListItem, string, error)
	DeleteDraft(ctx context.Context, tenantID, actorID, id string, ifMatch int) error
	FindDuplicates(ctx context.Context, tenantID, jobID, titleKey, locationKey, onlyMine string) ([]jobs.Duplicate, error)
	RecordAudit(ctx context.Context, tenantID, actorID, jobID, action string, diff map[string]any) error

	ListDepartments(ctx context.Context, tenantID string) ([]hiringtypes.Department, error)
	EnsureDepartment(ctx context.Context, tenantID, name string) (*hiringtypes.Department, error)

	ListTemplates(ctx context.Context, tenantID, userID, kind string) ([]hiringtypes.Template, error)
	GetTemplate(ctx context.Context, tenantID, userID, id string) (*hiringtypes.Template, error)
	CreateTemplate(ctx context.Context, tenantID, userID, kind, name string,
		payload json.RawMessage) (*hiringtypes.Template, error)
	SaveDefaultTemplate(ctx context.Context, tenantID, userID, kind string,
		payload json.RawMessage) (*hiringtypes.Template, error)
	DeleteTemplate(ctx context.Context, tenantID, userID, id string) error

	GetSettings(ctx context.Context, tenantID string) (hiringtypes.Settings, error)
	SetAutomatedScreening(ctx context.Context, tenantID, actorID string, on bool) error
}

// Reference is the global database: markets and catalogs shared by every company.
type Reference interface {
	Markets(ctx context.Context) (jobs.MarketCatalog, []jobs.Market, error)
	SeniorityLevels(ctx context.Context) ([]hiringtypes.CatalogItem, error)
	ExperienceRanges(ctx context.Context) ([]hiringtypes.CatalogItem, error)
	Industries(ctx context.Context) ([]hiringtypes.CatalogItem, error)
	SearchSkills(ctx context.Context, q string, limit int) ([]hiringtypes.CatalogItem, error)
	CheckRefs(ctx context.Context, skillIDs []string, industry, seniority, experience string,
	) (*hiringtypes.RefCheck, error)
	SkillNames(ctx context.Context, ids []string) (map[string]string, error)
}

// Caller is who is acting and what they may do, resolved once per request.
type Caller struct {
	UserID      string
	OrgID       string
	CompanyName string
	Perms       rbac.Set
}

func (c Caller) can(p rbac.Permission) bool { return c.Perms.Has(p) }

// Permissions that the permission set of rbac has no constant for yet.
const (
	permTemplateUse    rbac.Permission = "job.template.use"
	permTemplateCreate rbac.Permission = "job.template.create"
	permSalaryView     rbac.Permission = "job.salary_range.view"
)

// Service is the job-draft use cases.
type Service struct {
	Store Store
	Ref   Reference
	Now   func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ---- errors ----

// ValidationError carries every field problem found, so the form can mark them all at once.
type ValidationError struct{ Fields []jobs.FieldError }

func (e *ValidationError) Error() string { return "draft: validation failed" }

// ForbiddenError means the caller lacks a permission the action needs.
type ForbiddenError struct{ Msg string }

func (e *ForbiddenError) Error() string { return "draft: forbidden: " + e.Msg }

// ErrNotEditable means the job is no longer a draft.
var ErrNotEditable = errors.New("draft: only drafts can be edited here")

func forbidden(msg string) error { return &ForbiddenError{Msg: msg} }

func invalid(fields ...jobs.FieldError) error { return &ValidationError{Fields: fields} }

func fe(path, msg string) jobs.FieldError { return jobs.FieldError{Path: path, Message: msg} }

func fromQuestionErrors(in []questions.FieldError) []jobs.FieldError {
	out := make([]jobs.FieldError, len(in))
	for i, e := range in {
		out[i] = jobs.FieldError{Path: e.Path, Message: e.Message}
	}
	return out
}

// ---- access rules ----

// canSee: everyone with job.view.all, and people with job.view.assigned for the jobs they created.
// Job members arrive with the permissions step in a later phase.
func canSee(c Caller, createdBy string) bool {
	return c.can(rbac.JobViewAll) || (c.can(rbac.JobViewAssigned) && createdBy == c.UserID)
}

// editable returns ErrNotFound for a job the caller cannot see, so a job's existence is not revealed, and a
// ForbiddenError for one they can see but may not change.
func editable(c Caller, j *jobs.Job) error {
	if !canSee(c, j.CreatedBy) {
		return hiringtypes.ErrNotFound
	}
	if !c.can(rbac.JobEditDraft) || (j.CreatedBy != c.UserID && !c.can(rbac.JobEditPublished)) {
		return forbidden("you cannot edit this job")
	}
	return nil
}

// ---- views ----

// JobView is a job plus the advice shown next to it.
type JobView struct {
	jobs.Job
	Warnings   []jobs.Warning   `json:"warnings"`
	Duplicates []jobs.Duplicate `json:"duplicates,omitempty"`
}

// FormView is the application form of a job.
type FormView struct {
	JobID                     string                   `json:"job_id"`
	JobRevision               int                      `json:"job_revision"`
	Questions                 []questions.FormQuestion `json:"questions"`
	Declarations              []questions.Declaration  `json:"declarations"`
	AutomatedScreeningEnabled bool                     `json:"automated_screening_enabled"`
	Warnings                  []jobs.Warning           `json:"warnings"`
}

func (s *Service) view(ctx context.Context, c Caller, rec *hiringtypes.JobRecord, cat jobs.MarketCatalog) *JobView {
	j := rec.Job
	v := &JobView{Warnings: []jobs.Warning{}}
	if names := s.skillNames(ctx, j.Skills); names != nil {
		skills := make([]jobs.Skill, len(j.Skills))
		for i, sk := range j.Skills {
			sk.Name = firstNonEmpty(names[sk.SkillID], sk.CustomLabel)
			skills[i] = sk
		}
		j.Skills = skills
	}
	if !c.can(rbac.JobSalaryRangeSet) && !c.can(permSalaryView) {
		j.Pay = jobs.Pay{Visible: j.Pay.Visible}
	}
	v.Job = j

	if cat != nil {
		_, mw, _ := jobs.ValidateMarkets(j.LocationMode, j.Markets, cat)
		v.Warnings = append(v.Warnings, mw...)
	}
	v.Warnings = append(v.Warnings, jobs.Warnings(&rec.Job.Details)...)
	if key := jobs.NormaliseTitle(j.Title); key != "" && j.Status == jobs.StatusDraft {
		mine := c.UserID
		if c.can(rbac.JobViewAll) {
			mine = ""
		}
		dups, err := s.Store.FindDuplicates(ctx, c.OrgID, j.ID, key, jobs.LocationKey(j.LocationMode, j.Markets), mine)
		if err != nil {
			slog.WarnContext(ctx, "draft: duplicate lookup failed", "error", err)
		} else if w := jobs.DuplicateWarning(dups); w != nil {
			v.Warnings = append(v.Warnings, *w)
			v.Duplicates = dups
		}
	}
	return v
}

func (s *Service) skillNames(ctx context.Context, skills []jobs.Skill) map[string]string {
	var ids []string
	for _, sk := range skills {
		if sk.SkillID != "" {
			ids = append(ids, sk.SkillID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	names, err := s.Ref.SkillNames(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "draft: skill names lookup failed", "error", err)
		return nil
	}
	return names
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- reading ----

// Get returns one job the caller may see.
func (s *Service) Get(ctx context.Context, c Caller, id string) (*JobView, error) {
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	cat, _, err := s.Ref.Markets(ctx)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, c, rec, cat), nil
}

func (s *Service) load(ctx context.Context, c Caller, id string) (*hiringtypes.JobRecord, error) {
	if !jobs.IsUUID(id) {
		return nil, hiringtypes.ErrNotFound
	}
	rec, err := s.Store.GetJob(ctx, c.OrgID, id)
	if err != nil {
		return nil, err
	}
	if !canSee(c, rec.Job.CreatedBy) {
		return nil, hiringtypes.ErrNotFound
	}
	return rec, nil
}

// ListQuery is the filter of the jobs list as the client sends it.
type ListQuery struct {
	Statuses     []string
	DepartmentID string
	Query        string
	Cursor       string
	Limit        int
}

var knownStatuses = map[string]bool{
	jobs.StatusDraft: true, jobs.StatusPendingApproval: true, jobs.StatusPublished: true, jobs.StatusPaused: true,
	jobs.StatusClosed: true, jobs.StatusArchived: true, jobs.StatusExpired: true,
}

// List returns one page of the jobs the caller may see, newest first.
func (s *Service) List(ctx context.Context, c Caller, q ListQuery) ([]hiringtypes.ListItem, string, error) {
	if !c.can(rbac.JobViewAll) && !c.can(rbac.JobViewAssigned) {
		return nil, "", forbidden("you cannot view jobs")
	}
	var errs []jobs.FieldError
	for _, st := range q.Statuses {
		if !knownStatuses[st] {
			errs = append(errs, fe("status", "unknown status "+st))
		}
	}
	if q.DepartmentID != "" && !jobs.IsUUID(q.DepartmentID) {
		errs = append(errs, fe("department_id", "invalid department"))
	}
	if q.Cursor != "" {
		if _, _, err := hiringtypes.DecodeCursor(q.Cursor); err != nil {
			errs = append(errs, fe("cursor", "invalid cursor"))
		}
	}
	if len(errs) > 0 {
		return nil, "", invalid(errs...)
	}
	f := &hiringtypes.ListFilter{
		Statuses: q.Statuses, DepartmentID: q.DepartmentID, Query: q.Query, Cursor: q.Cursor, Limit: q.Limit,
	}
	if !c.can(rbac.JobViewAll) {
		f.OnlyMine = c.UserID
	}
	return s.Store.ListJobs(ctx, c.OrgID, f)
}

// Delete removes a draft. Only people who may edit it can.
func (s *Service) Delete(ctx context.Context, c Caller, id string, ifMatch int) error {
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return err
	}
	if err := editable(c, &rec.Job); err != nil {
		return err
	}
	return s.Store.DeleteDraft(ctx, c.OrgID, c.UserID, id, ifMatch)
}

// AcknowledgeDuplicate records that the user saw the duplicate warning and kept going.
func (s *Service) AcknowledgeDuplicate(ctx context.Context, c Caller, id string) error {
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return err
	}
	if err := editable(c, &rec.Job); err != nil {
		return err
	}
	return s.Store.RecordAudit(ctx, c.OrgID, c.UserID, id, "job.duplicate_acknowledged", nil)
}
