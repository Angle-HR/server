// Package public serves what a candidate sees before applying: the job page, its applicant privacy notice, the
// sitemap and the share links. It holds no personal data. A job is found through the global registry (public id
// to region and company), then read from that company's regional database.
package public

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
)

// Errors the HTTP layer maps to responses.
var (
	// ErrNotFound means no such public job. A draft, a deleted job and an unknown id all look the same.
	ErrNotFound = hiringtypes.ErrNotFound
	// ErrGone means the job was archived. Search engines are told to drop it.
	ErrGone = errors.New("public: job is archived")
)

// NoticeVersion identifies the wording of the applicant privacy notice an application acknowledged. The text is
// generated in notice.go and is a draft until counsel signs it off.
const NoticeVersion = "2026-10-draft"

// State is what a candidate may do with a job right now.
type State string

// Job states as a candidate sees them.
const (
	StateOpen   State = "open"   // published and before the closing date: applications are accepted
	StatePaused State = "paused" // visible, but not accepting applications for now
	StateClosed State = "closed" // closed or expired: the page says so and search engines are told to drop it
)

// StateOf derives the candidate-facing state. The closing date is the last day to apply, so a published job
// stays open through it (UTC). The hourly expiry task catches up later; this makes the page correct before it
// runs. The second result is false for statuses that are not public (draft, pending approval) and for archived
// jobs, which the caller reports as not found or gone.
func StateOf(status, closingDate string, now time.Time) (State, bool) {
	switch status {
	case jobs.StatusPublished:
		if closingDate != "" && closingDate < now.UTC().Format("2006-01-02") {
			return StateClosed, true
		}
		return StateOpen, true
	case jobs.StatusPaused:
		return StatePaused, true
	case jobs.StatusClosed, jobs.StatusExpired:
		return StateClosed, true
	}
	return "", false
}

// OpenJob is one row of the sitemap.
type OpenJob struct {
	PublicID  string
	UpdatedAt time.Time
}

// Registry is the global database as the public pages use it.
type Registry interface {
	// LookupJob finds the region and company of a public id; it returns ErrNotFound for an unknown id.
	LookupJob(ctx context.Context, publicID string) (hiringtypes.RegistryEntry, error)
	// ListOpenJobs lists up to limit published jobs whose closing date has not passed, newest first.
	ListOpenJobs(ctx context.Context, limit int) ([]OpenJob, error)
}

// Company is what the company has told candidates about itself.
type Company struct {
	Name         string
	PrivacyEmail string
	DPOContact   string
}

// Posting is a published job as one region stores it. Form is the frozen form candidates apply with, not the
// editable one, so what they see is what the server validates against.
type Posting struct {
	Job            jobs.Job
	Form           []questions.FormQuestion
	FormVersion    int
	Declarations   []questions.Declaration
	Company        Company
	ScreeningRules bool // the job has disqualification rules
}

// Regional is one region's database.
type Regional interface {
	// LoadPosting reads a published job by public id inside one company. ErrNotFound when the job does not
	// exist, is not public or has never been published.
	LoadPosting(ctx context.Context, tenantID, publicID string) (*Posting, error)
}

// Directory finds a region's database.
type Directory interface {
	Regional(region string) (Regional, error)
}

// Catalog resolves ids on a job to names. A catalog outage only drops labels.
type Catalog interface {
	SkillNames(ctx context.Context, ids []string) (map[string]string, error)
	Industries(ctx context.Context) ([]hiringtypes.CatalogItem, error)
	SeniorityLevels(ctx context.Context) ([]hiringtypes.CatalogItem, error)
	ExperienceRanges(ctx context.Context) ([]hiringtypes.CatalogItem, error)
}

// Service reads public jobs.
type Service struct {
	Registry  Registry
	Directory Directory
	Catalog   Catalog
	// SiteURL is the public origin of the job pages, without a trailing slash, e.g. https://jobs.example.com.
	SiteURL string
	// ApplyURL builds the link of the apply page. Nil uses SiteURL + /j/{public id}/apply. src is the share
	// channel, kept so the application can record where the candidate came from.
	ApplyURL func(publicID, src string) string
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// View is a public job with everything the page, the privacy notice and the apply form need.
type View struct {
	PublicID        string
	State           State
	Job             jobs.PublicJob
	Company         Company
	PublishedAt     string // RFC 3339, "" when unknown
	ClosingDate     string // last day to apply, YYYY-MM-DD, "" when open-ended
	LawfulBasis     string
	RetentionMonths int
	Markets         []string
	Declarations    []questions.Declaration
	Form            []questions.FormQuestion
	FormVersion     int
	ScreeningActive bool
	Region          string
}

// Accepting reports whether an application can be sent now.
func (v *View) Accepting() bool { return v.State == StateOpen }

// Load reads a public job. It returns ErrNotFound for unknown or non-public jobs and ErrGone for archived ones.
func (s *Service) Load(ctx context.Context, publicID string) (*View, error) {
	publicID = strings.TrimSpace(publicID)
	if !validPublicID(publicID) {
		return nil, ErrNotFound
	}
	entry, err := s.Registry.LookupJob(ctx, publicID)
	if err != nil {
		return nil, err
	}
	reg, err := s.Directory.Regional(entry.Region)
	if err != nil {
		return nil, fmt.Errorf("public: region %q: %w", entry.Region, err)
	}
	p, err := reg.LoadPosting(ctx, entry.OrganizationID, publicID)
	if err != nil {
		return nil, err
	}
	if p.Job.Status == jobs.StatusArchived {
		return nil, ErrGone
	}
	state, ok := StateOf(p.Job.Status, p.Job.ClosingDate, s.now())
	if !ok {
		return nil, ErrNotFound
	}

	j := p.Job
	in := jobs.PublicInput{Job: &j, Company: p.Company.Name, Form: p.Form}
	names := s.skillNames(ctx, j.Skills)
	for _, sk := range j.Skills {
		label := names[sk.SkillID]
		if label == "" {
			label = sk.CustomLabel
		}
		if label != "" {
			in.SkillLabels = append(in.SkillLabels, label)
		}
	}
	in.Industry = nameOf(ctx, s.catalogIndustries, j.IndustryID)
	in.Seniority = nameOf(ctx, s.catalogSeniority, j.SeniorityLevelID)
	in.Experience = nameOf(ctx, s.catalogExperience, j.ExperienceRangeID)

	v := &View{
		PublicID:        publicID,
		State:           state,
		Job:             jobs.BuildPublic(in),
		Company:         p.Company,
		PublishedAt:     parseStamp(j.PublishedAt),
		ClosingDate:     j.ClosingDate,
		LawfulBasis:     j.LawfulBasis,
		RetentionMonths: j.RetentionMonths,
		Declarations:    p.Declarations,
		Form:            p.Form,
		FormVersion:     p.FormVersion,
		Region:          entry.Region,
		ScreeningActive: p.ScreeningRules || hasKnockout(p.Form),
	}
	for _, m := range j.Markets {
		v.Markets = appendUnique(v.Markets, m.MarketCode)
	}
	return v, nil
}

// Sitemap lists the open jobs for search engines.
func (s *Service) Sitemap(ctx context.Context, limit int) ([]OpenJob, error) {
	return s.Registry.ListOpenJobs(ctx, limit)
}

// JobURL is the canonical address of a job page.
func (s *Service) JobURL(publicID string) string {
	return strings.TrimRight(s.SiteURL, "/") + "/j/" + publicID
}

// ApplyLink is where the Apply button goes.
func (s *Service) ApplyLink(publicID, src string) string {
	if s.ApplyURL != nil {
		return s.ApplyURL(publicID, src)
	}
	link := s.JobURL(publicID) + "/apply"
	if src != "" {
		link += "?src=" + src
	}
	return link
}

func (s *Service) skillNames(ctx context.Context, skills []jobs.Skill) map[string]string {
	var ids []string
	for _, sk := range skills {
		if sk.SkillID != "" {
			ids = append(ids, sk.SkillID)
		}
	}
	if len(ids) == 0 || s.Catalog == nil {
		return map[string]string{}
	}
	names, err := s.Catalog.SkillNames(ctx, ids)
	if err != nil {
		return map[string]string{}
	}
	return names
}

func (s *Service) catalogIndustries(ctx context.Context) ([]hiringtypes.CatalogItem, error) {
	if s.Catalog == nil {
		return nil, nil
	}
	return s.Catalog.Industries(ctx)
}

func (s *Service) catalogSeniority(ctx context.Context) ([]hiringtypes.CatalogItem, error) {
	if s.Catalog == nil {
		return nil, nil
	}
	return s.Catalog.SeniorityLevels(ctx)
}

func (s *Service) catalogExperience(ctx context.Context) ([]hiringtypes.CatalogItem, error) {
	if s.Catalog == nil {
		return nil, nil
	}
	return s.Catalog.ExperienceRanges(ctx)
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

func hasKnockout(form []questions.FormQuestion) bool {
	for i := range form {
		if k := string(form[i].Knockout); k != "" && k != "null" {
			return true
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// parseStamp turns the store's millisecond timestamp into RFC 3339; anything unreadable gives "".
func parseStamp(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02T15:04:05.000000Z", s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// validPublicID cheaply rejects ids that cannot exist, so junk never reaches the database.
func validPublicID(id string) bool {
	if len(id) < 4 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
