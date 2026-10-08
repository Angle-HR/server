package jobs

import (
	"encoding/json"

	"github.com/Angle-HR/server/internal/hiring/questions"
)

// PublicQuestion is a question as a candidate sees it. It never carries the knockout rule: a candidate must
// not learn which answer flags them. The special category tag is shown as a plain flag so the form can point
// to the privacy notice.
type PublicQuestion struct {
	ID          string          `json:"id"`
	Section     string          `json:"section"`
	Type        string          `json:"type"`
	Label       string          `json:"label"`
	Description string          `json:"description,omitempty"`
	HelperText  string          `json:"helper_text,omitempty"`
	Required    bool            `json:"required"`
	Config      json.RawMessage `json:"config"`
	SystemKey   string          `json:"system_key,omitempty"`
	Sensitive   bool            `json:"sensitive,omitempty"`
}

// PublicPay is the pay shown on the posting. It exists only when the employer chose to show it.
type PublicPay struct {
	Type     string `json:"type"`
	Currency string `json:"currency"`
	Period   string `json:"period"`
	Min      int64  `json:"min"`
	Max      int64  `json:"max"`
}

// PublicLocation is one place the job is open to.
type PublicLocation struct {
	MarketCode  string `json:"market_code"`
	Subdivision string `json:"subdivision,omitempty"`
	City        string `json:"city,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

// PublicJob is what the public endpoint and the Preview button return. Both use BuildPublic, so the phone
// mock and the live page cannot drift apart.
type PublicJob struct {
	JobCode           string           `json:"job_code"`
	Title             string           `json:"title"`
	Company           string           `json:"company"`
	Department        string           `json:"department,omitempty"`
	Description       Sections         `json:"description_sections"`
	DescriptionText   string           `json:"description_text"`
	LocationMode      string           `json:"location_mode"`
	LocationText      string           `json:"location_text,omitempty"`
	Locations         []PublicLocation `json:"locations"`
	WorkplaceType     string           `json:"workplace_type,omitempty"`
	EmploymentType    string           `json:"employment_type,omitempty"`
	TravelFrequency   string           `json:"travel_frequency,omitempty"`
	VisaSponsorship   string           `json:"visa_sponsorship,omitempty"`
	Industry          string           `json:"industry,omitempty"`
	Seniority         string           `json:"seniority,omitempty"`
	Experience        string           `json:"experience,omitempty"`
	Skills            []string         `json:"skills"`
	Pay               *PublicPay       `json:"pay,omitempty"`
	ClosingDate       string           `json:"closing_date,omitempty"`
	Questions         []PublicQuestion `json:"questions"`
	AssessmentLinkSet bool             `json:"assessment_link_set,omitempty"`
}

// PublicInput gathers the stored job with the labels the public page needs (names instead of ids).
type PublicInput struct {
	Job         *Job
	Company     string
	Industry    string
	Seniority   string
	Experience  string
	Form        []questions.FormQuestion
	SkillLabels []string
}

// BuildPublic renders the candidate-facing view of a job.
func BuildPublic(in PublicInput) PublicJob {
	j := in.Job
	out := PublicJob{
		JobCode:           j.JobCode,
		Title:             j.Title,
		Company:           in.Company,
		Department:        j.DepartmentName,
		Description:       copySections(j.Description),
		DescriptionText:   PlainText(j.Description),
		LocationMode:      j.LocationMode,
		LocationText:      j.LocationText,
		Locations:         []PublicLocation{},
		WorkplaceType:     j.WorkplaceType,
		EmploymentType:    j.EmploymentType,
		TravelFrequency:   j.TravelFrequency,
		VisaSponsorship:   j.VisaSponsorship,
		Industry:          firstNonEmpty(in.Industry, j.CustomIndustry),
		Seniority:         in.Seniority,
		Experience:        in.Experience,
		Skills:            append([]string{}, in.SkillLabels...),
		ClosingDate:       j.ClosingDate,
		Questions:         []PublicQuestion{},
		AssessmentLinkSet: j.AssessmentURL != "",
	}
	for _, m := range j.Markets {
		out.Locations = append(out.Locations, PublicLocation{
			MarketCode: m.MarketCode, Subdivision: m.Subdivision, City: m.City, Timezone: m.Timezone,
		})
	}
	// Pay is shown only when the employer turned it on and the amounts are complete.
	if p := j.Pay; p.Visible && p.Complete() {
		maxPay := *p.Min
		if p.Max != nil {
			maxPay = *p.Max
		}
		out.Pay = &PublicPay{Type: p.Type, Currency: p.Currency, Period: p.Period, Min: *p.Min, Max: maxPay}
	}
	for _, q := range in.Form {
		cfg := q.Config
		if len(cfg) == 0 {
			cfg = json.RawMessage(`{}`)
		}
		out.Questions = append(out.Questions, PublicQuestion{
			ID: q.ID, Section: q.Section, Type: q.Type, Label: q.Label, Description: q.Description,
			HelperText: q.HelperText, Required: q.Required, Config: cfg, SystemKey: q.SystemKey,
			Sensitive: q.SpecialCategory != "",
		})
	}
	return out
}

func copySections(s Sections) Sections {
	out := make(Sections, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
