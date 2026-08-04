// Package config provides configuration management for the MCP orchestrator server.
//
// This package handles loading, parsing, and validating YAML configuration files
// with support for environment variable expansion. It provides the foundational
// configuration types used throughout the server.
//
// # Configuration File Format
//
// The configuration file is written in YAML and supports environment variable
// expansion using ${VAR} or ${VAR:-default} syntax:
//
//	configuration:
//	  server:
//	    host: "0.0.0.0"
//	    port: 8080
//	    base_url: "${BASE_URL:-http://localhost:8080}"
//
// # Security
//
// The package uses yaml.v3 with type-safe unmarshaling to prevent arbitrary
// Go object deserialization (YAML deserialization attacks). All configuration
// values are validated at load time.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gopkg.in/yaml.v3"
)

// Config represents the root configuration structure for the MCP orchestrator.
// It contains server settings, execution parameters, and tool definitions.
type Config struct {
	// Server contains HTTP server configuration
	Server ServerConfig `yaml:"server"`
	// Execution contains tool execution settings
	Execution ExecutionConfig `yaml:"execution"`
	// Upload contains file upload endpoint configuration
	Upload UploadConfig `yaml:"upload,omitempty"`
	// Tools is the list of available tools and their configurations
	Tools []ToolConfig `yaml:"tools"`
	// Prompts is the list of available prompts
	Prompts []PromptConfig `yaml:"prompts,omitempty"`
}

// ServerConfig holds HTTP server-specific settings including network binding,
// rate limiting, and CORS configuration.
type ServerConfig struct {
	// Host is the network address to bind to (default: "0.0.0.0")
	Host string `yaml:"host"`
	// Port is the TCP port to listen on (default: 8080)
	Port int `yaml:"port"`
	// Name is the human-readable service name for logging and health checks
	Name string `yaml:"name"`
	// BaseURL is the public-facing URL for SSE clients (e.g., "https://mcp.example.com")
	BaseURL string `yaml:"base_url"`
	// RateLimitRPS defines requests per second limit (0 = disabled)
	RateLimitRPS float64 `yaml:"rate_limit_rps"`
	// RateLimitBurst is the maximum burst size for rate limiting
	RateLimitBurst int `yaml:"rate_limit_burst"`
	// ShutdownTimeout defines how long to wait for graceful shutdown
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	// AllowedOrigins is the list of permitted CORS origins (empty = all)
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// ExecutionConfig contains settings that control how tools are executed,
// including timeout values, working directory, and environment variables
// passed to tool subprocesses.
type ExecutionConfig struct {
	// DefaultTimeout is the fallback timeout for tools without explicit timeout
	DefaultTimeout time.Duration `yaml:"default_timeout"`
	// WorkingDir is the root directory for tool execution (default: "/data")
	WorkingDir string `yaml:"working_dir"`
	// ReportsDir is the subdirectory for generated reports under WorkingDir (default: "reports")
	ReportsDir string `yaml:"reports_dir"`
	// Environment is a map of environment variables passed to tool processes
	Environment map[string]string `yaml:"environment"`
	// MaxConcurrency limits simultaneous subprocess executions (default: 5)
	// Prevents fork-bomb under high load. Set to 0 for unlimited (not recommended).
	MaxConcurrency int `yaml:"max_concurrency"`
	// ToolsDir is the directory to scan for tool manifests when discovery is enabled
	ToolsDir string `yaml:"tools_dir"`
	// ToolsDiscovery controls automatic tool discovery: "none" (default) or "manifest"
	ToolsDiscovery string `yaml:"tools_discovery"`
	// ToolsAppend determines merge behavior when discovery finds tools with the same name as config tools.
	// "false" (default): discovered tools override config tools.
	// "true": config tools take precedence; discovered tools are appended only if not present.
	ToolsAppend string `yaml:"tools_append"`
}

// ToolConfig defines a single tool's execution parameters, input schema,
// and metadata. Each tool is executed as a subprocess with JSON stdin/stdout.
type ToolConfig struct {
	// Name is the unique identifier for this tool (used in tool listings)
	Name string `yaml:"name"`
	// Description explains the tool's purpose for LLM consumption (supports i18n)
	Description string `yaml:"description"`
	// Command is the executable path (e.g., "python3", "/usr/bin/node")
	Command string `yaml:"command"`
	// Args contains the command-line arguments (supports tool entrypoint)
	Args []string `yaml:"args"`
	// Timeout is the maximum execution time (uses DefaultTimeout if 0)
	Timeout time.Duration `yaml:"timeout"`
	// InputSchema defines the expected JSON schema for tool arguments
	InputSchema map[string]interface{} `yaml:"input_schema"`
	// ReadOnlyHint indicates the tool does not modify external state
	ReadOnlyHint *bool `yaml:"read_only_hint,omitempty"`
	// DestructiveHint indicates the tool may delete or irreversibly modify data
	DestructiveHint *bool `yaml:"destructive_hint,omitempty"`
	// IdempotentHint indicates the tool produces the same result on repeated calls
	IdempotentHint *bool `yaml:"idempotent_hint,omitempty"`
	// OpenWorldHint indicates results depend on external world state
	OpenWorldHint *bool `yaml:"open_world_hint,omitempty"`
}

// PromptArgumentConfig defines an argument for a prompt.
type PromptArgumentConfig struct {
	// Name is the unique identifier for this argument
	Name string `yaml:"name"`
	// Description explains the argument for LLM consumption
	Description string `yaml:"description"`
	// Required indicates if this argument must be provided
	Required bool `yaml:"required"`
}

// PromptMessageConfig defines a message within a prompt template.
type PromptMessageConfig struct {
	// Role is the message speaker: "user" or "assistant"
	Role string `yaml:"role"`
	// Content is the message text (supports {{argument}} placeholders)
	Content string `yaml:"content"`
}

// PromptConfig defines a prompt template with configurable arguments.
type PromptConfig struct {
	// Name is the unique identifier for this prompt
	Name string `yaml:"name"`
	// Description explains the prompt's purpose for LLM consumption
	Description string `yaml:"description"`
	// Arguments are the configurable parameters for this prompt
	Arguments []PromptArgumentConfig `yaml:"arguments,omitempty"`
	// Messages are the template messages in the prompt
	Messages []PromptMessageConfig `yaml:"messages"`
}

// UploadConfig holds configuration for the file upload endpoint.
type UploadConfig struct {
	// Enabled indicates if upload endpoint is active (default: true)
	Enabled bool `yaml:"enabled"`
	// MaxSizeMB is the maximum file size in megabytes (default: 50)
	MaxSizeMB int64 `yaml:"max_size_mb"`
	// AllowedTypes is the whitelist of MIME types
	AllowedTypes []string `yaml:"allowed_types"`
	// DefaultTTLSeconds is the default time-to-live for uploaded files (default: 3600)
	DefaultTTLSeconds int `yaml:"default_ttl_seconds"`
	// MaxTTLSeconds is the maximum TTL a client can request (default: 86400)
	MaxTTLSeconds int `yaml:"max_ttl_seconds"`
	// UploadDir is the base directory for storing uploads (default: /data/uploads)
	UploadDir string `yaml:"upload_dir"`
}

// envVarRegex matches ${VAR_NAME} or ${VAR_NAME:-default} patterns for expansion
var envVarRegex = regexp.MustCompile(`\$\{([^}:]+)(?::-([^}]*))?\}`)

// expandEnvVars replaces ${VAR} and ${VAR:-default} patterns with environment values.
//
// The function supports two syntaxes:
//   - ${VAR_NAME} - substitutes with the environment variable value
//   - ${VAR_NAME:-default} - substitutes with default if VAR is unset or empty
//
// Parameters:
//   - input: the string containing environment variable patterns
//
// Returns:
//
//	the input string with all patterns replaced by their values
func expandEnvVars(input string) string {
	return envVarRegex.ReplaceAllStringFunc(input, func(match string) string {
		parts := envVarRegex.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		varName := parts[1]
		defaultVal := ""
		if len(parts) > 2 {
			defaultVal = parts[2]
		}

		if val, exists := os.LookupEnv(varName); exists {
			return val
		}
		return defaultVal
	})
}

// expandEnvVarsInMap recursively expands environment variables in all map values.
//
// This is used to expand variables in the execution environment map before
// passing to tool subprocesses.
//
// Parameters:
//   - m: a map of string keys to string values
//
// Returns:
//
//	a new map with all values expanded
func expandEnvVarsInMap(m map[string]string) map[string]string {
	result := make(map[string]string, len(m))
	for k, v := range m {
		result[k] = expandEnvVars(v)
	}
	return result
}

// readConfigFile reads the YAML configuration file from the given path.
// Returns raw bytes for downstream processing.
func readConfigFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	return data, nil
}

// expandAndUnmarshal expands env vars in raw YAML and unmarshals into Config.
// SECURITY: yaml.Unmarshal uses SafeDecoder by default in gopkg.in/yaml.v3
// which prevents deserialization of arbitrary Go objects.
func expandAndUnmarshal(raw []byte) (*Config, error) {
	expandedData := expandEnvVars(string(raw))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expandedData), &cfg); err != nil {
		return nil, fmt.Errorf("parse config YAML: %w", err)
	}
	return &cfg, nil
}

// applyDefaults fills zero-value fields with sensible defaults.
// POST: cfg.Server.Port != 0, cfg.Server.Name != "", etc.
func applyDefaults(cfg *Config) {
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Name == "" {
		cfg.Server.Name = "mcp-orchestrator"
	}
	if cfg.Server.BaseURL == "" {
		cfg.Server.BaseURL = fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Server.Port)
	}
	if cfg.Server.ShutdownTimeout == 0 {
		cfg.Server.ShutdownTimeout = 10 * time.Second
	}
	if cfg.Execution.DefaultTimeout == 0 {
		cfg.Execution.DefaultTimeout = 60 * time.Second
	}
	if cfg.Execution.WorkingDir == "" {
		cfg.Execution.WorkingDir = "/data"
	}
	if cfg.Execution.ReportsDir == "" {
		cfg.Execution.ReportsDir = "reports"
	}
	if cfg.Execution.MaxConcurrency <= 0 {
		cfg.Execution.MaxConcurrency = 5
	}
}

// expandEnvMap expands ${VAR} patterns in the execution environment map.
// PRE: applyDefaults has run (defaults ensure consistency).
// POST: no value in Environment contains unresolved ${VAR} patterns.
func expandEnvMap(cfg *Config) {
	if cfg.Execution.Environment != nil {
		cfg.Execution.Environment = expandEnvVarsInMap(cfg.Execution.Environment)
	}
}

// setToolDefaults applies the default timeout to tools that don't specify one.
func setToolDefaults(cfg *Config) {
	for i := range cfg.Tools {
		if cfg.Tools[i].Timeout == 0 {
			cfg.Tools[i].Timeout = cfg.Execution.DefaultTimeout
		}
	}
}

// applyDiscovery scans the tools directory for manifest-based tools and merges
// them into cfg.Tools. Only runs when ToolsDiscovery == "manifest".
// PRE: cfg.Tools has tools declared in YAML (if any).
// POST: cfg.Tools includes discovered tools merged per ToolsAppend policy.
func applyDiscovery(cfg *Config, configDir string) error {
	if cfg.Execution.ToolsDiscovery != "manifest" {
		return nil
	}
	if cfg.Execution.ToolsDir == "" {
		return fmt.Errorf("tools_discovery is 'manifest' but tools_dir is empty")
	}
	toolsDir := cfg.Execution.ToolsDir
	if !filepath.IsAbs(toolsDir) {
		toolsDir = filepath.Join(configDir, toolsDir)
	}
	discovered, err := DiscoverToolsFromDirectory(toolsDir)
	if err != nil {
		return fmt.Errorf("tool discovery failed for %s: %w", toolsDir, err)
	}
	for i := range discovered {
		if discovered[i].Timeout == 0 {
			discovered[i].Timeout = cfg.Execution.DefaultTimeout
		}
	}
	cfg.Tools = mergeTools(cfg.Tools, discovered, cfg.Execution.ToolsAppend)
	return nil
}

// applyToolsets filters the tool list based on the active toolset from the
// MCP_TOOLSET env var (default: "default"). Reads toolsets.yaml from the
// config directory. The file is optional — if it doesn't exist, all tools pass.
// PRE: cfg.Tools has the full tool list (applyDiscovery has run).
// POST: cfg.Tools is filtered to only the active toolset.
func applyToolsets(cfg *Config, configDir string) error {
	toolsetEnv := os.Getenv("MCP_TOOLSET")
	if toolsetEnv == "" {
		toolsetEnv = "default"
	}

	toolsetsPath := filepath.Join(configDir, "toolsets.yaml")
	if _, err := os.Stat(toolsetsPath); err == nil {
		tc, err := loadToolsets(toolsetsPath)
		if err != nil {
			return fmt.Errorf("loading toolsets config: %w", err)
		}
		cfg.Tools = filterToolsByToolset(cfg.Tools, toolsetEnv, tc.Toolsets)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat toolsets file: %w", err)
	}
	return nil
}

// Load reads and parses the configuration file from the specified path.
//
// Pipeline (stages run in order):
//  1. readConfigFile — read raw bytes from disk
//  2. expandAndUnmarshal — expand ${VAR} in YAML + unmarshal into Config
//  3. applyDefaults — fill zero-value fields with defaults
//  4. expandEnvMap — expand ${VAR} in execution environment map
//  5. setToolDefaults — apply default timeout to tools
//  6. applyDiscovery — scan tools directory for manifest tools
//  7. applyToolsets — filter tools by active toolset
//  8. Validate — check for conflicting configuration
//
// Parameters:
//   - path: absolute or relative path to the YAML configuration file
//
// Returns:
//   - *Config: the parsed and validated configuration
//   - error: any failure during reading, parsing, or validation
func Load(path string) (*Config, error) {
	raw, err := readConfigFile(path)
	if err != nil {
		return nil, err
	}

	cfg, err := expandAndUnmarshal(raw)
	if err != nil {
		return nil, err
	}

	applyDefaults(cfg)
	expandEnvMap(cfg)
	setToolDefaults(cfg)

	if err := applyDiscovery(cfg, filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := applyToolsets(cfg, filepath.Dir(path)); err != nil {
		return nil, err
	}

	return cfg, nil
}

// mergeTools merges discovered tools with configured tools.
// resolvedAppend is the string value from config; it is considered true only if it equals "true".
// If false, discovered tools override config tools (discovered first, then remaining config).
// If true, config tools take precedence and discovered tools are added only for new names.
func mergeTools(configured, discovered []ToolConfig, appendRaw string) []ToolConfig {
	appendMode := appendRaw == "true"
	nameSet := make(map[string]bool, len(configured)+len(discovered))
	for _, t := range configured {
		nameSet[t.Name] = true
	}
	for _, t := range discovered {
		nameSet[t.Name] = true
	}

	// Pre-allocate ordered result slice preserving deterministic order.
	result := make([]ToolConfig, 0, len(nameSet))

	if appendMode {
		// Config tools first (they win), then discovered tools that are new.
		for _, t := range configured {
			result = append(result, t)
		}
		for _, t := range discovered {
			found := false
			for _, existing := range configured {
				if existing.Name == t.Name {
					found = true
					break
				}
			}
			if !found {
				result = append(result, t)
			}
		}
	} else {
		// Discovered tools override config tools: discovered first, then config tools not overridden.
		overridden := make(map[string]bool, len(discovered))
		for _, t := range discovered {
			result = append(result, t)
			overridden[t.Name] = true
		}
		for _, t := range configured {
			if !overridden[t.Name] {
				result = append(result, t)
			}
		}
	}
	return result
}

// GetToolByName finds a tool configuration by its unique name.
//
// This is the primary method for tool lookup during request handling.
// The search is case-sensitive and linear.
//
// Parameters:
//   - name: the unique tool identifier to find
//
// Returns:
//   - *ToolConfig: pointer to the found configuration, or nil if not found
func (c *Config) GetToolByName(name string) *ToolConfig {
	for i := range c.Tools {
		if c.Tools[i].Name == name {
			return &c.Tools[i]
		}
	}
	return nil
}

// ToolsetsConfig defines the top-level structure for configs/toolsets.yaml.
type ToolsetsConfig struct {
	Toolsets map[string]ToolsetDefinition `yaml:"toolsets"`
}

// ToolsetDefinition defines a named toolset with its tool list.
type ToolsetDefinition struct {
	Description string   `yaml:"description"`
	Tools       []string `yaml:"tools"`
}

// loadToolsets reads the toolsets definition YAML file.
func loadToolsets(path string) (*ToolsetsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read toolsets file: %w", err)
	}
	var tc ToolsetsConfig
	if err := yaml.Unmarshal(data, &tc); err != nil {
		return nil, fmt.Errorf("parse toolsets YAML: %w", err)
	}
	return &tc, nil
}

// filterToolsByToolset keeps only tools whose names appear in the active toolset(s).
// The toolsetEnv can be a single name or a comma-separated list (union).
// Tools not found among the discovered tools are logged as warnings and skipped.
func filterToolsByToolset(tools []ToolConfig, toolsetEnv string, toolsets map[string]ToolsetDefinition) []ToolConfig {
	if toolsetEnv == "" {
		return tools
	}

	names := strings.Split(toolsetEnv, ",")
	allowed := make(map[string]bool)

	for _, name := range names {
		name = strings.TrimSpace(name)
		ts, ok := toolsets[name]
		if !ok {
			log.Warn().Str("toolset", name).Msg("Toolset not found in configs/toolsets.yaml")
			continue
		}
		for _, t := range ts.Tools {
			allowed[t] = true
		}
	}

	discoveredNames := make(map[string]bool, len(tools))
	for _, t := range tools {
		discoveredNames[t.Name] = true
	}
	for toolName := range allowed {
		if !discoveredNames[toolName] {
			log.Warn().Str("tool", toolName).Msg("Tool in active toolset not found; skipping")
		}
	}

	result := make([]ToolConfig, 0, len(tools))
	for _, t := range tools {
		if allowed[t.Name] {
			result = append(result, t)
		}
	}

	return result
}
