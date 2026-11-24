package bff

import (
	"net/http"
	"net/url"
	"strings"
)

// Route sends the requests Match accepts to another upstream than the default (the monolith). Match gets the
// request path with the API prefix removed.
type Route struct {
	Name   string
	Target *url.URL
	Match  func(path string) bool
}

// ApplicationsPath reports whether the path belongs to the applications service: public apply and uploads,
// the company side of a job's applications, and the applicant settings.
func ApplicationsPath(p string) bool {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case len(parts) >= 4 && parts[0] == "public" && parts[1] == "jobs" && (parts[3] == "applications" || parts[3] == "uploads"):
		return true
	case len(parts) >= 3 && parts[0] == "jobs" && parts[2] == "applications":
		return true
	case len(parts) == 2 && parts[0] == "hiring" && parts[1] == "applicant-settings":
		return true
	}
	return false
}

// HiringPath reports whether the path belongs to the hiring service: jobs and their lifecycle, drafts, forms,
// templates, departments, catalog and settings (/jobs..., /hiring...), the DPIA route (/organization/dpia; the
// agreements and privacy-contact routes belong to the org service), the public job JSON and sitemap (/public/jobs/sitemap.xml,
// /public/jobs/{public_id}) and the careers pages (/j/{public_id}, /j/{public_id}/privacy). The applications
// paths are never hiring paths, so the two route sets are disjoint and the order of the routes does not matter.
func HiringPath(p string) bool {
	if ApplicationsPath(p) {
		return false
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch parts[0] {
	case "jobs", "hiring":
		return true
	case "public":
		return len(parts) == 3 && parts[1] == "jobs"
	case "j":
		return len(parts) == 2 || len(parts) == 3 && parts[2] == "privacy"
	case "organization":
		return len(parts) == 2 && parts[1] == "dpia"
	}
	return false
}

// orgOnboarding are the /onboarding/<name> routes of the org service, public and signed-in. The legacy
// per-account-type submits (individual, business) are included.
var orgOnboarding = map[string]bool{
	"business-types": true, "industries": true, "company-roles": true, "identification-requirements": true,
	"status": true, "profile": true, "address": true, "compliance": true, "business": true, "complete": true,
	"individual": true,
}

// orgVerification are the /organization/verification/<name> routes.
var orgVerification = map[string]bool{
	"retry": true, "confirm-name": true, "confirm-address": true, "change-country": true,
}

// OrgPath reports whether the path belongs to the org service. Every path is listed exactly (no prefix match), so
// a new route in another service never lands here by accident:
//
//	GET  /countries
//	     /onboarding/{business-types,industries,company-roles,identification-requirements,status,profile,address,
//	                  compliance,business,complete,individual}, /onboarding/address/{search,verify}
//	     /organization/verification and /organization/verification/{retry,confirm-name,confirm-address,change-country}
//	     /organization/agreements, /organization/privacy-contact
//
// Not here: /organization/dpia (hiring), /organizations/invites and /auth/* (identity), /admin/* (admin BFF).
func OrgPath(p string) bool {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch parts[0] {
	case "countries":
		return len(parts) == 1
	case "onboarding":
		switch len(parts) {
		case 2:
			return orgOnboarding[parts[1]]
		case 3:
			return parts[1] == "address" && (parts[2] == "search" || parts[2] == "verify")
		}
	case "organization":
		switch len(parts) {
		case 2:
			return parts[1] == "verification" || parts[1] == "agreements" || parts[1] == "privacy-contact"
		case 3:
			return parts[1] == "verification" && orgVerification[parts[2]]
		}
	}
	return false
}

// IdentityPath reports whether the path belongs to the identity service: the product auth routes (/auth/*,
// including session start, /auth/me and TOTP), org-invite creation, and the identity half of the admin API (admin
// auth, users, staff, roles, permissions, audit logs and the KYB verification queue). The admin catalogs are served by the identity
// service too.
func IdentityPath(p string) bool {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch parts[0] {
	case "auth":
		return len(parts) >= 2
	case "organizations":
		return len(parts) == 2 && parts[1] == "invites"
	case "admin":
		if len(parts) < 2 {
			return false
		}
		if parts[1] == "auth" {
			return len(parts) >= 3
		}
		return adminIdentityCollection(parts[1])
	}
	return false
}

func adminIdentityCollection(name string) bool {
	switch name {
	case "users", "staff", "roles", "permissions", "audit-logs", "verification", "companies", "catalogs":
		return true
	}
	return false
}

// routed picks the proxy by path: the first matching route, else the default.
func routed(prefix string, def http.Handler, routes []Route, proxies []http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, prefix)
		for i, rt := range routes {
			if rt.Match(p) {
				proxies[i].ServeHTTP(w, r)
				return
			}
		}
		def.ServeHTTP(w, r)
	})
}
