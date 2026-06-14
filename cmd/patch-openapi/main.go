// Command patch-openapi applies API tag-group metadata to generated OpenAPI files.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Angle-HR/server/internal/docs"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	specDir := filepath.Join("internal", "docs", "spec")
	jsonPath := filepath.Join(specDir, "swagger.json")
	yamlPath := filepath.Join(specDir, "swagger.yaml")

	jsonContent, err := os.ReadFile(jsonPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", jsonPath, err)
	}

	var doc map[string]any
	if err := json.Unmarshal(jsonContent, &doc); err != nil {
		return fmt.Errorf("unmarshal %s: %w", jsonPath, err)
	}

	docs.ApplyAPITagGroups(doc)

	patchedJSON, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", jsonPath, err)
	}
	patchedJSON = append(patchedJSON, '\n')
	if err := os.WriteFile(jsonPath, patchedJSON, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", jsonPath, err)
	}

	yamlContent, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", yamlPath, err)
	}

	var yamlDoc map[string]any
	if err := yaml.Unmarshal(yamlContent, &yamlDoc); err != nil {
		return fmt.Errorf("unmarshal %s: %w", yamlPath, err)
	}

	docs.ApplyAPITagGroups(yamlDoc)

	patchedYAML, err := yaml.Marshal(yamlDoc)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", yamlPath, err)
	}
	if err := os.WriteFile(yamlPath, patchedYAML, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", yamlPath, err)
	}

	return nil
}
