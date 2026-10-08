package gates

import (
	"fmt"

	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
)

// Issue codes.
const (
	CodeCompanyNotVerified    = "company_not_verified"
	CodeDPANotAccepted        = "dpa_not_accepted"
	CodePrivacyContactMissing = "privacy_contact_missing"
	CodeDetailsIncomplete     = "details_incomplete"
	CodeNoMarket              = "no_market"
	CodeMarketClosed          = "market_closed"
	CodeLawfulBasisMissing    = "lawful_basis_missing"
	CodeLIAMissing            = "lia_reference_missing"
	CodeLawfulBasisConsent    = "lawful_basis_consent"
	CodeGateUnconfirmed       = "gate_unconfirmed"
	CodeGateStale             = "gate_stale"
	CodeDPIAMissing           = "dpia_missing"
	CodeScreeningDisabled     = "automated_screening_disabled"
	CodeScreeningOutOfScope   = "screening_not_confirmed"
	CodeSpecialCategory       = "special_category_undeclared"
	CodeWordingFlag           = "wording_flag"
	CodeFormMissing           = "form_missing"
	CodeRetentionTooShort     = "retention_below_minimum"
	CodeScreeningRuleInvalid  = "screening_rule_invalid"
)

// Issue is one reason a job cannot publish (Blocking) or a note shown before publishing (Warning).
type Issue struct {
	Code     string `json:"code"`
	Path     string `json:"path,omitempty"`
	GateID   string `json:"gate_id,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report is the result of the publish checks, the same list the dry run shows and publish enforces.
type Report struct {
	Blocking []Issue `json:"blocking"`
	Warnings []Issue `json:"warnings"`
	Gates    []State `json:"gates"`
}

// OK reports whether the job may publish.
func (r *Report) OK() bool { return len(r.Blocking) == 0 }

// Input is everything the publish checks read. The service loads it; this package decides.
type Input struct {
	Job        *jobs.Job
	Form       []questions.FormQuestion
	Catalog    jobs.MarketCatalog
	Gates      []Gate
	Confirms   []Confirmation
	Categories []string // special categories declared for the job

	// Company state.
	CompanyVerified      bool
	DPAAccepted          bool
	PrivacyContactSet    bool
	ScreeningEnabled     bool // Legal turned automated screening on for the company
	DPIARecorded         bool // the company's DPIA exists
	JobScreeningInScope  bool // this job's owner confirmed the DPIA covers it
	HasDisqualifyRules   bool
	UnresolvedWordingFlg []string // wording flags with no logged override (PL-07 list; empty until it exists)
	RuleProblems         []string // screening rules that no longer match the saved form
}

// UsesScreening reports whether the job asks for automated screening: any knockout on a question, or any
// disqualification rule.
func UsesScreening(form []questions.FormQuestion, hasRules bool) bool {
	if hasRules {
		return true
	}
	for i := range form {
		if len(form[i].Knockout) > 0 && string(form[i].Knockout) != "null" {
			return true
		}
	}
	return false
}

// Check runs every publish check. Drafting is never gated; this runs on publish, resume, reopen and on edits to
// a published job that touch markets, pay, screening or special categories.
func Check(in *Input) *Report {
	r := &Report{Blocking: []Issue{}, Warnings: []Issue{}, Gates: []State{}}
	block := func(code, path, gate, msg string) {
		r.Blocking = append(r.Blocking, Issue{Code: code, Path: path, GateID: gate, Severity: SeverityRequired, Message: msg})
	}
	warn := func(code, path, gate, sev, msg string) {
		r.Warnings = append(r.Warnings, Issue{Code: code, Path: path, GateID: gate, Severity: sev, Message: msg})
	}

	// Company prerequisites. No role overrides these.
	if !in.CompanyVerified {
		block(CodeCompanyNotVerified, "", "", "Verify your company before publishing. You can keep drafting.")
	}
	if !in.DPAAccepted {
		block(CodeDPANotAccepted, "", "", "Accept the current terms and data processing agreement. Only a Founder or Legal can.")
	}
	if !in.PrivacyContactSet {
		block(CodePrivacyContactMissing, "", "", "Add a privacy contact for applicants in your company settings.")
	}

	j := in.Job
	d := &j.Details
	for _, fe := range jobs.Required(d, jobs.Options{RequirePay: true}) {
		block(CodeDetailsIncomplete, fe.Path, "", fe.Message)
	}
	if len(in.Form) == 0 {
		block(CodeFormMissing, "application_form", "", "Save the application form before publishing.")
	}

	places := PlacesFor(d.LocationMode, d.Markets, in.Catalog)
	if len(places) == 0 {
		block(CodeNoMarket, "markets", "", "Pick where you are hiring.")
	}
	for _, p := range places {
		if m, ok := in.Catalog[p.MarketCode]; !ok || !m.IsOpen {
			block(CodeMarketClosed, "markets", "", fmt.Sprintf("Hiring in %s is not available yet.", p.MarketCode))
		}
	}

	switch {
	case d.LawfulBasis == "":
		block(CodeLawfulBasisMissing, "lawful_basis", "", "Choose the lawful basis you rely on for processing applications.")
	case d.LawfulBasis == "legitimate_interests" && d.LIAReference == "":
		block(CodeLIAMissing, "lia_reference", "", "Legitimate interests needs a reference to your legitimate interests assessment.")
	case d.LawfulBasis == "consent":
		warn(CodeLawfulBasisConsent, "lawful_basis", "", SeverityAdvisory,
			"Consent for hiring is hard to rely on because candidates can withdraw it.")
	}
	if min := jobs.MinRetention(d.Markets, d.LocationMode, in.Catalog); min > d.RetentionMonths {
		block(CodeRetentionTooShort, "retention_months", "", fmt.Sprintf("These markets need retention of at least %d months.", min))
	}

	// Special categories: every tagged question needs a declaration.
	declared := make(map[string]bool, len(in.Categories))
	for _, c := range in.Categories {
		declared[c] = true
	}
	for i := range in.Form {
		if c := in.Form[i].SpecialCategory; c != "" && !declared[c] {
			block(CodeSpecialCategory, fmt.Sprintf("questions[%d].special_category", i), "",
				"Record the legal condition for collecting "+c+" before publishing.")
		}
	}

	// Automated screening: any rule that flags candidates needs Legal's switch, the company DPIA and the job's
	// own confirmation that it is within the DPIA's scope.
	if UsesScreening(in.Form, in.HasDisqualifyRules) {
		switch {
		case !in.ScreeningEnabled:
			block(CodeScreeningDisabled, "", "", "Automated screening is switched off for your company. Legal turns it on.")
		case !in.DPIARecorded:
			block(CodeDPIAMissing, "", "", "Legal must record the company DPIA before a screening job publishes.")
		case !in.JobScreeningInScope:
			block(CodeScreeningOutOfScope, "", "", "Confirm this job's screening is within the scope of your DPIA.")
		}
	}

	for _, p := range in.RuleProblems {
		block(CodeScreeningRuleInvalid, "disqualification_rules", "", p)
	}
	for _, f := range in.UnresolvedWordingFlg {
		block(CodeWordingFlag, "", "", "Wording flagged: "+f+". Change it, or ask a Founder, HR 1 or Legal to override.")
	}

	// Compliance gates for the job's markets.
	ctx := &Context{
		Places: places, SpecialCategories: in.Categories, LawfulBasis: d.LawfulBasis,
		AutomatedScreening: UsesScreening(in.Form, in.HasDisqualifyRules),
	}
	r.Gates = Evaluate(Applicable(in.Gates, ctx), in.Confirms, d)
	for _, g := range r.Gates {
		if g.Confirmed {
			continue
		}
		switch {
		case g.Severity == SeverityRequired && g.Stale:
			block(CodeGateStale, "", g.ID, g.ID+": the wording changed since you confirmed it. Confirm it again.")
		case g.Severity == SeverityRequired:
			msg := g.Requirement
			if g.Detail != "" {
				msg = g.Detail
			}
			block(CodeGateUnconfirmed, "", g.ID, msg)
		case g.Severity == SeverityAdvisory:
			warn(CodeGateUnconfirmed, "", g.ID, SeverityAdvisory, g.Requirement)
		}
	}

	for _, w := range jobs.Warnings(d) {
		if w.Code != CodeLawfulBasisConsent { // reported above
			warn(w.Code, "", "", SeverityAdvisory, w.Message)
		}
	}
	return r
}

// TouchesGates reports whether an edit to a published job must run the publish checks again: markets, pay,
// screening, special categories (7 Oct answer).
func TouchesGates(before, after *jobs.Details, formChanged bool) bool {
	if formChanged {
		return true
	}
	if before.LocationMode != after.LocationMode || len(before.Markets) != len(after.Markets) {
		return true
	}
	for i := range before.Markets {
		if before.Markets[i] != after.Markets[i] {
			return true
		}
	}
	if !samePay(before.Pay, after.Pay) {
		return true
	}
	return before.LawfulBasis != after.LawfulBasis || before.RetentionMonths != after.RetentionMonths
}

func samePay(a, b jobs.Pay) bool {
	eq := func(x, y *int64) bool { return (x == nil && y == nil) || (x != nil && y != nil && *x == *y) }
	return a.Type == b.Type && a.Currency == b.Currency && a.Period == b.Period && a.Visible == b.Visible &&
		eq(a.Min, b.Min) && eq(a.Max, b.Max)
}
