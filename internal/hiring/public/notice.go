package public

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/Angle-HR/server/internal/hiring/questions"
)

// The applicant privacy notice is generated from the job, so it always says what this job actually collects.
// Everything here is DRAFT wording until counsel signs off the applicant notices (due 23 Oct); NoticeVersion
// changes when the wording does, and every application records the version its candidate acknowledged.

var lawfulBasisSentences = map[string]string{
	"contract":             "We need to handle your application to take steps at your request before a possible employment contract.",
	"legitimate_interests": "We have a legitimate interest in assessing applications and choosing the right person for the role, and we have weighed it against your rights.",
	"consent":              "You have given your consent. You can withdraw it at any time.",
	"legal_obligation":     "We are required by law to handle this information as part of recruiting.",
}

var regionPlaces = map[string]string{
	"uk": "the United Kingdom", "eu": "the European Union", "us": "the United States",
	"asia": "India", "africa": "Africa",
}

var specialCategoryNames = map[string]string{
	"health_disability": "health or disability",
	"race_ethnicity":    "race or ethnicity",
	"religion_belief":   "religion or belief",
	"biometric":         "biometric data",
	"criminal_records":  "criminal records",
}

var formSectionNames = map[string]string{
	"personal_information": "Personal information",
	"profile":              "Profile",
	"eligibility":          "Eligibility, availability and pay expectations",
	"screening":            "Screening questions",
}

type noticeSection struct {
	Heading string
	Lines   []string
	Items   []string
}

type noticeData struct {
	Title    string
	Version  string
	Company  string
	JobURL   string
	Sections []noticeSection
}

// RenderNotice renders the job-specific applicant privacy notice served at /j/{public_id}/privacy.
func RenderNotice(v *View, jobURL string) ([]byte, error) {
	d := noticeData{
		Title:   v.Job.Title,
		Version: NoticeVersion,
		Company: v.Company.Name,
		JobURL:  jobURL,
	}

	who := noticeSection{Heading: "Who is responsible for your data"}
	who.Lines = append(who.Lines, fmt.Sprintf("%s is responsible for the information you give when you apply for %s.",
		v.Company.Name, v.Job.Title))
	if v.Company.PrivacyEmail != "" {
		who.Lines = append(who.Lines, "Questions about your data: "+v.Company.PrivacyEmail+".")
	}
	if v.Company.DPOContact != "" {
		who.Lines = append(who.Lines, "Data protection officer: "+v.Company.DPOContact+".")
	}
	who.Lines = append(who.Lines, "Open HR provides the hiring platform and handles your data on the company's behalf.")
	d.Sections = append(d.Sections, who)

	d.Sections = append(d.Sections, noticeSection{Heading: "What we collect", Items: collected(v.Form)})

	if len(v.Declarations) > 0 {
		special := noticeSection{
			Heading: "Sensitive information",
			Lines:   []string{"This application may ask for the following sensitive information, for the reasons given:"},
		}
		for _, dec := range v.Declarations {
			name := specialCategoryNames[dec.Category]
			if name == "" {
				name = dec.Category
			}
			special.Items = append(special.Items, name+": "+dec.Purpose)
		}
		d.Sections = append(d.Sections, special)
	}

	why := noticeSection{Heading: "Why we use it"}
	why.Lines = append(why.Lines, "To assess your application, contact you about it and manage the hiring process for this role.")
	if s := lawfulBasisSentences[v.LawfulBasis]; s != "" {
		why.Lines = append(why.Lines, s)
	}
	d.Sections = append(d.Sections, why)

	if v.ScreeningActive {
		d.Sections = append(d.Sections, noticeSection{
			Heading: "Screening",
			Lines: []string{"Some answers can flag an application so that a person looks at it more closely. " +
				"No application is rejected automatically: a person reviews every flagged application and makes the decision."},
		})
	}

	d.Sections = append(d.Sections, noticeSection{
		Heading: "How long we keep it",
		Lines: []string{fmt.Sprintf("Up to %d months after the hiring decision, or after the role closes if your application "+
			"is not reviewed. After that it is deleted.", v.RetentionMonths)},
	})

	if place := regionPlaces[v.Region]; place != "" {
		d.Sections = append(d.Sections, noticeSection{
			Heading: "Where it is stored",
			Lines:   []string{"Your application is stored in " + place + "."},
		})
	}

	rights := noticeSection{Heading: "Your rights"}
	rights.Lines = append(rights.Lines, "You can ask to see your data, correct it, delete it, restrict or object to its use, "+
		"and receive a copy. You can also complain to your data protection authority.")
	if v.Company.PrivacyEmail != "" {
		rights.Lines = append(rights.Lines, "To use these rights, write to "+v.Company.PrivacyEmail+".")
	}
	d.Sections = append(d.Sections, rights)

	var buf bytes.Buffer
	if err := noticeTemplate.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("public: render notice: %w", err)
	}
	return buf.Bytes(), nil
}

// collected lists the kinds of information the form asks for, by section. Only the question labels are shown,
// never any answer, and special category questions are listed in their own section instead.
func collected(form []questions.FormQuestion) []string {
	order := []string{"personal_information", "profile", "eligibility", "screening"}
	by := map[string][]string{}
	for i := range form {
		q := &form[i]
		if q.SpecialCategory != "" {
			continue
		}
		by[q.Section] = append(by[q.Section], q.Label)
	}
	var out []string
	for _, sec := range order {
		if labels := by[sec]; len(labels) > 0 {
			out = append(out, formSectionNames[sec]+": "+strings.Join(labels, ", "))
		}
	}
	if len(out) == 0 {
		out = []string{"The information you enter in the application form."}
	}
	return out
}

var noticeTemplate = template.Must(template.New("notice").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Applicant privacy notice: {{.Title}} at {{.Company}}</title>
<style>
body{font-family:system-ui,sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem;line-height:1.55;color:#1a1a1a}
h2{margin-top:1.8rem}.v{color:#777;font-size:.85rem}
</style>
</head>
<body>
<main>
<h1>Applicant privacy notice</h1>
<p>{{.Title}} at {{.Company}} · <a href="{{.JobURL}}">back to the job</a></p>
{{range .Sections}}<section><h2>{{.Heading}}</h2>
{{range .Lines}}<p>{{.}}</p>{{end}}
{{if .Items}}<ul>{{range .Items}}<li>{{.}}</li>{{end}}</ul>{{end}}
</section>{{end}}
<p class="v">Notice version {{.Version}}</p>
</main>
</body>
</html>
`))
