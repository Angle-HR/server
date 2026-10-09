// Package drafttest has in-memory stand-ins for the hiring store and the global catalogs, so the service and
// the HTTP layer can be tested without a database.
package drafttest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Angle-HR/server/internal/hiring/gates"
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

	// Phase 2.
	People map[string]hiringtypes.Member // company members by user id (for hiring team checks)
	Log    []string                      // compliance log rows as gate|event

	DPAAccepted  []string // accepted agreement versions
	PrivacyEmail string
	DPO          string
	DPIAVersion  int
}

// NewStore returns an empty in-memory store.
func NewStore() *Store {
	return &Store{
		Jobs: map[string]*hiringtypes.JobRecord{}, TplUser: map[string]string{}, People: map[string]hiringtypes.Member{},
	}
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
	f.writePhase2(rec, res)
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
		if flt.OnlyMine != "" && r.Job.CreatedBy != flt.OnlyMine && !isMember(r, flt.OnlyMine) {
			continue
		}
		if len(flt.Statuses) > 0 && !contains(flt.Statuses, r.Job.Status) {
			continue
		}
		if !fakeMatches(r, flt) {
			continue
		}
		out = append(out, fakeListItem(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, "", nil
}

// fakeMatches applies the list filters that go beyond status and ownership.
func fakeMatches(r *hiringtypes.JobRecord, flt *hiringtypes.ListFilter) bool {
	d := r.Job.Details
	switch {
	case flt.CreatedBy != "" && r.Job.CreatedBy != flt.CreatedBy:
		return false
	case flt.Assignee != "" && !isMember(r, flt.Assignee):
		return false
	case flt.EmploymentType != "" && d.EmploymentType != flt.EmploymentType:
		return false
	case flt.WorkplaceType != "" && d.WorkplaceType != flt.WorkplaceType:
		return false
	case flt.LocationMode != "" && d.LocationMode != flt.LocationMode:
		return false
	}
	if flt.Market != "" {
		found := false
		for _, m := range d.Markets {
			if m.MarketCode == flt.Market {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// fakeListItem builds the list row of a stored job, with its hiring managers.
func fakeListItem(r *hiringtypes.JobRecord) hiringtypes.ListItem {
	d := r.Job.Details
	markets := make([]string, 0, len(d.Markets))
	for _, m := range d.Markets {
		markets = append(markets, m.MarketCode)
	}
	managers := []hiringtypes.ListPerson{}
	for _, m := range r.Members {
		if m.Role == hiringtypes.MemberHiringManager {
			managers = append(managers, hiringtypes.ListPerson{UserID: m.UserID, Name: m.Name})
		}
	}
	return hiringtypes.ListItem{
		ID: r.Job.ID, Title: r.Job.Title, Status: r.Job.Status, JobCode: r.Job.JobCode, CreatedBy: r.Job.CreatedBy,
		EmploymentType: d.EmploymentType, LocationMode: d.LocationMode, WorkplaceType: d.WorkplaceType,
		ClosingDate: d.ClosingDate, PublishedAt: r.Job.PublishedAt, Markets: markets, Managers: managers,
	}
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

	// Phase 2.
	GateList     []gates.Gate
	Registry     map[string]hiringtypes.RegistryEntry
	FailRegister error
}

// Gates implements draft.Reference.
func (r *Ref) Gates(context.Context) ([]gates.Gate, error) { return r.GateList, nil }

// RegisterJob implements draft.Reference.
func (r *Ref) RegisterJob(_ context.Context, e hiringtypes.RegistryEntry) error {
	if r.FailRegister != nil {
		return r.FailRegister
	}
	if r.Registry == nil {
		r.Registry = map[string]hiringtypes.RegistryEntry{}
	}
	r.Registry[e.PublicID] = e
	return nil
}

// SetRegistryStatus implements draft.Reference.
func (r *Ref) SetRegistryStatus(_ context.Context, publicID, status string) error {
	e, ok := r.Registry[publicID]
	if !ok {
		return hiringtypes.ErrNotFound
	}
	e.Status = status
	r.Registry[publicID] = e
	return nil
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

func isMember(r *hiringtypes.JobRecord, user string) bool {
	for _, m := range r.Members {
		if m.UserID == user {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// writePhase2 applies the status, team, rules and confirmation parts of an update.
func (f *Store) writePhase2(rec *hiringtypes.JobRecord, res *hiringtypes.UpdateResult) {
	if res.Members != nil {
		rec.Members = append([]hiringtypes.Member(nil), *res.Members...)
	}
	if res.Rules != nil {
		rec.Rules = append(rec.Rules[:0:0], *res.Rules...)
		for i := range rec.Rules {
			if rec.Rules[i].ID == "" {
				rec.Rules[i].ID = f.uuid()
			}
		}
	}
	if res.DPIAConfirm != nil {
		rec.DPIAConfirmedAt = ""
		if *res.DPIAConfirm {
			rec.DPIAConfirmedAt = "2026-10-08T12:00:00Z"
		}
	}
	if c := res.Confirm; c != nil {
		replaced := false
		for i := range rec.Confirmations {
			if rec.Confirmations[i].GateID == c.GateID {
				rec.Confirmations[i].Version, rec.Confirmations[i].Confirmed = c.Version, c.Confirmed
				replaced = true
			}
		}
		if !replaced {
			rec.Confirmations = append(rec.Confirmations, gates.Confirmation{GateID: c.GateID, Version: c.Version, Confirmed: c.Confirmed})
		}
		f.Log = append(f.Log, c.GateID+"|"+map[bool]string{true: "confirmed", false: "withdrawn"}[c.Confirmed])
	}
	if sc := res.Status; sc != nil {
		rec.Job.Status = sc.To
		if sc.To == jobs.StatusPublished && rec.Job.PublishedAt == "" {
			rec.Job.PublishedAt = "2026-10-08T12:00:00Z"
		}
		if sc.PublicID != "" {
			rec.Job.PublicID = sc.PublicID
		}
		if sc.FreezeForm {
			rec.FormVersion++
		}
		for _, l := range sc.Log {
			f.Log = append(f.Log, l.GateID+"|"+l.Event)
		}
	}
}

// OrgMembers implements draft.Store.
func (f *Store) OrgMembers(_ context.Context, _ string, ids []string) (map[string]hiringtypes.Member, error) {
	out := map[string]hiringtypes.Member{}
	for _, id := range ids {
		if m, ok := f.People[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}

// ListPeople implements draft.Store. Roles are not tracked by the fake, so every person comes back without any.
func (f *Store) ListPeople(_ context.Context, _, query string, limit int) ([]hiringtypes.Person, error) {
	out := []hiringtypes.Person{}
	q := strings.ToLower(strings.TrimSpace(query))
	for id, m := range f.People {
		if q != "" && !strings.Contains(strings.ToLower(m.Name), q) && !strings.Contains(strings.ToLower(m.Email), q) {
			continue
		}
		out = append(out, hiringtypes.Person{UserID: id, Name: m.Name, Email: m.Email, Roles: []string{}})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name || (out[i].Name == out[j].Name && out[i].UserID < out[j].UserID)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---- company setup ----

// CompanySetup implements draft.Store.
func (f *Store) CompanySetup(_ context.Context, _, dpaVersion string) (*hiringtypes.CompanySetup, error) {
	out := &hiringtypes.CompanySetup{KYBStatus: "verified", DPAVersion: dpaVersion, PrivacyContact: f.PrivacyEmail,
		DPOContact: f.DPO, DPIAVersion: f.DPIAVersion}
	for _, v := range f.DPAAccepted {
		if v == dpaVersion {
			out.DPAAccepted = true
		}
	}
	return out, nil
}

// AcceptDPA implements draft.Store.
func (f *Store) AcceptDPA(_ context.Context, _, _, version string) error {
	f.DPAAccepted = append(f.DPAAccepted, version)
	return nil
}

// SetPrivacyContact implements draft.Store.
func (f *Store) SetPrivacyContact(_ context.Context, _, email, dpo string) error {
	f.PrivacyEmail, f.DPO = email, dpo
	return nil
}

// RecordDPIA implements draft.Store.
func (f *Store) RecordDPIA(_ context.Context, _, _ string, _ json.RawMessage) (int, error) {
	f.DPIAVersion++
	return f.DPIAVersion, nil
}
