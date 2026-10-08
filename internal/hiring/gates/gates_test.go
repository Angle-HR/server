package gates

import (
	"encoding/json"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
)

func i64(v int64) *int64 { return &v }

var catalog = jobs.MarketCatalog{
	"UK": {Code: "UK", IsOpen: true},
	"US": {Code: "US", IsOpen: true},
	"EU": {Code: "EU", IsOpen: false},
	"KE": {Code: "KE", IsOpen: false, MinRetentionMonths: 12},
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

var gateCatalog = []Gate{
	{ID: "uk-lawful", MarketCode: "UK", Severity: SeverityRequired, Requirement: "Lawful basis recorded.", Version: 1, Trigger: raw(`{"type":"always"}`)},
	{ID: "uk-scd", MarketCode: "UK", Severity: SeverityRequired, Requirement: "Condition recorded.", Version: 2, Trigger: raw(`{"type":"special_category_any","values":["health_disability","criminal_records"]}`)},
	{ID: "eu-pay-trans", MarketCode: "EU", Severity: SeverityRequired, Version: 1, Trigger: raw(`{"type":"always"}`)},
	{ID: "us-pay-trans", MarketCode: "US", Severity: SeverityRequired, Version: 1, Trigger: raw(`{"type":"subdivision_in","values":["CA","CO","NYC"]}`)},
	{ID: "us-fair-chance", MarketCode: "US", Severity: SeverityRequired, Version: 1, Trigger: raw(`{"type":"special_category","value":"criminal_records","subdivision_in":["CA"]}`)},
	{ID: "us-aedt", MarketCode: "US", Severity: SeverityAdvisory, Version: 1, Trigger: raw(`{"type":"automated_screening","subdivision_in":["NYC"]}`)},
	{ID: "eu-rep", Platform: true, Severity: SeverityRequired, Version: 1, Trigger: raw(`{"type":"always"}`)},
	{ID: "bad-trigger", MarketCode: "UK", Severity: SeverityAdvisory, Version: 1, Trigger: raw(`{not json`)},
}

func ids(g []Gate) map[string]bool {
	m := map[string]bool{}
	for _, x := range g {
		m[x.ID] = true
	}
	return m
}

func TestApplicable(t *testing.T) {
	t.Run("UK only", func(t *testing.T) {
		got := ids(Applicable(gateCatalog, &Context{Places: []jobs.MarketSel{{MarketCode: "UK"}}}))
		if !got["uk-lawful"] || got["uk-scd"] || got["eu-pay-trans"] || got["eu-rep"] {
			t.Errorf("got %v", got)
		}
		if !got["bad-trigger"] {
			t.Error("an unreadable trigger must apply the gate")
		}
	})
	t.Run("special category", func(t *testing.T) {
		got := ids(Applicable(gateCatalog, &Context{
			Places: []jobs.MarketSel{{MarketCode: "UK"}}, SpecialCategories: []string{"criminal_records"}}))
		if !got["uk-scd"] {
			t.Error("uk-scd should apply")
		}
	})
	t.Run("US state rules", func(t *testing.T) {
		tx := ids(Applicable(gateCatalog, &Context{Places: []jobs.MarketSel{{MarketCode: "US", Subdivision: "TX"}}}))
		ca := ids(Applicable(gateCatalog, &Context{Places: []jobs.MarketSel{{MarketCode: "US", Subdivision: "CA"}}}))
		nyc := ids(Applicable(gateCatalog, &Context{Places: []jobs.MarketSel{{MarketCode: "US", Subdivision: "NY", City: "New York City"}}}))
		if tx["us-pay-trans"] || !ca["us-pay-trans"] || !nyc["us-pay-trans"] {
			t.Errorf("tx=%v ca=%v nyc=%v", tx, ca, nyc)
		}
	})
	t.Run("remote US follows the strictest rule", func(t *testing.T) {
		got := ids(Applicable(gateCatalog, &Context{Places: []jobs.MarketSel{{MarketCode: "US"}}}))
		if !got["us-pay-trans"] {
			t.Error("a US place with no state must match state rules")
		}
	})
	t.Run("fair chance needs the category in CA", func(t *testing.T) {
		ca := Context{Places: []jobs.MarketSel{{MarketCode: "US", Subdivision: "CA"}}, SpecialCategories: []string{"criminal_records"}}
		tx := Context{Places: []jobs.MarketSel{{MarketCode: "US", Subdivision: "TX"}}, SpecialCategories: []string{"criminal_records"}}
		none := Context{Places: []jobs.MarketSel{{MarketCode: "US", Subdivision: "CA"}}}
		if !ids(Applicable(gateCatalog, &ca))["us-fair-chance"] || ids(Applicable(gateCatalog, &tx))["us-fair-chance"] ||
			ids(Applicable(gateCatalog, &none))["us-fair-chance"] {
			t.Error("fair chance trigger")
		}
	})
	t.Run("aedt only for NYC with screening", func(t *testing.T) {
		c := Context{Places: []jobs.MarketSel{{MarketCode: "US", Subdivision: "NY", City: "NYC"}}, AutomatedScreening: true}
		off := Context{Places: c.Places}
		if !ids(Applicable(gateCatalog, &c))["us-aedt"] || ids(Applicable(gateCatalog, &off))["us-aedt"] {
			t.Error("aedt trigger")
		}
	})
}

func TestPlacesForAnywhere(t *testing.T) {
	got := PlacesFor(jobs.LocationAnywhere, nil, catalog)
	if len(got) != 2 || got[0].MarketCode != "UK" || got[1].MarketCode != "US" {
		t.Errorf("anywhere covers open markets only: %+v", got)
	}
	sel := []jobs.MarketSel{{MarketCode: "EU"}}
	if got := PlacesFor(jobs.LocationSpecificArea, sel, catalog); len(got) != 1 {
		t.Errorf("specific area: %+v", got)
	}
}

func TestEvaluate(t *testing.T) {
	g := []Gate{
		{ID: "uk-lawful", Severity: SeverityRequired, Version: 2},
		{ID: "uk-privacy", Severity: SeverityRequired, Version: 1},
		{ID: "eu-pay-trans", Severity: SeverityRequired, Version: 1},
	}
	d := &jobs.Details{Pay: jobs.Pay{Type: "range", Currency: "EUR", Period: "year", Min: i64(1), Max: i64(2), Visible: false}}
	st := Evaluate(g, []Confirmation{
		{GateID: "uk-lawful", Version: 1, Confirmed: true}, // older wording
		{GateID: "uk-privacy", Version: 1, Confirmed: true},
		{GateID: "eu-pay-trans", Version: 1, Confirmed: true}, // customers cannot confirm an auto gate
	}, d)
	if st[0].Confirmed || !st[0].Stale {
		t.Errorf("stale confirmation must not count: %+v", st[0])
	}
	if !st[1].Confirmed {
		t.Error("current confirmation counts")
	}
	if !st[2].Auto || st[2].Confirmed {
		t.Errorf("hidden pay fails the auto gate regardless of a stored confirmation: %+v", st[2])
	}
	d.Pay.Visible = true
	if st := Evaluate(g[2:], nil, d); !st[0].Confirmed {
		t.Error("visible range passes")
	}
	d.Pay.Type = "exact"
	if st := Evaluate(g[2:], nil, d); st[0].Confirmed {
		t.Error("an exact figure is not a range")
	}
}

// ---- publish checks ----

func goodInput() *Input {
	j := &jobs.Job{ID: "j1", Details: jobs.NewDetails()}
	d := &j.Details
	d.Title, d.DepartmentID = "Engineer", "d1"
	d.LocationMode = jobs.LocationSpecificArea
	d.Markets = []jobs.MarketSel{{MarketCode: "UK"}}
	d.TravelFrequency, d.VisaSponsorship, d.EmploymentType = "none", "no", "full_time"
	d.Pay = jobs.Pay{Type: "range", Currency: "GBP", Period: "year", Min: i64(50000), Max: i64(70000), Visible: true}
	d.LawfulBasis = "contract"
	return &Input{
		Job: j, Form: questions.DefaultForm(), Catalog: catalog, Gates: gateCatalog,
		Confirms:        []Confirmation{{GateID: "uk-lawful", Version: 1, Confirmed: true}, {GateID: "bad-trigger", Version: 1, Confirmed: true}},
		CompanyVerified: true, DPAAccepted: true, PrivacyContactSet: true,
	}
}

func codes(is []Issue) map[string]bool {
	m := map[string]bool{}
	for _, i := range is {
		m[i.Code] = true
	}
	return m
}

func TestCheckHappyPath(t *testing.T) {
	r := Check(goodInput())
	if !r.OK() {
		t.Fatalf("blocking: %+v", r.Blocking)
	}
}

func TestCheckCompanyPrerequisites(t *testing.T) {
	in := goodInput()
	in.CompanyVerified, in.DPAAccepted, in.PrivacyContactSet = false, false, false
	got := codes(Check(in).Blocking)
	for _, c := range []string{CodeCompanyNotVerified, CodeDPANotAccepted, CodePrivacyContactMissing} {
		if !got[c] {
			t.Errorf("missing %s in %v", c, got)
		}
	}
}

func TestCheckRequiredGateUnconfirmed(t *testing.T) {
	in := goodInput()
	in.Confirms = nil
	r := Check(in)
	if !codes(r.Blocking)[CodeGateUnconfirmed] {
		t.Errorf("unconfirmed required gate must block: %+v", r.Blocking)
	}
	// An advisory gate never blocks.
	if !codes(r.Warnings)[CodeGateUnconfirmed] {
		t.Errorf("advisory gate should warn: %+v", r.Warnings)
	}
	for _, b := range r.Blocking {
		if b.GateID == "bad-trigger" {
			t.Error("advisory gate blocked")
		}
	}
}

func TestCheckClosedMarketAndRetention(t *testing.T) {
	in := goodInput()
	in.Job.Markets = []jobs.MarketSel{{MarketCode: "KE"}}
	got := codes(Check(in).Blocking)
	if !got[CodeMarketClosed] || !got[CodeRetentionTooShort] {
		t.Errorf("got %v", got)
	}
}

func TestCheckLawfulBasis(t *testing.T) {
	in := goodInput()
	in.Job.LawfulBasis = ""
	if !codes(Check(in).Blocking)[CodeLawfulBasisMissing] {
		t.Error("lawful basis required")
	}
	in.Job.LawfulBasis = "legitimate_interests"
	if !codes(Check(in).Blocking)[CodeLIAMissing] {
		t.Error("LIA reference required")
	}
	in.Job.LawfulBasis, in.Job.LIAReference = "consent", ""
	r := Check(in)
	if !r.OK() || !codes(r.Warnings)[CodeLawfulBasisConsent] {
		t.Errorf("consent warns but does not block: %+v %+v", r.Blocking, r.Warnings)
	}
}

func TestCheckAutomatedScreening(t *testing.T) {
	ko := questions.FormQuestion{Section: "screening", Type: "number", Label: "Years", Knockout: raw(`{"min":1}`)}
	in := goodInput()
	in.Form = append(in.Form, ko)
	steps := []struct {
		name   string
		mutate func()
		want   string
	}{
		{"switched off", func() {}, CodeScreeningDisabled},
		{"no dpia", func() { in.ScreeningEnabled = true }, CodeDPIAMissing},
		{"out of scope", func() { in.DPIARecorded = true }, CodeScreeningOutOfScope},
		{"ok", func() { in.JobScreeningInScope = true }, ""},
	}
	for _, s := range steps {
		s.mutate()
		r := Check(in)
		if s.want == "" {
			if !r.OK() {
				t.Errorf("%s: %+v", s.name, r.Blocking)
			}
			continue
		}
		if !codes(r.Blocking)[s.want] {
			t.Errorf("%s: want %s in %+v", s.name, s.want, r.Blocking)
		}
	}
	// Rules alone also count as screening.
	in2 := goodInput()
	in2.HasDisqualifyRules = true
	if !codes(Check(in2).Blocking)[CodeScreeningDisabled] {
		t.Error("disqualification rules need screening enabled")
	}
}

func TestCheckSpecialCategoryNeedsDeclaration(t *testing.T) {
	in := goodInput()
	in.Form = append(in.Form, questions.FormQuestion{Section: "eligibility", Type: "short_text", Label: "x", SpecialCategory: "health_disability"})
	if !codes(Check(in).Blocking)[CodeSpecialCategory] {
		t.Error("undeclared category must block")
	}
	in.Categories = []string{"health_disability"}
	if codes(Check(in).Blocking)[CodeSpecialCategory] {
		t.Error("declared category passes")
	}
}

func TestCheckEUPayDisclosure(t *testing.T) {
	in := goodInput()
	in.Catalog = jobs.MarketCatalog{"EU": {Code: "EU", IsOpen: true}}
	in.Job.Markets = []jobs.MarketSel{{MarketCode: "EU"}}
	in.Job.Pay.Visible = false
	in.Confirms = nil
	r := Check(in)
	found := false
	for _, b := range r.Blocking {
		if b.GateID == "eu-pay-trans" {
			found = true
		}
	}
	if !found {
		t.Errorf("hidden pay must block an EU job: %+v", r.Blocking)
	}
}

func TestCheckWordingFlags(t *testing.T) {
	in := goodInput()
	in.UnresolvedWordingFlg = []string{"young and energetic"}
	if !codes(Check(in).Blocking)[CodeWordingFlag] {
		t.Error("unresolved wording flag blocks")
	}
}

func TestTouchesGates(t *testing.T) {
	a := goodInput().Job.Details
	b := a
	if TouchesGates(&a, &b, false) {
		t.Error("same details, no change")
	}
	b.Title = "Other"
	if TouchesGates(&a, &b, false) {
		t.Error("title edits do not rerun gates")
	}
	b = a
	b.Markets = []jobs.MarketSel{{MarketCode: "US"}}
	if !TouchesGates(&a, &b, false) {
		t.Error("markets rerun gates")
	}
	b = a
	b.Pay.Min = i64(1)
	if !TouchesGates(&a, &b, false) {
		t.Error("pay reruns gates")
	}
	b = a
	if !TouchesGates(&a, &b, true) {
		t.Error("form changes rerun gates")
	}
	// Equal pay values behind different pointers are the same pay.
	b = a
	b.Pay.Min, b.Pay.Max = i64(*a.Pay.Min), i64(*a.Pay.Max)
	if TouchesGates(&a, &b, false) {
		t.Error("pointer identity must not matter")
	}
}
