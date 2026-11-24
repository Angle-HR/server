package hiringhttp

import (
	"os"
	"strings"
)

// EnvApplicationsRemote, set to "on", stops the monolith serving the applications routes (public apply and
// uploads, a job's applications, applicant settings). They are then served by cmd/applications, and the edge
// (web-bff, UPSTREAM_APPLICATIONS_URL) sends them there. Off by default: the monolith serves everything.
const EnvApplicationsRemote = "APPLICATIONS_REMOTE"

func applicationsRemote() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvApplicationsRemote)), "on")
}

// EnvHiringRemote, set to "on", stops the monolith serving the hiring routes: jobs, drafts, forms, templates,
// departments, catalog, settings, lifecycle, the DPIA route, the careers page and sitemap.
// They are then served by cmd/hiring, and the edge (web-bff, UPSTREAM_HIRING_URL) sends them there. The
// applications routes follow APPLICATIONS_REMOTE independently. Off by default.
const EnvHiringRemote = "HIRING_REMOTE"

// HiringRemote reports whether HIRING_REMOTE=on.
func HiringRemote() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvHiringRemote)), "on")
}

// EnvOrgRemote, set to "on", stops the monolith serving the company agreements and privacy-contact routes
// (/organization/agreements, /organization/privacy-contact): cmd/org serves them. The same variable switches the
// org handlers in internal/orghttp. /organization/dpia stays with hiring.
const EnvOrgRemote = "ORG_REMOTE"

func orgRemote() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvOrgRemote)), "on") }
