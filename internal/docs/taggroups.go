package docs

// API documentation is grouped in Scalar under top-level folders (x-tagGroups).
// Waitlist covers pre-launch signup flows; Onboarding covers product signup and setup.
var waitlistTagGroup = map[string]any{
	"name": "Waitlist",
	"tags": []any{
		"waitlist/reference",
		"waitlist/signup",
	},
}

var onboardingTagGroup = map[string]any{
	"name": "Onboarding",
	"tags": []any{
		"auth",
		"onboarding/reference",
		"onboarding/profile",
		"onboarding/address",
		"onboarding/compliance",
		"onboarding/business",
		"onboarding/individual",
		"onboarding/session",
	},
}

var adminTagGroup = map[string]any{
	"name": "Admin",
	"tags": []any{
		"admin/auth",
		"admin/waitlist",
		"admin/users",
		"admin/catalogs",
		"admin/jobs",
		"admin/staff",
		"admin/roles",
		"admin/audit",
	},
}

var apiTagDefinitions = []map[string]any{
	{
		"name":          "waitlist/reference",
		"description":   "Reference data for waitlist and onboarding forms: countries.",
		"x-displayName": "Reference",
	},
	{
		"name":          "waitlist/signup",
		"description":   "Email-only waitlist registration. Creates a regional waitlist entry and global waitlist.registry row.",
		"x-displayName": "Signup",
	},
	{
		"name":          "auth",
		"description":   "Product signup, email verification OTP (not for passwordless login), password login, passwordless login OTP (/auth/login/otp/*), TOTP MFA, password reset, logout, /auth/me, and product org invites.",
		"x-displayName": "Auth",
	},
	{
		"name":          "onboarding/reference",
		"description":   "Product onboarding catalog endpoints (business types, industries, company roles, identification requirements). Separate from waitlist reference data.",
		"x-displayName": "Reference",
	},
	{
		"name":          "onboarding/profile",
		"description":   "Canonical profile step: account type (`individual` or `business`) and profile fields via PUT /onboarding/profile. Individual accounts set region from country_id.",
		"x-displayName": "Profile",
	},
	{
		"name":          "onboarding/address",
		"description":   "Business identification and workspace address. POST /onboarding/address/search and POST /onboarding/address/verify accept payloads directly. Uses ADDRESS_SEARCH_MODE and ADDRESS_VERIFY_MODE passthrough in non-prod.",
		"x-displayName": "Address",
	},
	{
		"name":          "onboarding/compliance",
		"description":   "Canonical compliance step: PUT /onboarding/compliance (business_type_id, industry_id, employee_count) for individual and business accounts.",
		"x-displayName": "Compliance",
	},
	{
		"name":          "onboarding/business",
		"description":   "Deprecated alias for PUT /onboarding/compliance. POST /onboarding/business is deprecated (KYB-oriented one-shot).",
		"x-displayName": "Business",
	},
	{
		"name":          "onboarding/individual",
		"description":   "Deprecated one-shot POST /onboarding/individual. Prefer PUT /onboarding/profile with account_type individual.",
		"x-displayName": "Individual",
	},
	{
		"name":          "onboarding/session",
		"description":   "Onboarding progress and completion. Individual: verify_email → profile → compliance → complete. Business: verify_email → profile → identification_address → compliance → complete.",
		"x-displayName": "Session",
	},
	{
		"name":          "admin/auth",
		"description":   "Admin console login, invite acceptance, token refresh, and current admin profile with RBAC permissions.",
		"x-displayName": "Auth",
	},
	{
		"name":          "admin/waitlist",
		"description":   "Cross-region waitlist list, detail, flag updates, soft-delete, and restore.",
		"x-displayName": "Waitlist",
	},
	{
		"name":          "admin/users",
		"description":   "Product account directory via global registry and regional accounts.",
		"x-displayName": "Users",
	},
	{
		"name":          "admin/catalogs",
		"description":   "CMS for waitlist and product onboarding catalog tables.",
		"x-displayName": "Catalogs",
	},
	{
		"name":          "admin/jobs",
		"description":   "Fluvio job inspection and retry for email/upload queues.",
		"x-displayName": "Jobs",
	},
	{
		"name":          "admin/staff",
		"description":   "Invite and manage admin staff users and role assignments.",
		"x-displayName": "Staff",
	},
	{
		"name":          "admin/roles",
		"description":   "Create and manage custom roles, assign permissions from the seeded catalog.",
		"x-displayName": "Roles",
	},
	{
		"name":          "admin/audit",
		"description":   "Immutable audit trail of admin mutations.",
		"x-displayName": "Audit",
	},
}

// ApplyAPITagGroups injects Scalar x-tagGroups and tag metadata into an OpenAPI document.
func ApplyAPITagGroups(doc map[string]any) {
	doc["x-tagGroups"] = []any{waitlistTagGroup, onboardingTagGroup, adminTagGroup}

	tags := make([]any, len(apiTagDefinitions))
	for i, tag := range apiTagDefinitions {
		tags[i] = tag
	}
	doc["tags"] = tags
}
