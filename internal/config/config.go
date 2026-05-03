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
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the root configuration structure for the MCP orchestrator.
// It contains server settings, execution parameters, and tool definitions.
type Config struct {
	// Server contains HTTP server configuration
	Server ServerConfig `yaml:"server"`
	// Execution contains tool execution settings
	Execution ExecutionConfig `yaml:"execution"`
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
	// Environment is a map of environment variables passed to tool processes
	Environment map[string]string `yaml:"environment"`
	// MaxConcurrency limits simultaneous subprocess executions (default: 5)
	// Prevents fork-bomb under high load. Set to 0 for unlimited (not recommended).
	MaxConcurrency int `yaml:"max_concurrency"`
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

// Load reads and parses the configuration file from the specified path.
//
// The function performs the following steps:
//  1. Read the entire file contents
//  2. Expand environment variables in the raw YAML
//  3. Unmarshal YAML into Config struct (type-safe, no arbitrary objects)
//  4. Apply default values for unset fields
//  5. Expand environment variables in the execution environment map
//  6. Set default timeouts for tools that don't specify one
//
// Parameters:
//   - path: absolute or relative path to the YAML configuration file
//
// Returns:
//   - *Config: the parsed and validated configuration
//   - error: any failure during reading, parsing, or validation
//
// Example:
//
//	cfg, err := config.Load("/app/configs/config.yaml")
//	if err != nil {
//	    log.Fatal(err)
//	}
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand environment variables in the raw YAML
	expandedData := expandEnvVars(string(data))

	var cfg Config
	// SECURITY: yaml.Unmarshal uses SafeDecoder by default in gopkg.in/yaml.v3
	// which prevents deserialization of arbitrary Go objects. No unsafe
	// deserialization possible.
	if err := yaml.Unmarshal([]byte(expandedData), &cfg); err != nil {
		return nil, err
	}

	// Apply defaults
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
	if cfg.Execution.MaxConcurrency <= 0 {
		cfg.Execution.MaxConcurrency = 5
	}

	// Expand environment variables in the environment map
	if cfg.Execution.Environment != nil {
		cfg.Execution.Environment = expandEnvVarsInMap(cfg.Execution.Environment)
	}

	// Set default timeouts for tools
	for i := range cfg.Tools {
		if cfg.Tools[i].Timeout == 0 {
			cfg.Tools[i].Timeout = cfg.Execution.DefaultTimeout
		}
	}

	return &cfg, nil
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
