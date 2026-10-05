// Package rbac holds the permission keys the server checks. Handlers check
// permissions, never role names, so role-to-permission mapping can change in data.
package rbac

// Permission is a named capability such as "job.publish.external".
type Permission string

// Job and form permissions used by the hiring domain.
const (
	JobCreate              Permission = "job.create"
	JobEditDraft           Permission = "job.edit.draft"
	JobEditPublished       Permission = "job.edit.published"
	JobPublishExternal     Permission = "job.publish.external"
	JobApprove             Permission = "job.approve"
	JobArchive             Permission = "job.archive"
	JobExport              Permission = "jobs.export"
	JobManageAny           Permission = "jobs.manage_any"
	JobViewAll             Permission = "job.view.all"
	JobViewAssigned        Permission = "job.view.assigned"
	JobAccessViewList      Permission = "job.access.view_list"
	JobJurisdictionSet     Permission = "job.jurisdiction.set"
	JobSalaryRangeSet      Permission = "job.salary_range.set"
	JobRetentionSet        Permission = "job.retention.set"
	JobResidencyRegionSet  Permission = "job.residency.region.set"
	FormAutomatedScreening Permission = "form.automated_screening.enable"
	FormEDIModuleEnable    Permission = "form.edi_module.enable"
	FormHealthQuestionAdd  Permission = "form.health_question.add"
	FormEDIViewAggregate   Permission = "form.edi.view_aggregate"
)

// Never-granted permissions: no endpoint exists for these, listed so a typo cannot grant them.
const (
	JobDeleteHard     Permission = "job.delete.hard"
	FormAskPayHistory Permission = "form.ask_pay_history"
	FormEDIViewNamed  Permission = "form.edi.view_named"
)

// Role is one of the seven organization roles. A member can hold several.
type Role string

// Organization roles.
const (
	RoleFounder     Role = "founder"
	RoleHR1         Role = "hr_1"
	RoleHR2         Role = "hr_2"
	RoleLineManager Role = "line_manager"
	RolePayroll     Role = "payroll"
	RoleEmployee    Role = "employee"
	RoleLegal       Role = "legal"
)

// Set is the resolved permissions of a caller, loaded once per request.
type Set map[Permission]struct{}

// NewSet builds a Set. Never-granted permissions are dropped.
func NewSet(perms ...Permission) Set {
	s := make(Set, len(perms))
	for _, p := range perms {
		if p == JobDeleteHard || p == FormAskPayHistory || p == FormEDIViewNamed {
			continue
		}
		s[p] = struct{}{}
	}
	return s
}

// Has reports whether the set grants p.
func (s Set) Has(p Permission) bool {
	_, ok := s[p]
	return ok
}
