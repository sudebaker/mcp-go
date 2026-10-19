package executor

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

// The router resolves an array argument into the tool process only when the
// server injects it through the request's `_resources` map, and nothing in
// internal/ populates that map: every tool falls back to reading `__files__`
// from the arguments. `file_uris` used to be declared in several tool manifests
// as an array of strings, so a client that believed the schema (or the error
// message that named it) sent an argument no process ever read.
//
// These tests pin the contract at the manifest layer, which is the schema the
// server advertises when TOOLS_APPEND is false — the layer that had drifted.

const (
	configSchemaPath = "../../configs/config.yaml"
	manifestsGlob    = "../../tools/*/tool.yaml"
)

type toolManifest struct {
	Name        string                 `yaml:"name"`
	InputSchema map[string]interface{} `yaml:"input_schema"`
}

type toolSchemaDoc struct {
	Tools []struct {
		Name        string                 `yaml:"name"`
		InputSchema map[string]interface{} `yaml:"input_schema"`
	} `yaml:"tools"`
}

func schemaProperties(t *testing.T, name string, schema map[string]interface{}) map[string]bool {
	t.Helper()
	props := map[string]bool{}
	raw, ok := schema["properties"]
	if !ok {
		return props
	}
	casted, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("%s: properties is %T, want map", name, raw)
	}
	for key := range casted {
		props[key] = true
	}
	return props
}

func loadManifests(t *testing.T) []toolManifest {
	t.Helper()
	paths, err := filepath.Glob(manifestsGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", manifestsGlob, err)
	}
	if len(paths) == 0 {
		t.Fatalf("no tool manifests matched %s", manifestsGlob)
	}

	out := make([]toolManifest, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var m toolManifest
		if err := yaml.Unmarshal(raw, &m); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if m.Name == "" {
			t.Fatalf("%s has no name", path)
		}
		out = append(out, m)
	}
	return out
}

func loadConfigSchemas(t *testing.T) map[string]map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(configSchemaPath)
	if err != nil {
		t.Fatalf("read %s: %v", configSchemaPath, err)
	}
	var doc toolSchemaDoc
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), &doc); err != nil {
		t.Fatalf("parse %s: %v", configSchemaPath, err)
	}
	schemas := make(map[string]map[string]interface{}, len(doc.Tools))
	for _, tool := range doc.Tools {
		schemas[tool.Name] = tool.InputSchema
	}
	return schemas
}

// TestToolManifests_DoNotDeclareInertFileUris fails when a manifest advertises
// the file_uris argument again: the process would never read it.
func TestToolManifests_DoNotDeclareInertFileUris(t *testing.T) {
	for _, m := range loadManifests(t) {
		if schemaProperties(t, m.Name, m.InputSchema)["file_uris"] {
			t.Errorf("tools/*/tool.yaml for %q declares file_uris, but no tool process reads that argument (only __files__ from arguments or injected _resources)", m.Name)
		}
	}
}

// TestToolManifests_MatchConfiguredProperties guards the duplication between
// configs/config.yaml and tools/*/tool.yaml for the tools whose file contract was
// migrated: the same tool must not advertise a different set of arguments
// depending on TOOLS_APPEND.
func TestToolManifests_MatchConfiguredProperties(t *testing.T) {
	configured := loadConfigSchemas(t)
	manifests := loadManifests(t)

	checked := 0
	for _, m := range manifests {
		cfgSchema, ok := configured[m.Name]
		if !ok {
			continue // tools that only exist as a manifest are out of scope
		}
		checked++

		manifestProps := schemaProperties(t, m.Name, m.InputSchema)
		configProps := schemaProperties(t, m.Name, cfgSchema)
		if !sameKeys(manifestProps, configProps) {
			t.Errorf("%q advertises different arguments depending on the source: manifest-only=%v config-only=%v",
				m.Name, keysOnly(manifestProps, configProps), keysOnly(configProps, manifestProps))
		}
	}
	if checked == 0 {
		t.Fatal("no manifest matched a configs/config.yaml tool; the comparison would be vacuous")
	}
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func keysOnly(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
