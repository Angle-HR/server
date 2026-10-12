package jobs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/questions"
)

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

const deptID = "11111111-1111-1111-1111-111111111111"

func ptr[T any](v T) *T { return &v }

func hasPath(errs []FieldError, path string) bool {
	for _, e := range errs {
		if e.Path == path {
			return true
		}
	}
	return false
}

func complete() Details {
	d := NewDetails()
	d.Title = "Senior Backend Engineer"
	d.DepartmentID = deptID
	d.LocationMode = LocationSpecificArea
	d.TravelFrequency = "none"
	d.VisaSponsorship = "no"
	d.EmploymentType = "full_time"
	d.Markets = []MarketSel{{MarketCode: "UK"}}
	d.Pay = Pay{Type: "range", Currency: "GBP", Period: "year", Min: ptr(int64(5000000)), Max: ptr(int64(7000000)), Visible: true}
	return d
}

func TestTitleRules(t *testing.T) {
	tests := []struct {
		title string
		ok    bool
	}{
		{"Senior Backend Engineer", true},
		{"Développeur Sénior (H/F)", true},
		{"C++ / Go Developer – Remote", false}, // en dash is not in the list
		{"Sales & Marketing Lead, EMEA", true},
		{"Engineer <script>", false},
		{"Rockstar 🚀", false},
		{strings.Repeat("a", 70), true},
		{strings.Repeat("a", 71), false},
	}
	for _, tc := range tests {
		d := NewDetails()
		d.Title = tc.title
		if got := !hasPath(Check(&d, now), "title"); got != tc.ok {
			t.Errorf("title %q valid = %v, want %v", tc.title, got, tc.ok)
		}
	}
}

func TestCheckFormats(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(d *Details)
		path   string
	}{
		{"past closing date", func(d *Details) { d.ClosingDate = "2026-10-07" }, "closing_date"},
		{"bad closing date", func(d *Details) { d.ClosingDate = "next week" }, "closing_date"},
		{"bad location mode", func(d *Details) { d.LocationMode = "mars" }, "location_mode"},
		{"bad employment type", func(d *Details) { d.EmploymentType = "freelance" }, "employment_type"},
		{"bad department", func(d *Details) { d.DepartmentID = "sales" }, "department_id"},
		{"industry and custom", func(d *Details) { d.IndustryID = deptID; d.CustomIndustry = "Space" }, "custom_industry"},
		{"retention 5", func(d *Details) { d.RetentionMonths = 5 }, "retention_months"},
		{"http assessment link", func(d *Details) { d.AssessmentURL = "http://example.com/test" }, "assessment_url"},
		{"unsupported currency", func(d *Details) { d.Pay.Currency = "SGD" }, "pay.currency"},
		{"max below min", func(d *Details) { d.Pay = Pay{Min: ptr(int64(10)), Max: ptr(int64(5))} }, "pay.max"},
		{"negative pay", func(d *Details) { d.Pay.Min = ptr(int64(-1)) }, "pay.min"},
		{"unknown description key", func(d *Details) { d.Description = Sections{"extras": json.RawMessage(`"x"`)} }, "description_sections.extras"},
		{"invalid description json", func(d *Details) { d.Description = Sections{"role": json.RawMessage(`{oops`)} }, "description_sections.role"},
		{"skill with both", func(d *Details) { d.Skills = []Skill{{SkillID: deptID, CustomLabel: "Go"}} }, "skills[0]"},
		{"skill with neither", func(d *Details) { d.Skills = []Skill{{}} }, "skills[0]"},
		{"duplicate skill", func(d *Details) { d.Skills = []Skill{{CustomLabel: "Go"}, {CustomLabel: "go"}} }, "skills[1]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := complete()
			tc.mutate(&d)
			if errs := Check(&d, now); !hasPath(errs, tc.path) {
				t.Errorf("want error at %s, got %v", tc.path, errs)
			}
		})
	}
	good := complete()
	good.ClosingDate = "2026-10-08" // today is allowed
	good.Skills = []Skill{{SkillID: deptID}, {CustomLabel: "Rust"}}
	good.Description = Sections{"role": json.RawMessage(`{"type":"doc"}`)}
	if errs := Check(&good, now); len(errs) != 0 {
		t.Errorf("valid details rejected: %v", errs)
	}
	empty := NewDetails()
	if errs := Check(&empty, now); len(errs) != 0 {
		t.Errorf("a blank draft must pass the format check: %v", errs)
	}
}

func TestRequired(t *testing.T) {
	blank := NewDetails()
	errs := Required(&blank, Options{RequirePay: true})
	for _, p := range []string{"title", "department_id", "location_mode", "travel_frequency", "visa_sponsorship", "employment_type", "pay.type"} {
		if !hasPath(errs, p) {
			t.Errorf("missing required error for %s", p)
		}
	}
	d := complete()
	if errs := Required(&d, Options{RequirePay: true}); len(errs) != 0 {
		t.Errorf("complete details rejected: %v", errs)
	}
	d.Pay = Pay{}
	if errs := Required(&d, Options{RequirePay: false}); len(errs) != 0 {
		t.Errorf("pay must be optional for roles that cannot set it: %v", errs)
	}
	d.Pay = Pay{Type: "range", Currency: "GBP", Period: "year", Min: ptr(int64(1))}
	if errs := Required(&d, Options{RequirePay: true}); !hasPath(errs, "pay.max") {
		t.Errorf("a range needs a maximum: %v", errs)
	}
	d = complete()
	d.Markets = nil
	if errs := Required(&d, Options{}); !hasPath(errs, "markets") {
		t.Errorf("a specific-area job needs a place: %v", errs)
	}
	d.LocationMode = LocationSpecificTimezone
	d.Markets = []MarketSel{{MarketCode: "UK"}}
	if errs := Required(&d, Options{}); !hasPath(errs, "markets") {
		t.Errorf("a time zone job needs a time zone: %v", errs)
	}
	d.LocationMode = LocationAnywhere
	d.Markets = nil
	if errs := Required(&d, Options{}); len(errs) != 0 {
		t.Errorf("an anywhere job needs no places: %v", errs)
	}
}

func TestMergeLeavesOriginalAndHandlesPay(t *testing.T) {
	cur := complete()
	cur.Description = Sections{"role": json.RawMessage(`"old"`), "benefits": json.RawMessage(`"keep"`)}
	patch, err := DecodePatch([]byte(`{
		"title": "  Staff Engineer ",
		"description_sections": {"role": "new", "benefits": null},
		"pay": {"min": 6000000},
		"skills": [{"custom_label": "Go"}],
		"use_company_address": true
	}`))
	if err != nil {
		t.Fatal(err)
	}
	got := Merge(cur, patch)
	if got.Title != "Staff Engineer" || !got.UseCompanyAddress {
		t.Errorf("scalar merge wrong: %+v", got)
	}
	if string(got.Description["role"]) != `"new"` {
		t.Errorf("role = %s", got.Description["role"])
	}
	if _, ok := got.Description["benefits"]; ok {
		t.Error("null should clear a description section")
	}
	if *got.Pay.Min != 6000000 || *got.Pay.Max != 7000000 || got.Pay.Currency != "GBP" {
		t.Errorf("pay merge wrong: %+v", got.Pay)
	}
	if cur.Title != "Senior Backend Engineer" || string(cur.Description["role"]) != `"old"` || *cur.Pay.Min != 5000000 {
		t.Error("Merge must not modify the original")
	}

	exact, _ := DecodePatch([]byte(`{"pay":{"type":"exact","min":123}}`))
	if p := Merge(cur, exact).Pay; *p.Max != 123 || p.Type != "exact" {
		t.Errorf("an exact amount has max = min: %+v", p)
	}
	clear, _ := DecodePatch([]byte(`{"pay":{"type":""}}`))
	if p := Merge(cur, clear).Pay; p.Min != nil || p.Max != nil || p.Type != "" {
		t.Errorf("clearing the pay type clears the amounts: %+v", p)
	}
	if _, err := DecodePatch([]byte(`{"status":"published"}`)); err == nil {
		t.Error("unknown fields must be rejected: status cannot be set through a patch")
	}
	if !patch.TouchesPay() || patch.TouchesRetention() {
		t.Error("TouchesPay/TouchesRetention wrong")
	}
}

func TestWarnings(t *testing.T) {
	d := complete()
	d.Pay.Min = ptr(int64(0))
	d.LawfulBasis = "consent"
	got := map[string]bool{}
	for _, w := range Warnings(&d) {
		got[w.Code] = true
	}
	if !got["pay_min_zero"] || !got["lawful_basis_consent"] {
		t.Errorf("warnings = %v", got)
	}
	d = complete()
	d.Pay.Max = ptr(int64(20000000))
	if w := Warnings(&d); len(w) != 1 || w[0].Code != "pay_range_wide" {
		t.Errorf("wide range warning missing: %v", w)
	}
	d = complete()
	if w := Warnings(&d); len(w) != 0 {
		t.Errorf("unexpected warnings: %v", w)
	}
}

func testCatalog() MarketCatalog {
	return MarketCatalog{
		"UK": {Code: "UK", Name: "United Kingdom", IsOpen: true},
		"US": {Code: "US", Name: "United States", IsOpen: true, Warnings: []Warning{{Code: "us_state_rules", Message: "State rules apply."}}},
		"EU": {Code: "EU", Name: "European Union", IsOpen: false},
		"KE": {Code: "KE", Name: "Kenya", IsOpen: false, MinRetentionMonths: 12, Warnings: []Warning{{Code: "ke_s50", Message: "Section 50."}}},
		"IN": {Code: "IN", Name: "India", IsOpen: true, MinRetentionMonths: 12, Warnings: []Warning{{Code: "in_dpdp", Message: "DPDP."}}},
	}
}

func TestValidateMarkets(t *testing.T) {
	cat := testCatalog()
	t.Run("clean and dedupe", func(t *testing.T) {
		got, warn, errs := ValidateMarkets(LocationSpecificArea, []MarketSel{
			{MarketCode: " uk "}, {MarketCode: "UK"}, {MarketCode: "us", Subdivision: "ca", City: " San Francisco "},
		}, cat)
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		if len(got) != 2 || got[0].MarketCode != "UK" || got[1].Subdivision != "CA" || got[1].City != "San Francisco" {
			t.Errorf("clean = %+v", got)
		}
		if len(warn) != 1 || warn[0].Market != "US" {
			t.Errorf("warnings = %+v", warn)
		}
	})
	t.Run("closed and unknown markets", func(t *testing.T) {
		_, _, errs := ValidateMarkets(LocationSpecificArea, []MarketSel{{MarketCode: "EU"}, {MarketCode: "XX"}, {MarketCode: "KE"}}, cat)
		for _, p := range []string{"markets[0].market_code", "markets[1].market_code", "markets[2].market_code"} {
			if !hasPath(errs, p) {
				t.Errorf("missing error at %s: %v", p, errs)
			}
		}
	})
	t.Run("anywhere takes no places and warns for every open market", func(t *testing.T) {
		_, _, errs := ValidateMarkets(LocationAnywhere, []MarketSel{{MarketCode: "UK"}}, cat)
		if !hasPath(errs, "markets") {
			t.Errorf("anywhere with places should be rejected: %v", errs)
		}
		_, warn, errs := ValidateMarkets(LocationAnywhere, nil, cat)
		if len(errs) != 0 || len(warn) != 2 {
			t.Errorf("want US and IN warnings, got %v / %v", warn, errs)
		}
	})
	t.Run("time zones", func(t *testing.T) {
		_, _, errs := ValidateMarkets(LocationSpecificTimezone, []MarketSel{{MarketCode: "UK"}}, cat)
		if !hasPath(errs, "markets[0].timezone") {
			t.Errorf("time zone job needs zones: %v", errs)
		}
		_, _, errs = ValidateMarkets(LocationSpecificTimezone, []MarketSel{{MarketCode: "UK", Timezone: "Europe/London"}}, cat)
		if len(errs) != 0 {
			t.Errorf("valid zone rejected: %v", errs)
		}
		_, _, errs = ValidateMarkets(LocationSpecificTimezone, []MarketSel{{MarketCode: "UK", Timezone: "Mars/Olympus"}}, cat)
		if !hasPath(errs, "markets[0].timezone") {
			t.Errorf("unknown zone accepted: %v", errs)
		}
		_, _, errs = ValidateMarkets(LocationSpecificArea, []MarketSel{{MarketCode: "UK", Timezone: "Europe/London"}}, cat)
		if !hasPath(errs, "markets[0].timezone") {
			t.Errorf("zone on an area job accepted: %v", errs)
		}
	})
	t.Run("bad subdivision and too many", func(t *testing.T) {
		_, _, errs := ValidateMarkets(LocationSpecificArea, []MarketSel{{MarketCode: "US", Subdivision: "CA; DROP"}}, cat)
		if !hasPath(errs, "markets[0].subdivision") {
			t.Errorf("bad subdivision accepted: %v", errs)
		}
		many := make([]MarketSel, MaxMarkets+1)
		for i := range many {
			many[i] = MarketSel{MarketCode: "UK", City: strings.Repeat("c", i+1)}
		}
		if _, _, errs := ValidateMarkets(LocationSpecificArea, many, cat); !hasPath(errs, "markets") {
			t.Errorf("too many places accepted: %v", errs)
		}
	})
}

func TestMinRetention(t *testing.T) {
	cat := testCatalog()
	if got := MinRetention([]MarketSel{{MarketCode: "UK"}}, LocationSpecificArea, cat); got != 0 {
		t.Errorf("UK min = %d", got)
	}
	if got := MinRetention([]MarketSel{{MarketCode: "UK"}, {MarketCode: "IN"}}, LocationSpecificArea, cat); got != 12 {
		t.Errorf("UK+IN min = %d", got)
	}
	if got := MinRetention(nil, LocationAnywhere, cat); got != 12 {
		t.Errorf("anywhere min = %d, want the strictest open market", got)
	}
}

func TestCompletedAndNextStep(t *testing.T) {
	d := complete()
	d.LawfulBasis = "contract"
	got := Completed(&d)
	want := []string{SectionBasics, SectionMarkets, SectionCompensation, SectionDataProtection}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("completed = %v, want %v", got, want)
	}
	if got := Completed(ptr(NewDetails())); len(got) != 0 {
		t.Errorf("blank draft completed %v", got)
	}
	if s := NextStep(nil, false); s != StepDetails {
		t.Errorf("blank step = %s", s)
	}
	if s := NextStep(want, false); s != StepApplicationForm {
		t.Errorf("step = %s", s)
	}
	if s := NextStep(want, true); s != StepPermissions {
		t.Errorf("step = %s", s)
	}
	if m := MergeSections([]string{"a", "b"}, []string{"b", "c"}); strings.Join(m, ",") != "a,b,c" {
		t.Errorf("merge = %v", m)
	}
}

func TestDuplicateKeys(t *testing.T) {
	a := NormaliseTitle("Senior Engineer (Backend)")
	b := NormaliseTitle("  senior   ENGINEER - backend ")
	if a != b || a != "senior engineer backend" {
		t.Errorf("keys %q vs %q", a, b)
	}
	if NormaliseTitle("Développeur Sénior") != "développeur sénior" {
		t.Error("accents must survive")
	}
	if NormaliseTitle("C++ Dev") == NormaliseTitle("C Dev") {
		// punctuation is dropped on purpose; "C++" and "C" collide, which only produces a warning.
		t.Log("C++ and C collide, which is accepted for a warning-only check")
	}
	l1 := LocationKey(LocationSpecificArea, []MarketSel{{MarketCode: "uk"}, {MarketCode: "US", Subdivision: "ca", City: "SF"}})
	l2 := LocationKey(LocationSpecificArea, []MarketSel{{MarketCode: "US", Subdivision: "CA", City: "sf"}, {MarketCode: "UK"}})
	if l1 != l2 {
		t.Errorf("order and case must not matter: %q vs %q", l1, l2)
	}
	if LocationKey(LocationAnywhere, nil) == l1 || LocationKey(LocationSpecificArea, []MarketSel{{MarketCode: "UK"}}) == l1 {
		t.Error("different places must give different keys")
	}
	if DuplicateWarning(nil) != nil || DuplicateWarning([]Duplicate{{ID: "1"}}) == nil {
		t.Error("warning only when duplicates exist")
	}
}

func TestPlainText(t *testing.T) {
	s := Sections{
		"role":          json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Build APIs"}]}]}`),
		"benefits":      json.RawMessage(`"Free lunch"`),
		"about_company": json.RawMessage(`{"children":[{"text":"We make HR"}]}`),
	}
	got := PlainText(s)
	if got != "We make HR\n\nBuild APIs\n\nFree lunch" {
		t.Errorf("text = %q", got)
	}
	if PlainText(nil) != "" {
		t.Error("empty description has no text")
	}
}

func TestDiff(t *testing.T) {
	a := complete()
	b := complete()
	b.Title = "Staff Engineer"
	b.Pay.Visible = false
	b.Description = Sections{"role": json.RawMessage(`"x"`)}
	got := Diff(&a, &b)
	for _, k := range []string{"title", "pay", "description_sections"} {
		if _, ok := got[k]; !ok {
			t.Errorf("diff missing %s: %v", k, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("unexpected diff keys: %v", got)
	}
	if len(Diff(&a, &a)) != 0 {
		t.Error("identical details must not differ")
	}
}

func TestTimezones(t *testing.T) {
	zones := Timezones(now)
	if len(zones) < 300 {
		t.Fatalf("only %d zones", len(zones))
	}
	by := map[string]Timezone{}
	for _, z := range zones {
		by[z.Name] = z
	}
	if z := by["Europe/London"]; z.Offset != "+01:00" { // BST on 8 Oct 2026
		t.Errorf("London = %+v", z)
	}
	if z := by["Africa/Lagos"]; z.Offset != "+01:00" {
		t.Errorf("Lagos = %+v", z)
	}
	if z := by["Asia/Kolkata"]; z.Offset != "+05:30" {
		t.Errorf("Kolkata = %+v", z)
	}
	if !ValidTimezone("UTC") || ValidTimezone("Mars/Base") {
		t.Error("ValidTimezone wrong")
	}
	for i := 1; i < len(zones); i++ {
		if zones[i-1].OffsetSeconds > zones[i].OffsetSeconds {
			t.Fatal("zones must be sorted by offset")
		}
	}
	offsets := UTCOffsets(zones)
	if len(offsets) < 20 || offsets[0].OffsetSeconds >= offsets[len(offsets)-1].OffsetSeconds {
		t.Errorf("offsets = %d", len(offsets))
	}
	if FormatOffset(-12600) != "-03:30" || FormatOffset(0) != "+00:00" {
		t.Error("FormatOffset wrong")
	}
}

func TestBuildPublicHidesInternals(t *testing.T) {
	j := &Job{JobCode: "JB-7", DepartmentName: "Engineering", Details: complete()}
	j.Description = Sections{"role": json.RawMessage(`"Build things"`)}
	j.AssessmentURL = "https://assess.example.com/x"
	j.Pay.Visible = true
	form := []questions.FormQuestion{
		{ID: "q1", Section: "screening", Type: "single_choice", Label: "Right to work?", Required: true,
			Config:   json.RawMessage(`{"options":[{"id":"y","label":"Yes"},{"id":"n","label":"No"}]}`),
			Knockout: json.RawMessage(`{"option_ids":["n"],"reason":"secret"}`)},
		{ID: "q2", Section: "eligibility", Type: "single_choice", Label: "Disability?", SpecialCategory: "health_disability"},
	}
	pub := BuildPublic(PublicInput{Job: j, Company: "Acme Ltd", Seniority: "Senior", Form: form, SkillLabels: []string{"Go"}})
	raw, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, leak := range []string{"knockout", "secret", "assess.example.com", "lawful_basis", "retention", "created_by", "revision"} {
		if strings.Contains(s, leak) {
			t.Errorf("public view leaks %q: %s", leak, s)
		}
	}
	if pub.Pay == nil || pub.Pay.Min != 5000000 || pub.Pay.Max != 7000000 {
		t.Errorf("pay = %+v", pub.Pay)
	}
	if !pub.Questions[1].Sensitive || pub.Questions[0].Sensitive {
		t.Error("special category questions are flagged sensitive")
	}
	if pub.Company != "Acme Ltd" || pub.Department != "Engineering" || pub.Seniority != "Senior" || pub.Locations[0].MarketCode != "UK" {
		t.Errorf("fields wrong: %+v", pub)
	}
	if !pub.AssessmentLinkSet {
		t.Error("the page needs to know an assessment is linked, without the link")
	}
	j.Pay.Visible = false
	if BuildPublic(PublicInput{Job: j}).Pay != nil {
		t.Error("hidden pay must not appear")
	}
	j.Pay.Visible = true
	j.Pay.Max = nil
	if BuildPublic(PublicInput{Job: j}).Pay != nil {
		t.Error("incomplete range must not appear")
	}
	if empty := BuildPublic(PublicInput{Job: &Job{}}); empty.Questions == nil || empty.Skills == nil || empty.Locations == nil {
		t.Error("lists must serialise as [] not null")
	}
}
