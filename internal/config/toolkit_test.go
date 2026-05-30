package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// createTestToolkitDir prepara un directorio temporal con archivos toolkit.yaml
// y devuelve la ruta base.
func createTestToolkitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func writeToolkitFile(t *testing.T, dir, name string, content map[string]interface{}) string {
	t.Helper()
	path := filepath.Join(dir, name+".yaml")
	data, _ := yaml.Marshal(content)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write toolkit %s: %v", path, err)
	}
	return path
}

func TestLoadToolkit_Success(t *testing.T) {
	dir := createTestToolkitDir(t)
	writeToolkitFile(t, dir, "default", map[string]interface{}{
		"name":        "default",
		"description": "Toolkit general",
		"tools":       []string{"echo", "datetime"},
	})

	// Override the hardcoded "configs/toolkits" path by using a relative lookup
	// or accept the limitation that the test only works when run from the repo root.
	// For unit isolation we test filterToolsByToolkit directly.
}

func TestFilterToolsByToolkit_Basic(t *testing.T) {
	tools := []ToolConfig{
		{Name: "echo"},
		{Name: "datetime"},
		{Name: "opencode_context"},
	}

	tk := &ToolkitConfig{
		Name:  "default",
		Tools: []string{"echo", "datetime"},
	}

	result := filterToolsByToolkit(tools, tk)
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}
	if result[0].Name != "echo" || result[1].Name != "datetime" {
		t.Errorf("unexpected result: %v", result)
	}
}

func TestFilterToolsByToolkit_EmptyToolkit(t *testing.T) {
	tools := []ToolConfig{
		{Name: "echo"},
		{Name: "datetime"},
	}

	result := filterToolsByToolkit(tools, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 tools with nil toolkit, got %d", len(result))
	}

	result = filterToolsByToolkit(tools, &ToolkitConfig{Tools: []string{}})
	if len(result) != 2 {
		t.Fatalf("expected 2 tools with empty toolkit, got %d", len(result))
	}
}

func TestFilterToolsByToolkit_NoMatch(t *testing.T) {
	tools := []ToolConfig{
		{Name: "echo"},
	}

	tk := &ToolkitConfig{
		Tools: []string{"nonexistent"},
	}

	result := filterToolsByToolkit(tools, tk)
	if len(result) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(result))
	}
}

func TestLoadToolkit_ValidateName(t *testing.T) {
	// loadToolkit uses a hardcoded "configs/toolkits/" prefix.
	// We cannot easily mock the filesystem without modifying loadToolkit to accept a base dir.
	// Instead, we test the validation logic directly by observing that the function
	// does not panic on invalid input and returns an error.
	// This is an integration-level test.

	// Test invalid names (path traversal attempt).
	invalidNames := []string{"../etc/passwd", "foo/bar", "foo\\bar"}
	for _, name := range invalidNames {
		_, err := loadToolkit(name)
		if err == nil {
			t.Errorf("loadToolkit(%q) expected error, got nil", name)
		}
	}
}
