package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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

	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("Expected default host 127.0.0.1, got %s", cfg.Server.Host)
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

// unsetEnv removes key for the duration of the test, restoring the previous
// value (or absence) on cleanup. Needed because t.Setenv can only set a value.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Failed to unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
			return
		}
		_ = os.Unsetenv(key)
	})
}

// writeBindHostConfig writes a config using the exact server.host line shipped
// in configs/config.yaml, so the test covers the real template semantics.
func writeBindHostConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server:
  host: "${MCP_BIND_HOST:-127.0.0.1}"
  port: 8080
execution:
  working_dir: "/tmp"
tools: []
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	return path
}

// TestLoadConfigBindHost verifies the MCP_BIND_HOST contract: loopback unless
// the operator explicitly asks for a wider bind address.
func TestLoadConfigBindHost(t *testing.T) {
	path := writeBindHostConfig(t)

	t.Run("unset falls back to loopback", func(t *testing.T) {
		unsetEnv(t, "MCP_BIND_HOST")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Failed to load config: %v", err)
		}
		if cfg.Server.Host != "127.0.0.1" {
			t.Errorf("Expected host 127.0.0.1, got %s", cfg.Server.Host)
		}
	})

	t.Run("empty falls back to loopback", func(t *testing.T) {
		t.Setenv("MCP_BIND_HOST", "")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Failed to load config: %v", err)
		}
		if cfg.Server.Host != "127.0.0.1" {
			t.Errorf("Expected host 127.0.0.1, got %s", cfg.Server.Host)
		}
	})

	t.Run("env override wins", func(t *testing.T) {
		t.Setenv("MCP_BIND_HOST", "0.0.0.0")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Failed to load config: %v", err)
		}
		if cfg.Server.Host != "0.0.0.0" {
			t.Errorf("Expected host 0.0.0.0, got %s", cfg.Server.Host)
		}
	})
}

// TestRepositoryConfigBindsLoopback parses the shipped configs/config.yaml so a
// regression that hardcodes a wildcard bind in the template fails here. Only the
// server section is decoded: the full file points at container-only paths
// (tools_dir: /app/tools) that do not exist on a developer host.
func TestRepositoryConfigBindsLoopback(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "config.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("Repository config not available: %v", err)
	}

	hostFor := func(content string) string {
		t.Helper()
		var serverOnly struct {
			Server struct {
				Host string `yaml:"host"`
			} `yaml:"server"`
		}
		if err := yaml.Unmarshal([]byte(expandEnvVars(content)), &serverOnly); err != nil {
			t.Fatalf("Failed to parse repository config: %v", err)
		}
		return serverOnly.Server.Host
	}

	unsetEnv(t, "MCP_BIND_HOST")
	if got := hostFor(string(raw)); got != "127.0.0.1" {
		t.Errorf("configs/config.yaml must bind loopback by default, got %q", got)
	}

	t.Setenv("MCP_BIND_HOST", "0.0.0.0")
	if got := hostFor(string(raw)); got != "0.0.0.0" {
		t.Errorf("Expected MCP_BIND_HOST override to be honoured, got %q", got)
	}
}
