// Package config provides toolkit-based tool filtering.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ToolkitConfig defines a named subset of tools.
type ToolkitConfig struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tools       []string `yaml:"tools"`
}

// loadToolkit reads a toolkit YAML from the configs/toolkits directory.
func loadToolkit(name string) (*ToolkitConfig, error) {
	if name == "" {
		return nil, nil
	}

	// Validate name to prevent path traversal.
	if strings.Contains(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		return nil, fmt.Errorf("invalid toolkit name: %q", name)
	}

	path := filepath.Join("configs", "toolkits", name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read toolkit %q: %w", path, err)
	}

	var tk ToolkitConfig
	if err := yaml.Unmarshal(data, &tk); err != nil {
		return nil, fmt.Errorf("parse toolkit %q: %w", path, err)
	}

	if tk.Name == "" {
		return nil, fmt.Errorf("toolkit %q missing required field: name", path)
	}

	return &tk, nil
}

// filterToolsByToolkit keeps only tools whose name appears in the toolkit.
// If the toolkit is nil or has an empty tools list, returns the original slice unchanged.
func filterToolsByToolkit(tools []ToolConfig, tk *ToolkitConfig) []ToolConfig {
	if tk == nil || len(tk.Tools) == 0 {
		return tools
	}

	allowed := make(map[string]bool, len(tk.Tools))
	for _, name := range tk.Tools {
		allowed[name] = true
	}

	result := make([]ToolConfig, 0, len(tools))
	for _, t := range tools {
		if allowed[t.Name] {
			result = append(result, t)
		}
	}

	return result
}
