// Command patch-openapi applies API tag-group metadata to generated OpenAPI files.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/Angle-HR/server/internal/docs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// specFileMode keeps the generated API spec world-readable.
const specFileMode = 0o644

func run() error {
	specDir := filepath.Join("internal", "docs", "spec")
	jsonPath := filepath.Join(specDir, "swagger.json")
	yamlPath := filepath.Join(specDir, "swagger.yaml")

	jsonContent, err := os.ReadFile(jsonPath) //nolint:gosec // G304: fixed in-repo path built from constants
	if err != nil {
		return fmt.Errorf("read %s: %w", jsonPath, err)
	}

	var doc map[string]any
	if unmarshalErr := json.Unmarshal(jsonContent, &doc); unmarshalErr != nil {
		return fmt.Errorf("unmarshal %s: %w", jsonPath, unmarshalErr)
	}

	docs.ApplyAPITagGroups(doc)

	patchedJSON, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", jsonPath, err)
	}
	patchedJSON = append(patchedJSON, '\n')
	if writeErr := os.WriteFile(jsonPath, patchedJSON, specFileMode); writeErr != nil {
		return fmt.Errorf("write %s: %w", jsonPath, writeErr)
	}

	yamlContent, err := os.ReadFile(yamlPath) //nolint:gosec // G304: fixed in-repo path built from constants
	if err != nil {
		return fmt.Errorf("read %s: %w", yamlPath, err)
	}

	var yamlDoc map[string]any
	if unmarshalErr := yaml.Unmarshal(yamlContent, &yamlDoc); unmarshalErr != nil {
		return fmt.Errorf("unmarshal %s: %w", yamlPath, unmarshalErr)
	}

	docs.ApplyAPITagGroups(yamlDoc)

	patchedYAML, err := yaml.Marshal(yamlDoc)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", yamlPath, err)
	}
	if writeErr := os.WriteFile(yamlPath, patchedYAML, specFileMode); writeErr != nil {
		return fmt.Errorf("write %s: %w", yamlPath, writeErr)
	}

	return nil
}
