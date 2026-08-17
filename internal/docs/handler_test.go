package docs

import (
	"encoding/json"
	"testing"
)

func TestPatchOpenAPISpec(t *testing.T) {
	t.Parallel()

	const basePath = "/api/v1"
	input := []byte(`{
		"swagger": "2.0",
		"host": "localhost:8080",
		"basePath": "` + basePath + `",
		"schemes": ["http"]
	}`)

	patched, err := patchOpenAPISpec(input, "api.example.com", "https")
	if err != nil {
		t.Fatalf("patchOpenAPISpec: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(patched, &doc); err != nil {
		t.Fatalf("unmarshal patched spec: %v", err)
	}

	if doc["host"] != "api.example.com" {
		t.Fatalf("host: got %v, want api.example.com", doc["host"])
	}

	if doc["basePath"] != basePath {
		t.Fatalf("basePath: got %v, want %s", doc["basePath"], basePath)
	}

	schemes, ok := doc["schemes"].([]any)
	if !ok || len(schemes) != 1 || schemes[0] != "https" {
		t.Fatalf("schemes: got %v, want [https]", doc["schemes"])
	}

	assertAPITagGroups(t, doc)
}

func assertAPITagGroups(t *testing.T, doc map[string]any) {
	t.Helper()

	groups, ok := doc["x-tagGroups"].([]any)
	if !ok || len(groups) != 3 {
		t.Fatalf("x-tagGroups: got %v, want Waitlist, Onboarding, and Admin groups", doc["x-tagGroups"])
	}

	assertTagGroup(t, groups[0], "Waitlist", []string{
		"waitlist/reference",
		"waitlist/signup",
	})
	assertTagGroup(t, groups[1], "Onboarding", []string{
		"auth",
		"onboarding/reference",
		"onboarding/profile",
		"onboarding/address",
		"onboarding/business",
		"onboarding/individual",
		"onboarding/session",
	})
	assertTagGroup(t, groups[2], "Admin", []string{
		"admin/auth",
		"admin/waitlist",
		"admin/users",
		"admin/catalogs",
		"admin/jobs",
		"admin/staff",
		"admin/roles",
		"admin/audit",
	})

	tagDefs, ok := doc["tags"].([]any)
	if !ok || len(tagDefs) != 17 {
		t.Fatalf("tags: got %v, want 17 tag definitions", doc["tags"])
	}
	wantTagNames := []string{
		"waitlist/reference",
		"waitlist/signup",
		"auth",
		"onboarding/reference",
		"onboarding/profile",
		"onboarding/address",
		"onboarding/business",
		"onboarding/individual",
		"onboarding/session",
		"admin/auth",
		"admin/waitlist",
		"admin/users",
		"admin/catalogs",
		"admin/jobs",
		"admin/staff",
		"admin/roles",
		"admin/audit",
	}
	for i, want := range wantTagNames {
		tagDef, ok := tagDefs[i].(map[string]any)
		if !ok {
			t.Fatalf("tags[%d]: got %T, want map[string]any", i, tagDefs[i])
		}
		if tagDef["name"] != want {
			t.Fatalf("tags[%d] name: got %v, want %v", i, tagDef["name"], want)
		}
		if tagDef["x-displayName"] == nil || tagDef["x-displayName"] == "" {
			t.Fatalf("tags[%d] x-displayName: missing", i)
		}
	}
}

func assertTagGroup(t *testing.T, groupAny any, name string, wantTags []string) {
	t.Helper()

	group, ok := groupAny.(map[string]any)
	if !ok {
		t.Fatalf("tag group: got %T, want map[string]any", groupAny)
	}
	if group["name"] != name {
		t.Fatalf("tag group name: got %v, want %v", group["name"], name)
	}

	groupTags, ok := group["tags"].([]any)
	if !ok {
		t.Fatalf("tag group %q tags: got %T, want []any", name, group["tags"])
	}
	if len(groupTags) != len(wantTags) {
		t.Fatalf("tag group %q tags: got %v, want %v", name, groupTags, wantTags)
	}
	for i, want := range wantTags {
		if groupTags[i] != want {
			t.Fatalf("tag group %q tags[%d]: got %v, want %v", name, i, groupTags[i], want)
		}
	}
}

func TestApplyAPITagGroups(t *testing.T) {
	t.Parallel()

	doc := map[string]any{}
	ApplyAPITagGroups(doc)
	assertAPITagGroups(t, doc)
}

func TestIsEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		appEnv string
		want   bool
	}{
		{"development", true},
		{"staging", true},
		{"test", true},
		{"production", false},
	}

	for _, tc := range tests {
		if got := IsEnabled(tc.appEnv); got != tc.want {
			t.Errorf("IsEnabled(%q) = %v, want %v", tc.appEnv, got, tc.want)
		}
	}
}

func TestSpecURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		origin string
		want   string
	}{
		{"http://localhost:8080", "http://localhost:8080/openapi.json"},
		{"https://api.example.com/", "https://api.example.com/openapi.json"},
	}

	for _, tc := range tests {
		if got := specURL(tc.origin); got != tc.want {
			t.Errorf("specURL(%q) = %q, want %q", tc.origin, got, tc.want)
		}
	}
}
