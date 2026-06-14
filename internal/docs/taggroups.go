package docs

// API documentation is grouped in Scalar under top-level folders (x-tagGroups).
// Waitlist covers pre-launch signup flows; Onboarding is reserved for post-waitlist
// product onboarding endpoints not yet implemented.
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
		// Add onboarding/<domain> tags here as handlers are implemented.
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
