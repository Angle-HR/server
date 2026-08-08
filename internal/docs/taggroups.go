package docs

// API documentation is grouped in Scalar under top-level folders (x-tagGroups).
// Waitlist covers pre-launch signup flows; Onboarding covers product signup and setup.
var waitlistTagGroup = map[string]any{
	"name": "Waitlist",
	"tags": []any{
		"waitlist/reference",
		"waitlist/signup",
		"waitlist/onboarding",
	},
}

var onboardingTagGroup = map[string]any{
	"name": "Onboarding",
	"tags": []any{
		"auth",
		"onboarding/reference",
		"onboarding/profile",
		"onboarding/address",
		"onboarding/business",
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
		"description":   "Reference data for waitlist forms: countries, industries, hiring tools, roles, and team sizes.",
		"x-displayName": "Reference",
	},
	{
		"name":          "waitlist/signup",
		"description":   "Initial waitlist registration. Creates a regional waitlist entry and global users_registry row.",
		"x-displayName": "Signup",
	},
	{
		"name":          "waitlist/onboarding",
		"description":   "Waitlist onboarding form submission after signup (distinct from product onboarding).",
		"x-displayName": "Waitlist onboarding",
	},
	{
		"name":          "auth",
		"description":   "Product signup, 6-digit email verification (OTP expires in 300s, resend cooldown 30s), login, and token refresh.",
		"x-displayName": "Auth",
	},
	{
		"name":          "onboarding/reference",
		"description":   "Product onboarding catalog endpoints (business types, industries, company roles). Separate from waitlist reference data.",
		"x-displayName": "Reference",
	},
	{
		"name":          "onboarding/profile",
		"description":   "Account type (`individual` or `business`) and profile fields. Individual accounts set region from country_id.",
		"x-displayName": "Profile",
	},
	{
		"name":          "onboarding/address",
		"description":   "Workspace address (search or manual entry). `POST /onboarding/address/verify` is reserved and returns 501.",
		"x-displayName": "Address",
	},
	{
		"name":          "onboarding/business",
		"description":   "Business compliance step: business type, industry, employee count. Business accounts only.",
		"x-displayName": "Business",
	},
	{
		"name":          "onboarding/session",
		"description":   "Onboarding progress and completion. Individual: verify_email → profile → address. Business: also requires business step.",
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
