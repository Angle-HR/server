package jobs

import (
	"bytes"
	"encoding/json"
)

// PayPatch changes some pay fields. Absent fields keep their value.
type PayPatch struct {
	Type     *string `json:"type"`
	Currency *string `json:"currency"`
	Period   *string `json:"period"`
	Min      *int64  `json:"min"`
	Max      *int64  `json:"max"`
	Visible  *bool   `json:"visible"`
}

// Patch is a partial update of Details. A nil field is left unchanged; an empty string clears a text field.
// The same shape serves "Save as draft" (PATCH) and "Save & continue" (PUT), which differ only in how
// strictly the merged result is checked.
type Patch struct {
	Title             *string                    `json:"title"`
	DepartmentID      *string                    `json:"department_id"`
	ClosingDate       *string                    `json:"closing_date"`
	LocationMode      *string                    `json:"location_mode"`
	LocationText      *string                    `json:"location_text"`
	UseCompanyAddress *bool                      `json:"use_company_address"`
	WorkplaceType     *string                    `json:"workplace_type"`
	TravelFrequency   *string                    `json:"travel_frequency"`
	VisaSponsorship   *string                    `json:"visa_sponsorship"`
	Description       map[string]json.RawMessage `json:"description_sections"`
	IndustryID        *string                    `json:"industry_id"`
	CustomIndustry    *string                    `json:"custom_industry"`
	EmploymentType    *string                    `json:"employment_type"`
	SeniorityLevelID  *string                    `json:"seniority_level_id"`
	ExperienceRangeID *string                    `json:"experience_range_id"`
	Pay               *PayPatch                  `json:"pay"`
	ShowOnCareerPage  *bool                      `json:"show_on_career_page"`
	Skills            *[]Skill                   `json:"skills"`
	Markets           *[]MarketSel               `json:"markets"`
	LawfulBasis       *string                    `json:"lawful_basis"`
	LIAReference      *string                    `json:"lia_reference"`
	RetentionMonths   *int                       `json:"retention_months"`
	AssessmentURL     *string                    `json:"assessment_url"`
}

// DecodePatch parses a request body, rejecting unknown fields.
func DecodePatch(raw []byte) (Patch, error) {
	var p Patch
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Patch{}, err
	}
	return p, nil
}

// TouchesPay reports whether the patch sets any pay field.
func (p Patch) TouchesPay() bool { return p.Pay != nil }

// TouchesRetention reports whether the patch sets the retention period.
func (p Patch) TouchesRetention() bool { return p.RetentionMonths != nil }

func setStr(dst *string, v *string) {
	if v != nil {
		*dst = trim(*v)
	}
}

// Merge applies p to cur and returns the result. cur is not modified.
func Merge(cur Details, p Patch) Details {
	d := cur
	d.Description = make(Sections, len(cur.Description))
	for k, v := range cur.Description {
		d.Description[k] = v
	}
	setStr(&d.Title, p.Title)
	setStr(&d.DepartmentID, p.DepartmentID)
	setStr(&d.ClosingDate, p.ClosingDate)
	setStr(&d.LocationMode, p.LocationMode)
	setStr(&d.LocationText, p.LocationText)
	setStr(&d.WorkplaceType, p.WorkplaceType)
	setStr(&d.TravelFrequency, p.TravelFrequency)
	setStr(&d.VisaSponsorship, p.VisaSponsorship)
	setStr(&d.IndustryID, p.IndustryID)
	setStr(&d.CustomIndustry, p.CustomIndustry)
	setStr(&d.EmploymentType, p.EmploymentType)
	setStr(&d.SeniorityLevelID, p.SeniorityLevelID)
	setStr(&d.ExperienceRangeID, p.ExperienceRangeID)
	setStr(&d.LawfulBasis, p.LawfulBasis)
	setStr(&d.LIAReference, p.LIAReference)
	setStr(&d.AssessmentURL, p.AssessmentURL)
	if p.UseCompanyAddress != nil {
		d.UseCompanyAddress = *p.UseCompanyAddress
	}
	if p.ShowOnCareerPage != nil {
		d.ShowOnCareerPage = *p.ShowOnCareerPage
	}
	if p.RetentionMonths != nil {
		d.RetentionMonths = *p.RetentionMonths
	}
	for k, v := range p.Description {
		if isNullOrEmpty(v) {
			delete(d.Description, k)
			continue
		}
		d.Description[k] = v
	}
	if p.Skills != nil {
		d.Skills = append([]Skill(nil), (*p.Skills)...)
	}
	if p.Markets != nil {
		d.Markets = append([]MarketSel(nil), (*p.Markets)...)
	}
	if p.Pay != nil {
		d.Pay = mergePay(d.Pay, *p.Pay)
	}
	return d
}

func isNullOrEmpty(v json.RawMessage) bool {
	s := bytes.TrimSpace(v)
	return len(s) == 0 || bytes.Equal(s, []byte("null")) || bytes.Equal(s, []byte(`""`))
}

func mergePay(cur Pay, p PayPatch) Pay {
	out := cur
	setStr(&out.Type, p.Type)
	setStr(&out.Currency, p.Currency)
	setStr(&out.Period, p.Period)
	if p.Visible != nil {
		out.Visible = *p.Visible
	}
	if p.Min != nil {
		v := *p.Min
		out.Min = &v
	}
	if p.Max != nil {
		v := *p.Max
		out.Max = &v
	}
	switch out.Type {
	case "":
		// Clearing the pay type clears the amounts too.
		if p.Type != nil {
			out.Min, out.Max = nil, nil
		}
	case "exact":
		// An exact amount has no separate maximum.
		if out.Min != nil {
			v := *out.Min
			out.Max = &v
		}
	}
	return out
}
