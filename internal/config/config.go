package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the main configuration structure
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Execution ExecutionConfig `yaml:"execution"`
	Tools     []ToolConfig    `yaml:"tools"`
}

// ServerConfig holds server-specific settings
type ServerConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	Name            string        `yaml:"name"`
	BaseURL         string        `yaml:"base_url"`
	RateLimitRPS    float64       `yaml:"rate_limit_rps"`
	RateLimitBurst  int           `yaml:"rate_limit_burst"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	AllowedOrigins  []string      `yaml:"allowed_origins"`
}

// ExecutionConfig holds execution-related settings
type ExecutionConfig struct {
	DefaultTimeout time.Duration     `yaml:"default_timeout"`
	WorkingDir     string            `yaml:"working_dir"`
	Environment    map[string]string `yaml:"environment"`
}

// ToolConfig defines a tool's configuration
type ToolConfig struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Command     string                 `yaml:"command"`
	Args        []string               `yaml:"args"`
	Timeout     time.Duration          `yaml:"timeout"`
	InputSchema map[string]interface{} `yaml:"input_schema"`
}

// envVarRegex matches ${VAR_NAME} or ${VAR_NAME:-default} patterns
var envVarRegex = regexp.MustCompile(`\$\{([^}:]+)(?::-([^}]*))?\}`)

// expandEnvVars replaces ${VAR} and ${VAR:-default} patterns with environment values
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

// expandEnvVarsInMap recursively expands environment variables in a map
func expandEnvVarsInMap(m map[string]string) map[string]string {
	result := make(map[string]string, len(m))
	for k, v := range m {
		result[k] = expandEnvVars(v)
	}
	return result
}

// Load reads and parses the configuration file
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

// GetToolByName finds a tool configuration by name
func (c *Config) GetToolByName(name string) *ToolConfig {
	for i := range c.Tools {
		if c.Tools[i].Name == name {
			return &c.Tools[i]
		}
	}
	return nil
}
