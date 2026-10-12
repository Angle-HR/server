// Package gates decides which compliance gates apply to a job and whether the job may publish. The gate catalog
// is data (hiring.compliance_gates in the global database); this package reads a gate's trigger, matches it
// against the job, and combines the result with Open HR's own system checks (verified company, accepted DPA,
// pay disclosure, DPIA for automated screening). It has no database dependency.
//
// Gate wording is for counsel to sign off. Nothing here claims a job is compliant: a confirmed gate is the
// customer's own confirmation, logged against their user.
package gates

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Angle-HR/server/internal/hiring/jobs"
)

// Severities of a gate.
const (
	SeverityRequired      = "required"
	SeverityAdvisory      = "advisory"
	SeverityInformational = "informational"
)

// Trigger kinds stored in compliance_gates.trigger.
const (
	TriggerAlways               = "always"
	TriggerSubdivisionIn        = "subdivision_in"
	TriggerSpecialCategory      = "special_category"
	TriggerSpecialCategoryAny   = "special_category_any"
	TriggerAutomatedScreening   = "automated_screening"
	TriggerLawfulBasis          = "lawful_basis"
	TriggerAlwaysWhenNoSelector = ""
)

// Gate is one row of the catalog.
type Gate struct {
	ID          string          `json:"id"`
	MarketCode  string          `json:"market_code,omitempty"`
	Platform    bool            `json:"platform,omitempty"`
	Severity    string          `json:"severity"`
	Requirement string          `json:"requirement"`
	LegalBasis  string          `json:"legal_basis,omitempty"`
	Trigger     json.RawMessage `json:"-"`
	Version     int             `json:"version"`
}

// trigger is the decoded trigger JSON. Fields not used by a kind are ignored.
type trigger struct {
	Type          string   `json:"type"`
	Value         string   `json:"value"`
	Values        []string `json:"values"`
	SubdivisionIn []string `json:"subdivision_in"`
}

// Context is everything about a job that decides which gates apply.
type Context struct {
	// Places are the places the job is open to. For an "anywhere" job pass one entry per open market with no
	// subdivision (the strictest rules apply).
	Places             []jobs.MarketSel
	SpecialCategories  []string
	AutomatedScreening bool
	LawfulBasis        string
}

// PlacesFor expands a job's location into the places gates are matched against. An "anywhere" job covers every
// open market. A place with no subdivision matches every subdivision rule of its market: a remote US job
// follows the strictest state rule.
func PlacesFor(mode string, sel []jobs.MarketSel, cat jobs.MarketCatalog) []jobs.MarketSel {
	if mode != jobs.LocationAnywhere {
		return append([]jobs.MarketSel(nil), sel...)
	}
	codes := make([]string, 0, len(cat))
	for code, m := range cat {
		if m.IsOpen {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	out := make([]jobs.MarketSel, len(codes))
	for i, c := range codes {
		out[i] = jobs.MarketSel{MarketCode: c}
	}
	return out
}

// Applicable returns the gates that apply to the job, in catalog order (market, then id). Platform gates are
// Open HR's own obligations and never reach a customer, so they are skipped here.
func Applicable(catalog []Gate, c *Context) []Gate {
	var out []Gate
	for i := range catalog {
		g := catalog[i]
		if g.Platform || g.MarketCode == "" {
			continue
		}
		var places []jobs.MarketSel
		for _, p := range c.Places {
			if strings.EqualFold(p.MarketCode, g.MarketCode) {
				places = append(places, p)
			}
		}
		if len(places) == 0 {
			continue
		}
		if matches(g.Trigger, places, c) {
			out = append(out, g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MarketCode != out[j].MarketCode {
			return out[i].MarketCode < out[j].MarketCode
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func matches(raw json.RawMessage, places []jobs.MarketSel, c *Context) bool {
	var t trigger
	if len(raw) == 0 || json.Unmarshal(raw, &t) != nil {
		// An unreadable trigger applies the gate: failing closed is the safe side for a compliance check.
		return true
	}
	switch t.Type {
	case TriggerAlways, TriggerAlwaysWhenNoSelector:
		return true
	case TriggerSubdivisionIn:
		return anyPlaceIn(places, t.Values)
	case TriggerSpecialCategory:
		if !hasCategory(c.SpecialCategories, t.Value) {
			return false
		}
		return len(t.SubdivisionIn) == 0 || anyPlaceIn(places, t.SubdivisionIn)
	case TriggerSpecialCategoryAny:
		for _, v := range t.Values {
			if hasCategory(c.SpecialCategories, v) {
				return true
			}
		}
		return false
	case TriggerAutomatedScreening:
		if !c.AutomatedScreening {
			return false
		}
		return len(t.SubdivisionIn) == 0 || anyPlaceIn(places, t.SubdivisionIn)
	case TriggerLawfulBasis:
		return c.LawfulBasis == t.Value
	}
	return true // unknown trigger kind: fail closed
}

func hasCategory(have []string, want string) bool {
	for _, h := range have {
		if h == want {
			return true
		}
	}
	return false
}

// anyPlaceIn reports whether any place falls in one of the subdivisions. A place with no subdivision and no
// city is "the whole market" and matches, because the strictest rule applies. "NYC" matches a New York City
// place.
func anyPlaceIn(places []jobs.MarketSel, values []string) bool {
	for _, p := range places {
		if p.Subdivision == "" && p.City == "" {
			return true
		}
		for _, v := range values {
			if strings.EqualFold(p.Subdivision, v) {
				return true
			}
			if strings.EqualFold(v, "NYC") && isNYC(p) {
				return true
			}
		}
	}
	return false
}

func isNYC(p jobs.MarketSel) bool {
	c := strings.ToLower(strings.TrimSpace(p.City))
	return c == "nyc" || c == "new york city" || c == "new york, ny"
}

// ---- confirmation state ----

// Confirmation is a stored customer confirmation of one gate.
type Confirmation struct {
	GateID    string
	Version   int
	Confirmed bool
}

// State is one applicable gate with where it stands.
type State struct {
	Gate
	// Auto is true when the server answers the gate from the job's own data and nobody confirms it.
	Auto      bool   `json:"auto"`
	Confirmed bool   `json:"confirmed"`
	Stale     bool   `json:"stale,omitempty"` // confirmed against an older version of the wording
	Detail    string `json:"detail,omitempty"`
}

// Evaluate pairs each applicable gate with the job's confirmations. A confirmation for an older version of a
// gate no longer counts. Gates the server can check itself are answered from the job (see autoChecks).
func Evaluate(applicable []Gate, confirms []Confirmation, job *jobs.Details) []State {
	byID := make(map[string]Confirmation, len(confirms))
	for _, c := range confirms {
		byID[c.GateID] = c
	}
	out := make([]State, 0, len(applicable))
	for _, g := range applicable {
		st := State{Gate: g}
		if check, ok := autoChecks[g.ID]; ok {
			st.Auto = true
			st.Confirmed, st.Detail = check(job)
			out = append(out, st)
			continue
		}
		if c, ok := byID[g.ID]; ok && c.Confirmed {
			if c.Version == g.Version {
				st.Confirmed = true
			} else {
				st.Stale = true
			}
		}
		out = append(out, st)
	}
	return out
}

// autoChecks are gates whose answer is in the job's data. "Pay range is shown" must mean shown, not stored.
var autoChecks = map[string]func(*jobs.Details) (bool, string){
	"eu-pay-trans": payShown,
	"us-pay-trans": payShown,
	// Pay-history questions are rejected when a form is saved, in every market, so this gate holds by
	// construction.
	"us-salary-history": func(*jobs.Details) (bool, string) { return true, "" },
	"ke-retention": func(d *jobs.Details) (bool, string) {
		if d.RetentionMonths >= 12 {
			return true, ""
		}
		return false, "Kenyan applicant data is kept for 12 months; set retention to 12 months."
	},
}

func payShown(d *jobs.Details) (bool, string) {
	p := d.Pay
	switch {
	case !p.Complete():
		return false, "Add the pay range."
	case p.Type != "range":
		return false, "Show a pay range with a minimum and a maximum."
	case !p.Visible:
		return false, "The pay range is stored but hidden from the posting. Turn on 'show pay'."
	}
	return true, ""
}

// IsAuto reports whether the server answers a gate itself.
func IsAuto(gateID string) bool { _, ok := autoChecks[gateID]; return ok }

// Describe is a short label for logs and error messages.
func (g Gate) Describe() string { return fmt.Sprintf("%s (v%d)", g.ID, g.Version) }
