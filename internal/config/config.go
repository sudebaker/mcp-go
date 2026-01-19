package config

import (
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"
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
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	Name string `yaml:"name"`
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

// Watch monitors the configuration file for changes and reloads it
func Watch(path string, onChange func(*Config)) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					log.Info().Str("file", path).Msg("Config file changed, reloading")
					cfg, err := Load(path)
					if err != nil {
						log.Error().Err(err).Msg("Failed to reload config")
						continue
					}
					onChange(cfg)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Error().Err(err).Msg("Config watcher error")
			}
		}
	}()

	// Watch the directory containing the config file to handle editors that
	// create new files instead of modifying in place
	dir := path
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		dir = path[:idx]
	}

	return watcher.Add(dir)
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
