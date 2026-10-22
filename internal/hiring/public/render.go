package public

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html/template"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Angle-HR/server/internal/hiring/jobs"
)

// sectionHeadings are the fixed sections of a job description, in display order.
var sectionHeadings = map[string]string{
	"about_company":    "About the company",
	"role":             "The role",
	"responsibilities": "Responsibilities",
	"requirements":     "Requirements",
	"benefits":         "Benefits",
	"hiring_process":   "Hiring process",
}

// marketNames and marketCountries describe the launch markets. EU is a bloc, so it has no single country.
var (
	marketNames = map[string]string{
		"UK": "United Kingdom", "US": "United States", "EU": "European Union",
		"IN": "India", "NG": "Nigeria", "KE": "Kenya", "ZA": "South Africa",
	}
	marketCountries = map[string]string{"UK": "GB", "US": "US", "IN": "IN", "NG": "NG", "KE": "KE", "ZA": "ZA"}
)

type section struct {
	Heading    string
	Paragraphs []string
}

type pageData struct {
	Title       string
	Company     string
	Description string
	Canonical   string
	ApplyURL    string
	PrivacyURL  string
	Accepting   bool
	State       State
	StateLine   string
	NoIndex     bool
	Facts       []string
	Sections    []section
	Skills      []string
	ClosingDate string
	LD          map[string]any
}

// RenderPage renders the server-side job page: Open Graph tags and JobPosting JSON-LD for crawlers and link
// previews, the description for people, and a link to the apply page. Every value is escaped by html/template,
// and the description is rendered from plain text, so nothing an employer types can inject markup.
func RenderPage(v *View, canonical, applyURL, privacyURL string) ([]byte, error) {
	d := pageData{
		Title:       v.Job.Title,
		Company:     v.Job.Company,
		Canonical:   canonical,
		ApplyURL:    applyURL,
		PrivacyURL:  privacyURL,
		Accepting:   v.Accepting(),
		State:       v.State,
		NoIndex:     v.State != StateOpen,
		Facts:       facts(v),
		Skills:      v.Job.Skills,
		ClosingDate: v.ClosingDate,
	}
	d.Description = shorten(v.Job.DescriptionText, 200)
	if d.Description == "" {
		d.Description = fmt.Sprintf("%s at %s", v.Job.Title, v.Job.Company)
	}
	switch v.State {
	case StatePaused:
		d.StateLine = "This role is not accepting applications right now."
	case StateClosed:
		d.StateLine = "This role is closed and no longer accepting applications."
	}
	for _, key := range jobs.DescriptionKeys {
		raw, ok := v.Job.Description[key]
		if !ok {
			continue
		}
		text := jobs.PlainText(jobs.Sections{key: raw})
		if text == "" {
			continue
		}
		d.Sections = append(d.Sections, section{Heading: sectionHeadings[key], Paragraphs: paragraphs(text)})
	}
	// Only an open job is offered to Google for Jobs. A closed one must not carry the markup.
	if v.State == StateOpen {
		// html/template encodes the map as JSON inside the script element, escaping <, > and &, so the data
		// cannot close the element.
		d.LD = JobPostingLD(v)
	}
	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("public: render page: %w", err)
	}
	return buf.Bytes(), nil
}

// RenderMessage renders a small page for a job that is gone, so a person who follows an old link sees why.
func RenderMessage(title, message string) []byte {
	var buf bytes.Buffer
	_ = messageTemplate.Execute(&buf, map[string]string{"Title": title, "Message": message})
	return buf.Bytes()
}

func facts(v *View) []string {
	var out []string
	if v.Job.Department != "" {
		out = append(out, v.Job.Department)
	}
	if t := labelOf(employmentLabels, v.Job.EmploymentType); t != "" {
		out = append(out, t)
	}
	if t := labelOf(workplaceLabels, v.Job.WorkplaceType); t != "" {
		out = append(out, t)
	}
	if loc := locationLine(v.Job); loc != "" {
		out = append(out, loc)
	}
	if p := payLine(v.Job.Pay); p != "" {
		out = append(out, p)
	}
	if v.ClosingDate != "" {
		out = append(out, "Apply by "+v.ClosingDate)
	}
	return out
}

var (
	employmentLabels = map[string]string{
		"full_time": "Full time", "part_time": "Part time", "contract": "Contract", "internship": "Internship",
	}
	workplaceLabels = map[string]string{"onsite": "On site", "hybrid": "Hybrid", "remote": "Remote"}
)

func labelOf(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return ""
}

func locationLine(j jobs.PublicJob) string {
	if j.LocationText != "" {
		return j.LocationText
	}
	var places []string
	for _, l := range j.Locations {
		var parts []string
		if l.City != "" {
			parts = append(parts, l.City)
		}
		if l.Subdivision != "" {
			parts = append(parts, l.Subdivision)
		}
		name := marketNames[l.MarketCode]
		if name == "" {
			name = l.MarketCode
		}
		parts = append(parts, name)
		places = append(places, strings.Join(parts, ", "))
	}
	return strings.Join(places, " · ")
}

// payLine formats pay for people. Amounts are minor units; every currency is shown with two decimals' worth of
// minor units, which holds for GBP, USD, EUR, INR, NGN and KES.
func payLine(p *jobs.PublicPay) string {
	if p == nil {
		return ""
	}
	per := map[string]string{"hour": "per hour", "month": "per month", "year": "per year"}[p.Period]
	amount := groupDigits(p.Min / 100)
	if p.Type == "range" && p.Max > p.Min {
		amount += " to " + groupDigits(p.Max/100)
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %s", p.Currency, amount, per))
}

func groupDigits(n int64) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func paragraphs(text string) []string {
	var out []string
	for _, p := range strings.Split(text, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// JobPostingLD builds the schema.org JobPosting that Google for Jobs reads. Fields without data are left out
// rather than guessed: Google ignores a partial posting less harshly than a wrong one.
func JobPostingLD(v *View) map[string]any {
	j := v.Job
	ld := map[string]any{
		"@context":           "https://schema.org",
		"@type":              "JobPosting",
		"title":              j.Title,
		"description":        j.DescriptionText,
		"identifier":         map[string]any{"@type": "PropertyValue", "name": j.Company, "value": j.JobCode},
		"hiringOrganization": map[string]any{"@type": "Organization", "name": j.Company},
		"directApply":        true,
	}
	if v.PublishedAt != "" {
		ld["datePosted"] = v.PublishedAt[:10]
	}
	if v.ClosingDate != "" {
		// The closing date is the last day to apply, so the posting is valid until the end of that day (UTC).
		ld["validThrough"] = v.ClosingDate + "T23:59:59Z"
	}
	if t := map[string]string{
		"full_time": "FULL_TIME", "part_time": "PART_TIME", "contract": "CONTRACTOR", "internship": "INTERN",
	}[j.EmploymentType]; t != "" {
		ld["employmentType"] = t
	}

	var places []any
	var countries []any
	for _, l := range j.Locations {
		cc := marketCountries[l.MarketCode]
		if cc == "" {
			continue // EU is a bloc: Google needs a country, so no place is claimed for it
		}
		countries = append(countries, map[string]any{"@type": "Country", "name": cc})
		addr := map[string]any{"@type": "PostalAddress", "addressCountry": cc}
		if l.City != "" {
			addr["addressLocality"] = l.City
		}
		if l.Subdivision != "" {
			addr["addressRegion"] = l.Subdivision
		}
		places = append(places, map[string]any{"@type": "Place", "address": addr})
	}
	if j.WorkplaceType == "remote" {
		ld["jobLocationType"] = "TELECOMMUTE"
		if len(countries) > 0 {
			ld["applicantLocationRequirements"] = countries
		}
	} else if len(places) > 0 {
		ld["jobLocation"] = places
	}

	if p := j.Pay; p != nil {
		unit := map[string]string{"hour": "HOUR", "month": "MONTH", "year": "YEAR"}[p.Period]
		val := map[string]any{"@type": "QuantitativeValue", "unitText": unit}
		if p.Type == "range" && p.Max > p.Min {
			val["minValue"], val["maxValue"] = float64(p.Min)/100, float64(p.Max)/100
		} else {
			val["value"] = float64(p.Min) / 100
		}
		ld["baseSalary"] = map[string]any{"@type": "MonetaryAmount", "currency": p.Currency, "value": val}
	}
	return ld
}

type urlSet struct {
	XMLName xml.Name     `xml:"urlset"`
	NS      string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// MaxSitemapURLs is the protocol's limit for one sitemap file.
const MaxSitemapURLs = 50000

// RenderSitemap renders sitemap.xml for the open jobs. siteURL has no trailing slash.
func RenderSitemap(siteURL string, list []OpenJob) ([]byte, error) {
	set := urlSet{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for i, j := range list {
		if i >= MaxSitemapURLs {
			break
		}
		u := sitemapURL{Loc: strings.TrimRight(siteURL, "/") + "/j/" + j.PublicID}
		if !j.UpdatedAt.IsZero() {
			u.LastMod = j.UpdatedAt.UTC().Format(time.DateOnly)
		}
		set.URLs = append(set.URLs, u)
	}
	out, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("public: sitemap: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} at {{.Company}}</title>
<meta name="description" content="{{.Description}}">
<link rel="canonical" href="{{.Canonical}}">
{{if .NoIndex}}<meta name="robots" content="noindex">{{end}}
<meta property="og:type" content="website">
<meta property="og:title" content="{{.Title}} at {{.Company}}">
<meta property="og:description" content="{{.Description}}">
<meta property="og:url" content="{{.Canonical}}">
<meta name="twitter:card" content="summary">
{{if .LD}}<script type="application/ld+json">{{.LD}}</script>{{end}}
<style>
body{font-family:system-ui,sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem;line-height:1.55;color:#1a1a1a}
h1{margin-bottom:.25rem}.co{color:#555;margin-top:0}.facts{list-style:none;padding:0;display:flex;flex-wrap:wrap;gap:.5rem}
.facts li{background:#f1f1f1;border-radius:999px;padding:.2rem .7rem;font-size:.9rem}
.btn{display:inline-block;background:#111;color:#fff;padding:.7rem 1.2rem;border-radius:8px;text-decoration:none}
.note{background:#fff7e0;border:1px solid #f0d98a;padding:.7rem 1rem;border-radius:8px}
footer{margin-top:2.5rem;font-size:.85rem;color:#555}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p class="co">{{.Company}}</p>
{{if .Facts}}<ul class="facts">{{range .Facts}}<li>{{.}}</li>{{end}}</ul>{{end}}
{{if .StateLine}}<p class="note">{{.StateLine}}</p>{{end}}
{{if .Accepting}}<p><a class="btn" href="{{.ApplyURL}}">Apply for this role</a></p>{{end}}
{{range .Sections}}<section><h2>{{.Heading}}</h2>{{range .Paragraphs}}<p>{{.}}</p>{{end}}</section>{{end}}
{{if .Skills}}<section><h2>Skills</h2><p>{{range $i, $s := .Skills}}{{if $i}}, {{end}}{{$s}}{{end}}</p></section>{{end}}
{{if .Accepting}}<p><a class="btn" href="{{.ApplyURL}}">Apply for this role</a></p>{{end}}
</main>
<footer><a href="{{.PrivacyURL}}">How {{.Company}} uses your data when you apply</a></footer>
</body>
</html>
`))

var messageTemplate = template.Must(template.New("message").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex"><title>{{.Title}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:720px;margin:4rem auto;padding:0 1rem;line-height:1.55}</style>
</head><body><h1>{{.Title}}</h1><p>{{.Message}}</p></body></html>
`))
