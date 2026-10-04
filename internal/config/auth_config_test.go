package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The auth keyring is declared in YAML but the secrets themselves live in the
// environment, one variable per entry.
func TestLoad_AuthKeyringSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
server:
  host: "0.0.0.0"
  port: 8080
  name: "test-server"

execution:
  default_timeout: "60s"
  working_dir: "/tmp"

auth:
  keys:
    - user_id: amphora
      key_env: MCP_AUTH_KEY_AMPHORA
    - user_id: legacy-upload
      key_env: MCP_UPLOAD_API_KEY

tools: []
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(cfg.Auth.Keys) != 2 {
		t.Fatalf("expected 2 auth keys, got %d", len(cfg.Auth.Keys))
	}
	if cfg.Auth.Keys[0].UserID != "amphora" || cfg.Auth.Keys[0].KeyEnv != "MCP_AUTH_KEY_AMPHORA" {
		t.Errorf("unexpected first entry: %+v", cfg.Auth.Keys[0])
	}
	if cfg.Auth.Keys[1].UserID != "legacy-upload" || cfg.Auth.Keys[1].KeyEnv != "MCP_UPLOAD_API_KEY" {
		t.Errorf("unexpected second entry: %+v", cfg.Auth.Keys[1])
	}
}

// A config without an auth section stays loadable; the switch that decides
// whether the keyring is mandatory is MCP_AUTH_MODE, not the YAML.
func TestLoad_WithoutAuthSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
server:
  host: "0.0.0.0"
  port: 8080
  name: "test-server"

execution:
  default_timeout: "60s"
  working_dir: "/tmp"

tools: []
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(cfg.Auth.Keys) != 0 {
		t.Errorf("expected no auth keys, got %d", len(cfg.Auth.Keys))
	}
}
