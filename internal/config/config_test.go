package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	configContent := `
server:
  host: "0.0.0.0"
  port: 8080
  name: "test-server"

execution:
  default_timeout: "60s"
  working_dir: "/tmp"
  environment:
    TEST_VAR: "test_value"

tools:
  - name: "test-tool"
    description: "Test tool"
    command: "echo"
    args: ["test"]
    timeout: "30s"
`
	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Expected host 0.0.0.0, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Expected port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.Name != "test-server" {
		t.Errorf("Expected name test-server, got %s", cfg.Server.Name)
	}
	if cfg.Execution.DefaultTimeout != 60*time.Second {
		t.Errorf("Expected timeout 60s, got %v", cfg.Execution.DefaultTimeout)
	}
	if cfg.Tools[0].Name != "test-tool" {
		t.Errorf("Expected tool name test-tool, got %s", cfg.Tools[0].Name)
	}
}

func TestLoadConfigWithEnvVars(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	os.Setenv("TEST_PORT", "9999")
	defer os.Unsetenv("TEST_PORT")

	configContent := `
server:
  host: "0.0.0.0"
  port: ${TEST_PORT}
  name: "test-server"
execution:
  default_timeout: "30s"
  working_dir: "/tmp"
tools: []
`
	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Server.Port != 9999 {
		t.Errorf("Expected port 9999, got %d", cfg.Server.Port)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	configContent := `
server:
  port: 0
execution:
  working_dir: ""
tools: []
`
	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Expected default host 0.0.0.0, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Expected default port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.Name != "mcp-orchestrator" {
		t.Errorf("Expected default name mcp-orchestrator, got %s", cfg.Server.Name)
	}
	if cfg.Execution.DefaultTimeout != 60*time.Second {
		t.Errorf("Expected default timeout 60s, got %v", cfg.Execution.DefaultTimeout)
	}
	if cfg.Execution.WorkingDir != "/data" {
		t.Errorf("Expected default working dir /data, got %s", cfg.Execution.WorkingDir)
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
}

func TestGetToolByName(t *testing.T) {
	cfg := &Config{
		Tools: []ToolConfig{
			{Name: "tool1"},
			{Name: "tool2"},
			{Name: "tool3"},
		},
	}

	tool := cfg.GetToolByName("tool2")
	if tool == nil {
		t.Fatal("Expected to find tool2")
	}
	if tool.Name != "tool2" {
		t.Errorf("Expected tool name tool2, got %s", tool.Name)
	}

	notFound := cfg.GetToolByName("nonexistent")
	if notFound != nil {
		t.Error("Expected nil for nonexistent tool")
	}
}
