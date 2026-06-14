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

var apiTagDefinitions = []map[string]any{
	{
		"name":          "waitlist/reference",
		"description":   "Reference data for waitlist forms (countries, industries, hiring tools, roles, and team sizes).",
		"x-displayName": "Reference",
	},
	{
		"name":          "waitlist/signup",
		"description":   "Initial waitlist registration.",
		"x-displayName": "Signup",
	},
	{
		"name":          "waitlist/onboarding",
		"description":   "Waitlist onboarding form submission after signup (distinct from product onboarding).",
		"x-displayName": "Waitlist onboarding",
	},
	{
		"name":          "auth",
		"description":   "Product signup, email verification, login, and token refresh.",
		"x-displayName": "Auth",
	},
	{
		"name":          "onboarding/reference",
		"description":   "Product onboarding catalogs (business types, industries, company roles).",
		"x-displayName": "Reference",
	},
	{
		"name":          "onboarding/profile",
		"description":   "Account type and profile fields.",
		"x-displayName": "Profile",
	},
	{
		"name":          "onboarding/address",
		"description":   "Workspace address collection.",
		"x-displayName": "Address",
	},
	{
		"name":          "onboarding/business",
		"description":   "Business compliance step (business accounts only).",
		"x-displayName": "Business",
	},
	{
		"name":          "onboarding/session",
		"description":   "Onboarding progress and completion.",
		"x-displayName": "Session",
	},
}

// ApplyAPITagGroups injects Scalar x-tagGroups and tag metadata into an OpenAPI document.
func ApplyAPITagGroups(doc map[string]any) {
	doc["x-tagGroups"] = []any{waitlistTagGroup, onboardingTagGroup}

	tags := make([]any, len(apiTagDefinitions))
	for i, tag := range apiTagDefinitions {
		tags[i] = tag
	}
	doc["tags"] = tags
}
