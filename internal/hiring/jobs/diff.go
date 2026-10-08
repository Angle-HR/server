package jobs

import "reflect"

// Diff lists the top-level Details fields that differ between before and after, for the audit log.
// Description bodies are reported as changed without their content; the draft itself holds the text.
func Diff(before, after *Details) map[string]any {
	out := map[string]any{}
	record := func(name string, b, a any) {
		if !reflect.DeepEqual(b, a) {
			out[name] = map[string]any{"from": b, "to": a}
		}
	}
	record("title", before.Title, after.Title)
	record("department_id", before.DepartmentID, after.DepartmentID)
	record("closing_date", before.ClosingDate, after.ClosingDate)
	record("location_mode", before.LocationMode, after.LocationMode)
	record("location_text", before.LocationText, after.LocationText)
	record("use_company_address", before.UseCompanyAddress, after.UseCompanyAddress)
	record("workplace_type", before.WorkplaceType, after.WorkplaceType)
	record("travel_frequency", before.TravelFrequency, after.TravelFrequency)
	record("visa_sponsorship", before.VisaSponsorship, after.VisaSponsorship)
	record("industry_id", before.IndustryID, after.IndustryID)
	record("custom_industry", before.CustomIndustry, after.CustomIndustry)
	record("employment_type", before.EmploymentType, after.EmploymentType)
	record("seniority_level_id", before.SeniorityLevelID, after.SeniorityLevelID)
	record("experience_range_id", before.ExperienceRangeID, after.ExperienceRangeID)
	record("pay", before.Pay, after.Pay)
	record("show_on_career_page", before.ShowOnCareerPage, after.ShowOnCareerPage)
	record("skills", before.Skills, after.Skills)
	record("markets", before.Markets, after.Markets)
	record("lawful_basis", before.LawfulBasis, after.LawfulBasis)
	record("lia_reference", before.LIAReference, after.LIAReference)
	record("retention_months", before.RetentionMonths, after.RetentionMonths)
	record("assessment_url", before.AssessmentURL, after.AssessmentURL)
	if !SectionsEqual(before.Description, after.Description) {
		changed := []string{}
		for _, k := range DescriptionKeys {
			if !SectionsEqual(Sections{k: before.Description[k]}, Sections{k: after.Description[k]}) {
				changed = append(changed, k)
			}
		}
		out["description_sections"] = map[string]any{"changed": changed}
	}
	return out
}
