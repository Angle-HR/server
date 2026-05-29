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
