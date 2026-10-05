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

func run() (err error) {
	specDir := filepath.Join("internal", "docs", "spec")
	root, err := os.OpenRoot(specDir)
	if err != nil {
		return fmt.Errorf("open %s: %w", specDir, err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", specDir, closeErr)
		}
	}()

	const jsonName = "swagger.json"
	const yamlName = "swagger.yaml"

	jsonContent, err := root.ReadFile(jsonName)
	if err != nil {
		return fmt.Errorf("read %s: %w", jsonName, err)
	}

	var doc map[string]any
	if unmarshalErr := json.Unmarshal(jsonContent, &doc); unmarshalErr != nil {
		return fmt.Errorf("unmarshal %s: %w", jsonName, unmarshalErr)
	}

	docs.ApplyAPITagGroups(doc)

	patchedJSON, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", jsonName, err)
	}
	patchedJSON = append(patchedJSON, '\n')
	if writeErr := root.WriteFile(jsonName, patchedJSON, specFileMode); writeErr != nil {
		return fmt.Errorf("write %s: %w", jsonName, writeErr)
	}

	yamlContent, err := root.ReadFile(yamlName)
	if err != nil {
		return fmt.Errorf("read %s: %w", yamlName, err)
	}

	var yamlDoc map[string]any
	if unmarshalErr := yaml.Unmarshal(yamlContent, &yamlDoc); unmarshalErr != nil {
		return fmt.Errorf("unmarshal %s: %w", yamlName, unmarshalErr)
	}

	docs.ApplyAPITagGroups(yamlDoc)

	patchedYAML, err := yaml.Marshal(yamlDoc)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", yamlName, err)
	}
	if writeErr := root.WriteFile(yamlName, patchedYAML, specFileMode); writeErr != nil {
		return fmt.Errorf("write %s: %w", yamlName, writeErr)
	}

	return nil
}
