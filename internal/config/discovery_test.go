package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func createTempToolDir(t *testing.T, baseDir, toolName string, manifest []byte) string {
	t.Helper()
	dir := filepath.Join(baseDir, toolName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	manifestPath := filepath.Join(dir, "tool.yaml")
	if err := os.WriteFile(manifestPath, manifest, 0644); err != nil {
		t.Fatalf("write manifest %s: %v", manifestPath, err)
	}
	return dir
}

func writeDummyScript(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/usr/bin/env python3\nprint('ok')\n"), 0755); err != nil {
		t.Fatalf("write script %s: %v", path, err)
	}
	return path
}

func basicManifest(name, script string) []byte {
	m := map[string]interface{}{
		"name":        name,
		"description": "desc for " + name,
		"command":     "python3",
		"args":        []string{script},
		"input_schema": map[string]interface{}{
			"type": "object",
		},
	}
	b, _ := yaml.Marshal(m)
	return b
}

func TestDiscoverToolsFromDirectory_SingleTool(t *testing.T) {
	tmpDir := t.TempDir()
	dir := createTempToolDir(t, tmpDir, "echo", basicManifest("echo", "main.py"))
	writeDummyScript(t, dir, "main.py")

	tools, err := DiscoverToolsFromDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("expected name echo, got %s", tools[0].Name)
	}
	if tools[0].Description == "" {
		t.Error("expected non-empty description")
	}
	if tools[0].Command != "python3" {
		t.Errorf("expected command python3, got %s", tools[0].Command)
	}
	if len(tools[0].Args) != 1 || !filepath.IsAbs(tools[0].Args[0]) {
		t.Errorf("expected absolute path arg, got %v", tools[0].Args)
	}
}

func TestDiscoverToolsFromDirectory_MultipleTools(t *testing.T) {
	tmpDir := t.TempDir()
	d1 := createTempToolDir(t, tmpDir, "echo", basicManifest("echo", "main.py"))
	writeDummyScript(t, d1, "main.py")
	d2 := createTempToolDir(t, tmpDir, "datetime", basicManifest("datetime", "run.py"))
	writeDummyScript(t, d2, "run.py")

	tools, err := DiscoverToolsFromDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
	}
	if !names["echo"] || !names["datetime"] {
		t.Errorf("expected echo and datetime, got %v", names)
	}
}

func TestDiscoverToolsFromDirectory_IgnoreMissingManifest(t *testing.T) {
	tmpDir := t.TempDir()
	// A directory without tool.yaml should be ignored.
	if err := os.MkdirAll(filepath.Join(tmpDir, "no_manifest"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tools, err := DiscoverToolsFromDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(tools))
	}
}

func TestDiscoverToolsFromDirectory_IgnoreInvalidManifest(t *testing.T) {
	tmpDir := t.TempDir()
	// Malformed YAML inside a tool directory.
	dir := filepath.Join(tmpDir, "bad_tool")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte("{{bad\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tools, err := DiscoverToolsFromDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools after skipping invalid manifest, got %d", len(tools))
	}
}

func TestDiscoverToolsFromDirectory_ResolveRelativeArgs(t *testing.T) {
	tmpDir := t.TempDir()
	dir := createTempToolDir(t, tmpDir, "rel_tool", basicManifest("rel_tool", "sub/run.py"))
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	writeDummyScript(t, filepath.Join(dir, "sub"), "run.py")

	tools, err := DiscoverToolsFromDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	expected := filepath.Join(dir, "sub", "run.py")
	if tools[0].Args[0] != expected {
		t.Errorf("expected arg %s, got %s", expected, tools[0].Args[0])
	}
}

func TestDiscoverToolsFromDirectory_BlockPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := map[string]interface{}{
		"name":        "bad_tool",
		"description": "bad",
		"command":     "python3",
		"args":        []string{"../../../etc/passwd"},
		"input_schema": map[string]interface{}{
			"type": "object",
		},
	}
	b, _ := yaml.Marshal(manifest)
	createTempToolDir(t, tmpDir, "bad_tool", b)

	tools, err := DiscoverToolsFromDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools after blocking traversal, got %d", len(tools))
	}
}

func TestDiscoverToolsFromDirectory_AbsoluteArgNotResolved(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := map[string]interface{}{
		"name":        "abs_tool",
		"description": "desc",
		"command":     "python3",
		"args":        []string{"/usr/bin/python3"},
		"input_schema": map[string]interface{}{
			"type": "object",
		},
	}
	b, _ := yaml.Marshal(manifest)
	createTempToolDir(t, tmpDir, "abs_tool", b)

	tools, err := DiscoverToolsFromDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Args[0] != "/usr/bin/python3" {
		t.Errorf("expected arg unchanged, got %s", tools[0].Args[0])
	}
}

func TestMergeTools_AppendFalse(t *testing.T) {
	configured := []ToolConfig{{Name: "echo", Command: "c1"}, {Name: "date", Command: "c2"}}
	discovered := []ToolConfig{{Name: "echo", Command: "c1_new"}, {Name: "new_tool", Command: "c3"}}
	merged := mergeTools(configured, discovered, "false")

	order := []string{}
	for _, m := range merged {
		order = append(order, m.Name)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 tools, got %d: %v", len(order), order)
	}
	// discovered first, then configured not overridden.
	if order[0] != "echo" || order[1] != "new_tool" || order[2] != "date" {
		t.Errorf("unexpected order: %v", order)
	}
	if merged[0].Command != "c1_new" {
		t.Errorf("expected discovered echo to override configured, got %s", merged[0].Command)
	}
}

func TestMergeTools_AppendTrue(t *testing.T) {
	configured := []ToolConfig{{Name: "echo", Command: "c1"}, {Name: "date", Command: "c2"}}
	discovered := []ToolConfig{{Name: "echo", Command: "c1_new"}, {Name: "new_tool", Command: "c3"}}
	merged := mergeTools(configured, discovered, "true")

	order := []string{}
	for _, m := range merged {
		order = append(order, m.Name)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 tools, got %d: %v", len(order), order)
	}
	// configured first (they win), then new discovered.
	if order[0] != "echo" || order[1] != "date" || order[2] != "new_tool" {
		t.Errorf("unexpected order: %v", order)
	}
	if merged[0].Command != "c1" {
		t.Errorf("expected configured echo to win, got %s", merged[0].Command)
	}
}

func TestLoadConfig_ToolsDiscoveryNoneNoChange(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config_test_*.yaml")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	configContent := `
server:
  host: "0.0.0.0"
  port: 8080
  name: "test"
execution:
  default_timeout: "60s"
tools:
  - name: "manual"
    description: "manual tool"
    command: "echo"
    args: ["hi"]
    timeout: "10s"
    input_schema:
      type: object
`
	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("write: %v", err)
	}
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if len(cfg.Tools) != 1 || cfg.Tools[0].Name != "manual" {
		t.Fatalf("expected 1 manual tool, got %v", cfg.Tools)
	}
}

func TestLoadConfig_EmptyToolsDirWithDiscoveryManifest(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config_test_*.yaml")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	configContent := `
server:
  host: "0.0.0.0"
  port: 8080
  name: "test"
execution:
  default_timeout: "60s"
  tools_discovery: "manifest"
  tools_dir: ""
tools: []
`
	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("write: %v", err)
	}
	tmpFile.Close()

	_, err = Load(tmpFile.Name())
	if err == nil {
		t.Fatal("expected error for empty tools_dir with manifest discovery, got nil")
	}
}

func TestLoadConfig_DiscoveryManifest(t *testing.T) {
	tmpDir := t.TempDir()
	d1 := createTempToolDir(t, tmpDir, "discovered1", basicManifest("discovered1", "main.py"))
	writeDummyScript(t, d1, "main.py")

	tmpFile, err := os.CreateTemp("", "config_test_*.yaml")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	configContent := `
server:
  host: "0.0.0.0"
  port: 8080
  name: "test"
execution:
  default_timeout: "60s"
  tools_discovery: "manifest"
  tools_dir: "` + tmpDir + `"
tools:
  - name: "manual"
    description: "manual tool"
    command: "echo"
    args: ["hi"]
    timeout: "10s"
    input_schema:
      type: object
`
	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("write: %v", err)
	}
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if len(cfg.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(cfg.Tools))
	}
	names := map[string]bool{}
	for _, tl := range cfg.Tools {
		names[tl.Name] = true
	}
	if !names["manual"] || !names["discovered1"] {
		t.Errorf("expected manual and discovered1, got %v", names)
	}
	// default timeout applied
	if cfg.Execution.DefaultTimeout != 60*time.Second {
		t.Errorf("expected default timeout 60s, got %v", cfg.Execution.DefaultTimeout)
	}
}

func TestDiscoverToolsFromDirectory_DefaultTimeoutApplied(t *testing.T) {
	tmpDir := t.TempDir()
	// manifest without explicit timeout
	m := map[string]interface{}{
		"name":        "notime",
		"description": "no timeout",
		"command":     "python3",
		"args":        []string{"main.py"},
		"input_schema": map[string]interface{}{
			"type": "object",
		},
	}
	b, _ := yaml.Marshal(m)
	dir := createTempToolDir(t, tmpDir, "notime", b)
	writeDummyScript(t, dir, "main.py")

	tools, err := DiscoverToolsFromDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	// Discovery itself does NOT fill default timeout; Load does after calling DiscoverToolsFromDirectory.
	// However, if the manifest omitted timeout, it remains 0 here.
	if tools[0].Timeout != 0 {
		t.Errorf("expected zero timeout before Load, got %v", tools[0].Timeout)
	}
}

func TestDiscoverToolsFromDirectory_DirDoesNotExist(t *testing.T) {
	_, err := DiscoverToolsFromDirectory("/tmp/this_dir_should_not_exist_9999")
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}
