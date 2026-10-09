package draft

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/rbac"
)

// ---- departments ----

const maxNameRunes = 80

func cleanName(name string) (string, bool) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return name, true
}

// Departments lists the company's departments.
func (s *Service) Departments(ctx context.Context, c Caller) ([]hiringtypes.Department, error) {
	if !c.can(rbac.JobCreate) && !c.can(rbac.JobViewAll) && !c.can(rbac.JobViewAssigned) {
		return nil, forbidden("you cannot view departments")
	}
	return s.Store.ListDepartments(ctx, c.OrgID)
}

// AddDepartment returns the department with this name, creating it when it does not exist yet.
func (s *Service) AddDepartment(ctx context.Context, c Caller, name string) (*hiringtypes.Department, error) {
	if !c.can(rbac.JobCreate) {
		return nil, forbidden("you cannot add departments")
	}
	clean, ok := cleanName(name)
	if !ok {
		return nil, invalid(fe("name", "department name must be 1 to 80 characters"))
	}
	return s.Store.EnsureDepartment(ctx, c.OrgID, clean)
}

// ---- templates ----

// TemplateInput creates a template from an existing job, so the saved payload always matches what the
// builders produce.
type TemplateInput struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	FromJob     string `json:"from_job_id"`
	AsMyDefault bool   `json:"as_my_default"`
}

// Templates lists the templates the caller can start from.
func (s *Service) Templates(ctx context.Context, c Caller, kind string) ([]hiringtypes.Template, error) {
	if !c.can(permTemplateUse) && !c.can(permTemplateCreate) && !c.can(rbac.JobCreate) {
		return nil, forbidden("you cannot use templates")
	}
	if kind != "" && !validKind(kind) {
		return nil, invalid(fe("kind", "unknown template kind"))
	}
	return s.Store.ListTemplates(ctx, c.OrgID, c.UserID, kind)
}

func validKind(k string) bool {
	return k == hiringtypes.KindJobDetails || k == hiringtypes.KindApplicationForm
}

// SaveTemplate saves a job's setup as a company template, or as the caller's own default
// ("Keep this setup for my future jobs").
func (s *Service) SaveTemplate(ctx context.Context, c Caller, in TemplateInput) (*hiringtypes.Template, error) {
	var errs []jobs.FieldError
	if !validKind(in.Kind) {
		errs = append(errs, fe("kind", "kind must be job_details or application_form"))
	}
	if !jobs.IsUUID(in.FromJob) {
		errs = append(errs, fe("from_job_id", "pick the job to save the setup from"))
	}
	name, nameOK := cleanName(in.Name)
	if !in.AsMyDefault && !nameOK {
		errs = append(errs, fe("name", "template name must be 1 to 80 characters"))
	}
	if len(errs) > 0 {
		return nil, invalid(errs...)
	}
	if in.AsMyDefault {
		if !c.can(rbac.JobCreate) {
			return nil, forbidden("you cannot create jobs")
		}
	} else if !c.can(permTemplateCreate) {
		return nil, forbidden("you cannot create templates")
	}
	rec, err := s.load(ctx, c, in.FromJob)
	if err != nil {
		return nil, err
	}
	payload, err := templatePayload(c, rec, in.Kind)
	if err != nil {
		return nil, err
	}
	if in.AsMyDefault {
		return s.Store.SaveDefaultTemplate(ctx, c.OrgID, c.UserID, in.Kind, payload)
	}
	t, err := s.Store.CreateTemplate(ctx, c.OrgID, c.UserID, in.Kind, name, payload)
	if err != nil {
		if isConflict(err) {
			return nil, invalid(fe("name", "a template with this name already exists"))
		}
		return nil, err
	}
	return t, nil
}

func isConflict(err error) bool { return err == hiringtypes.ErrConflict }

// templatePayload keeps what is reusable from a job. The title and closing date belong to one posting, so they
// stay out; pay and retention stay out unless the caller is allowed to set them, so a template cannot carry
// numbers its author could not see.
func templatePayload(c Caller, rec *hiringtypes.JobRecord, kind string) (json.RawMessage, error) {
	if kind == hiringtypes.KindApplicationForm {
		qs := make([]questions.FormQuestion, len(rec.Form))
		copy(qs, rec.Form)
		for i := range qs {
			qs[i].ID = ""
		}
		decls := rec.Declarations
		if decls == nil {
			decls = []questions.Declaration{}
		}
		return json.Marshal(formPayload{Questions: qs, Declarations: decls})
	}
	raw, err := json.Marshal(rec.Job.Details)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "title")
	delete(m, "closing_date")
	if !c.can(rbac.JobSalaryRangeSet) {
		delete(m, "pay")
	}
	if !c.can(rbac.JobRetentionSet) {
		delete(m, "retention_months")
	}
	return json.Marshal(m)
}

// DeleteTemplate removes a template the caller can see. Company templates need the create permission.
func (s *Service) DeleteTemplate(ctx context.Context, c Caller, id string) error {
	if !jobs.IsUUID(id) {
		return hiringtypes.ErrNotFound
	}
	t, err := s.Store.GetTemplate(ctx, c.OrgID, c.UserID, id)
	if err != nil {
		return err
	}
	if !t.IsDefault && !c.can(permTemplateCreate) {
		return forbidden("you cannot delete templates")
	}
	return s.Store.DeleteTemplate(ctx, c.OrgID, c.UserID, id)
}

// companyTemplate loads a named company template for management. Personal defaults cannot be renamed,
// pinned, duplicated or exported, so they read as not found here.
func (s *Service) companyTemplate(ctx context.Context, c Caller, id string) (*hiringtypes.Template, error) {
	if !c.can(permTemplateCreate) {
		return nil, forbidden("you cannot manage templates")
	}
	if !jobs.IsUUID(id) {
		return nil, hiringtypes.ErrNotFound
	}
	t, err := s.Store.GetTemplate(ctx, c.OrgID, c.UserID, id)
	if err != nil {
		return nil, err
	}
	if t.IsDefault {
		return nil, hiringtypes.ErrNotFound
	}
	return t, nil
}

// RenameTemplate renames a company template.
func (s *Service) RenameTemplate(ctx context.Context, c Caller, id, name string) (*hiringtypes.Template, error) {
	if _, err := s.companyTemplate(ctx, c, id); err != nil {
		return nil, err
	}
	clean, ok := cleanName(name)
	if !ok {
		return nil, invalid(fe("name", "template name must be 1 to 80 characters"))
	}
	t, err := s.Store.RenameTemplate(ctx, c.OrgID, c.UserID, id, clean)
	if isConflict(err) {
		return nil, invalid(fe("name", "a template with this name already exists"))
	}
	return t, err
}

// PinTemplate pins or unpins a company template. Pinned templates list first for everyone.
func (s *Service) PinTemplate(ctx context.Context, c Caller, id string, pinned bool) (*hiringtypes.Template, error) {
	if _, err := s.companyTemplate(ctx, c, id); err != nil {
		return nil, err
	}
	return s.Store.SetTemplatePinned(ctx, c.OrgID, c.UserID, id, pinned)
}

// DuplicateTemplate copies a company template under a new name (default "<name> (copy)").
func (s *Service) DuplicateTemplate(ctx context.Context, c Caller, id, name string) (*hiringtypes.Template, error) {
	src, err := s.companyTemplate(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = src.Name + " (copy)"
	}
	clean, ok := cleanName(name)
	if !ok {
		return nil, invalid(fe("name", "template name must be 1 to 80 characters"))
	}
	t, err := s.Store.CreateTemplate(ctx, c.OrgID, c.UserID, src.Kind, clean, src.Payload)
	if isConflict(err) {
		return nil, invalid(fe("name", "a template with this name already exists"))
	}
	return t, err
}

// TemplateExport is a template in a portable form.
type TemplateExport struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload"`
}

// ExportTemplate returns a company template's name, kind and payload.
func (s *Service) ExportTemplate(ctx context.Context, c Caller, id string) (*TemplateExport, error) {
	t, err := s.companyTemplate(ctx, c, id)
	if err != nil {
		return nil, err
	}
	return &TemplateExport{Version: 1, Kind: t.Kind, Name: t.Name, Payload: t.Payload}, nil
}

// ---- settings ----

// Settings returns the company's hiring settings.
func (s *Service) Settings(ctx context.Context, c Caller) (hiringtypes.Settings, error) {
	if !c.can(rbac.JobCreate) && !c.can(rbac.FormAutomatedScreening) {
		return hiringtypes.Settings{}, forbidden("you cannot view hiring settings")
	}
	return s.Store.GetSettings(ctx, c.OrgID)
}

// SetAutomatedScreening turns knockout questions on or off for the company. Only Legal can.
func (s *Service) SetAutomatedScreening(ctx context.Context, c Caller, on bool) (hiringtypes.Settings, error) {
	if !c.can(rbac.FormAutomatedScreening) {
		return hiringtypes.Settings{}, forbidden("only your legal contact can turn automated screening on or off")
	}
	if err := s.Store.SetAutomatedScreening(ctx, c.OrgID, c.UserID, on); err != nil {
		return hiringtypes.Settings{}, err
	}
	return s.Store.GetSettings(ctx, c.OrgID)
}

// ---- catalogs ----

// Option is one value of a fixed pick list.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Catalog is everything the job form needs to draw its pick lists.
type Catalog struct {
	LocationModes     []Option                  `json:"location_modes"`
	WorkplaceTypes    []Option                  `json:"workplace_types"`
	EmploymentTypes   []Option                  `json:"employment_types"`
	TravelFrequencies []Option                  `json:"travel_frequencies"`
	VisaPolicies      []Option                  `json:"visa_policies"`
	PayTypes          []Option                  `json:"pay_types"`
	PayPeriods        []Option                  `json:"pay_periods"`
	LawfulBases       []Option                  `json:"lawful_bases"`
	Currencies        []string                  `json:"currencies"`
	RetentionMonths   []int                     `json:"retention_months"`
	DescriptionKeys   []string                  `json:"description_sections"`
	MaxTitleLength    int                       `json:"max_title_length"`
	SeniorityLevels   []hiringtypes.CatalogItem `json:"seniority_levels"`
	ExperienceRanges  []hiringtypes.CatalogItem `json:"experience_ranges"`
	Industries        []hiringtypes.CatalogItem `json:"industries"`
	Markets           []jobs.Market             `json:"markets"`
}

func opts(pairs ...string) []Option {
	out := make([]Option, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Option{Value: pairs[i], Label: pairs[i+1]})
	}
	return out
}

// Catalog returns the pick lists. Fixed lists are in code (they are validated against the same sets);
// seniority, experience, industries and markets come from the global database.
func (s *Service) Catalog(ctx context.Context) (*Catalog, error) {
	cat := &Catalog{
		LocationModes: opts("anywhere", "Anywhere", "specific_area", "Specific area",
			"specific_timezone", "Specific time zone"),
		WorkplaceTypes: opts("onsite", "On-site", "hybrid", "Hybrid", "remote", "Remote"),
		EmploymentTypes: opts("full_time", "Full-time", "part_time", "Part-time", "contract", "Contract",
			"internship", "Internship"),
		TravelFrequencies: opts("none", "None", "occasional", "Occasional", "frequent", "Frequent"),
		VisaPolicies:      opts("yes", "Yes", "no", "No", "case_by_case", "Case by case"),
		PayTypes:          opts("exact", "Exact", "range", "Range"),
		PayPeriods:        opts("hour", "Per hour", "month", "Per month", "year", "Per year"),
		LawfulBases: opts("contract", "Contract", "legitimate_interests", "Legitimate interests",
			"consent", "Consent", "legal_obligation", "Legal obligation"),
		Currencies:      jobs.CurrencyCodes,
		RetentionMonths: jobs.RetentionChoices,
		DescriptionKeys: jobs.DescriptionKeys,
		MaxTitleLength:  jobs.MaxTitleRunes,
	}
	var err error
	if cat.SeniorityLevels, err = s.Ref.SeniorityLevels(ctx); err != nil {
		return nil, err
	}
	if cat.ExperienceRanges, err = s.Ref.ExperienceRanges(ctx); err != nil {
		return nil, err
	}
	if cat.Industries, err = s.Ref.Industries(ctx); err != nil {
		return nil, err
	}
	if _, cat.Markets, err = s.Ref.Markets(ctx); err != nil {
		return nil, err
	}
	return cat, nil
}

// Skills searches the skill catalog by name prefix.
func (s *Service) Skills(ctx context.Context, q string, limit int) ([]hiringtypes.CatalogItem, error) {
	return s.Ref.SearchSkills(ctx, q, limit)
}
