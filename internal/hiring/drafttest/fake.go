// Package drafttest has in-memory stand-ins for the hiring store and the global catalogs, so the service and
// the HTTP layer can be tested without a database.
package drafttest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
)

// Store is an in-memory Store that follows the real store's contract: tenant scoping, optimistic
// revision checks, whole-form replacement and a lawful-basis rule for declarations.
type Store struct {
	Jobs      map[string]*hiringtypes.JobRecord // key tenant|id
	counter   int
	Templates []hiringtypes.Template
	TplUser   map[string]string
	Depts     []hiringtypes.Department
	Screening bool
	Audits    []string
	nextID    int
}

// NewStore returns an empty in-memory store.
func NewStore() *Store {
	return &Store{Jobs: map[string]*hiringtypes.JobRecord{}, TplUser: map[string]string{}}
}

func (f *Store) uuid() string {
	f.nextID++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", f.nextID)
}

// kenyaRetention is the minimum retention of the fake Kenya market, in months.
const kenyaRetention = 12

// Key is the map key of a job: its company and id.
func Key(t, id string) string { return t + "|" + id }

func (f *Store) write(tenant, actor string, rec *hiringtypes.JobRecord, res *hiringtypes.UpdateResult) error {
	if res.Details != nil {
		rec.Job.Details = *res.Details
	}
	if res.Form != nil {
		rec.Form = append([]questions.FormQuestion(nil), *res.Form...)
		for i := range rec.Form {
			if rec.Form[i].ID == "" {
				rec.Form[i].ID = f.uuid()
			}
		}
		rec.FormRevision++
	}
	if res.Decls != nil {
		if len(*res.Decls) > 0 && rec.Job.LawfulBasis == "" {
			return hiringtypes.ErrLawfulBasisRequired
		}
		rec.Declarations = append([]questions.Declaration(nil), *res.Decls...)
	}
	rec.Job.CompletedSections = res.Completed
	rec.Job.CurrentStep = res.Step
	rec.Job.Revision++
	f.Audits = append(f.Audits, res.AuditAction)
	return nil
}

// CreateJob implements draft.Store.
func (f *Store) CreateJob(_ context.Context, in *hiringtypes.CreateInput) (*hiringtypes.JobRecord, error) {
	f.counter++
	id := f.uuid()
	rec := &hiringtypes.JobRecord{Job: jobs.Job{
		ID: id, JobNumber: f.counter, JobCode: jobs.JobCode(f.counter), CreatedBy: in.UserID,
		Status: jobs.StatusDraft, Revision: 0,
	}}
	res := &hiringtypes.UpdateResult{Details: &in.Details, Completed: in.Completed, Step: in.Step,
		Form: &in.Form, Decls: &in.Decls, AuditAction: "job.created"}
	if err := f.write(in.TenantID, in.UserID, rec, res); err != nil {
		return nil, err
	}
	f.Jobs[Key(in.TenantID, id)] = rec
	return clone(rec), nil
}

func clone(r *hiringtypes.JobRecord) *hiringtypes.JobRecord {
	b, err := json.Marshal(r)
	if err != nil {
		panic("drafttest: " + err.Error())
	}
	var out hiringtypes.JobRecord
	if err = json.Unmarshal(b, &out); err != nil {
		panic("drafttest: " + err.Error())
	}
	return &out
}

// GetJob implements draft.Store.
func (f *Store) GetJob(_ context.Context, tenant, id string) (*hiringtypes.JobRecord, error) {
	r, ok := f.Jobs[Key(tenant, id)]
	if !ok {
		return nil, hiringtypes.ErrNotFound
	}
	return clone(r), nil
}

// UpdateJob implements draft.Store.
func (f *Store) UpdateJob(_ context.Context, tenant, actor, id string, ifMatch int,
	fn func(*hiringtypes.JobRecord) (*hiringtypes.UpdateResult, error)) (*hiringtypes.JobRecord, error) {
	r, ok := f.Jobs[Key(tenant, id)]
	if !ok {
		return nil, hiringtypes.ErrNotFound
	}
	if ifMatch != 0 && r.Job.Revision != ifMatch {
		return nil, &hiringtypes.StaleRevisionError{Current: r.Job.Revision}
	}
	work := clone(r)
	res, err := fn(work)
	if err != nil {
		return nil, err
	}
	if res != nil {
		if err := f.write(tenant, actor, work, res); err != nil {
			return nil, err
		}
		f.Jobs[Key(tenant, id)] = work
		r = work
	}
	return clone(r), nil
}

// ListJobs implements draft.Store.
func (f *Store) ListJobs(
	_ context.Context, tenant string, flt *hiringtypes.ListFilter,
) ([]hiringtypes.ListItem, string, error) {
	var out []hiringtypes.ListItem
	for k, r := range f.Jobs {
		if !strings.HasPrefix(k, tenant+"|") {
			continue
		}
		if flt.OnlyMine != "" && r.Job.CreatedBy != flt.OnlyMine {
			continue
		}
		out = append(out, hiringtypes.ListItem{ID: r.Job.ID, Title: r.Job.Title, Status: r.Job.Status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, "", nil
}

// DeleteDraft implements draft.Store.
func (f *Store) DeleteDraft(_ context.Context, tenant, _ string, id string, ifMatch int) error {
	r, ok := f.Jobs[Key(tenant, id)]
	if !ok {
		return hiringtypes.ErrNotFound
	}
	if ifMatch != 0 && r.Job.Revision != ifMatch {
		return &hiringtypes.StaleRevisionError{Current: r.Job.Revision}
	}
	if r.Job.Status != jobs.StatusDraft {
		return hiringtypes.ErrNotDraft
	}
	delete(f.Jobs, Key(tenant, id))
	return nil
}

// FindDuplicates implements draft.Store.
func (f *Store) FindDuplicates(
	_ context.Context, tenant, jobID, titleKey, locKey, onlyMine string,
) ([]jobs.Duplicate, error) {
	var out []jobs.Duplicate
	for k, r := range f.Jobs {
		if !strings.HasPrefix(k, tenant+"|") || r.Job.ID == jobID {
			continue
		}
		if jobs.NormaliseTitle(r.Job.Title) != titleKey || jobs.LocationKey(r.Job.LocationMode, r.Job.Markets) != locKey {
			continue
		}
		if onlyMine != "" && r.Job.CreatedBy != onlyMine {
			continue
		}
		out = append(out, jobs.Duplicate{ID: r.Job.ID, JobCode: r.Job.JobCode, Title: r.Job.Title, Status: r.Job.Status})
	}
	return out, nil
}

// RecordAudit implements draft.Store.
func (f *Store) RecordAudit(_ context.Context, _, _, _ string, action string, _ map[string]any) error {
	f.Audits = append(f.Audits, action)
	return nil
}

// ListDepartments implements draft.Store.
func (f *Store) ListDepartments(context.Context, string) ([]hiringtypes.Department, error) {
	return f.Depts, nil
}

// EnsureDepartment implements draft.Store.
func (f *Store) EnsureDepartment(_ context.Context, _ string, name string) (*hiringtypes.Department, error) {
	for i := range f.Depts {
		if strings.EqualFold(f.Depts[i].Name, name) {
			return &f.Depts[i], nil
		}
	}
	d := hiringtypes.Department{ID: f.uuid(), Name: name}
	f.Depts = append(f.Depts, d)
	return &d, nil
}

// ListTemplates implements draft.Store.
func (f *Store) ListTemplates(_ context.Context, _, user, kind string) ([]hiringtypes.Template, error) {
	var out []hiringtypes.Template
	for _, t := range f.Templates {
		if owner := f.TplUser[t.ID]; (kind == "" || t.Kind == kind) && (owner == "" || owner == user) {
			out = append(out, t)
		}
	}
	return out, nil
}

// GetTemplate implements draft.Store.
func (f *Store) GetTemplate(_ context.Context, _, user, id string) (*hiringtypes.Template, error) {
	for _, t := range f.Templates {
		if owner := f.TplUser[t.ID]; t.ID == id && (owner == "" || owner == user) {
			t := t
			return &t, nil
		}
	}
	return nil, hiringtypes.ErrNotFound
}

// CreateTemplate implements draft.Store.
func (f *Store) CreateTemplate(
	_ context.Context, _, user, kind, name string, payload json.RawMessage,
) (*hiringtypes.Template, error) {
	for _, t := range f.Templates {
		if f.TplUser[t.ID] == "" && t.Kind == kind && strings.EqualFold(t.Name, name) {
			return nil, hiringtypes.ErrConflict
		}
	}
	t := hiringtypes.Template{ID: f.uuid(), Kind: kind, Name: name, Payload: payload, CreatedBy: user}
	f.Templates = append(f.Templates, t)
	return &t, nil
}

// SaveDefaultTemplate implements draft.Store.
func (f *Store) SaveDefaultTemplate(
	_ context.Context, _, user, kind string, payload json.RawMessage,
) (*hiringtypes.Template, error) {
	for i, t := range f.Templates {
		if f.TplUser[t.ID] == user && t.IsDefault && t.Kind == kind {
			f.Templates[i].Payload = payload
			return &f.Templates[i], nil
		}
	}
	t := hiringtypes.Template{
		ID: f.uuid(), Kind: kind, Name: "My default", Payload: payload, IsDefault: true, CreatedBy: user,
	}
	f.Templates = append(f.Templates, t)
	f.TplUser[t.ID] = user
	return &t, nil
}

// DeleteTemplate implements draft.Store.
func (f *Store) DeleteTemplate(_ context.Context, _, user, id string) error {
	for i, t := range f.Templates {
		if t.ID == id && (f.TplUser[id] == "" || f.TplUser[id] == user) {
			f.Templates = append(f.Templates[:i], f.Templates[i+1:]...)
			return nil
		}
	}
	return hiringtypes.ErrNotFound
}

// GetSettings implements draft.Store.
func (f *Store) GetSettings(context.Context, string) (hiringtypes.Settings, error) {
	return hiringtypes.Settings{AutomatedScreeningEnabled: f.Screening}, nil
}

// SetAutomatedScreening implements draft.Store.
func (f *Store) SetAutomatedScreening(_ context.Context, _, _ string, on bool) error {
	f.Screening = on
	return nil
}

// Ref is a small global catalog.
type Ref struct {
	Skills   map[string]string
	Industry string
}

// Markets implements draft.Reference.
func (r *Ref) Markets(context.Context) (jobs.MarketCatalog, []jobs.Market, error) {
	list := []jobs.Market{
		{Code: "UK", Name: "United Kingdom", IsOpen: true, Warnings: []jobs.Warning{}},
		{Code: "US", Name: "United States", IsOpen: true,
			Warnings: []jobs.Warning{{Code: "us_pay_transparency", Message: "pay", Market: "US"}}},
		{Code: "KE", Name: "Kenya", IsOpen: true, MinRetentionMonths: kenyaRetention, Warnings: []jobs.Warning{}},
		{Code: "NG", Name: "Nigeria", IsOpen: false, Warnings: []jobs.Warning{}},
	}
	cat := jobs.MarketCatalog{}
	for _, m := range list {
		cat[m.Code] = m
	}
	return cat, list, nil
}

// SeniorityLevels implements draft.Reference.
func (r *Ref) SeniorityLevels(context.Context) ([]hiringtypes.CatalogItem, error) {
	return []hiringtypes.CatalogItem{{ID: "11111111-1111-4111-8111-111111111111", Name: "Senior"}}, nil
}

// ExperienceRanges implements draft.Reference.
func (r *Ref) ExperienceRanges(context.Context) ([]hiringtypes.CatalogItem, error) {
	return nil, nil
}

// Industries implements draft.Reference.
func (r *Ref) Industries(context.Context) ([]hiringtypes.CatalogItem, error) {
	return []hiringtypes.CatalogItem{{ID: r.Industry, Name: "Tech / Software"}}, nil
}

// SearchSkills implements draft.Reference.
func (r *Ref) SearchSkills(context.Context, string, int) ([]hiringtypes.CatalogItem, error) {
	return nil, nil
}

// CheckRefs implements draft.Reference.
func (r *Ref) CheckRefs(_ context.Context, skills []string, industry, _, _ string) (*hiringtypes.RefCheck, error) {
	rc := &hiringtypes.RefCheck{
		IndustryOK: industry == "" || industry == r.Industry, SeniorityOK: true, ExperienceOK: true,
	}
	for _, id := range skills {
		if _, ok := r.Skills[id]; !ok {
			rc.UnknownSkills = append(rc.UnknownSkills, id)
		}
	}
	return rc, nil
}

// SkillNames implements draft.Reference.
func (r *Ref) SkillNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := r.Skills[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}
