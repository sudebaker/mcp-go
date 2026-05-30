// Package config provides automatic tool discovery via manifest scanning.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

// manifestFileName is the expected manifest file inside each tool subdirectory.
const manifestFileName = "tool.yaml"

// maxDiscoveredTools limits the number of tools loaded from a single directory
// to prevent startup denial of service from a directory with too many entries.
const maxDiscoveredTools = 500

// DiscoverToolsFromDirectory scans a single directory level for tool subdirectories
// containing a tool.yaml manifest. It returns a slice of ToolConfig values with
// absolute args paths resolved and validated.
func DiscoverToolsFromDirectory(toolsDir string) ([]ToolConfig, error) {
	absDir, err := filepath.Abs(toolsDir)
	if err != nil {
		return nil, fmt.Errorf("resolve tools_dir: %w", err)
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("tools_dir does not exist: %s", absDir)
		}
		return nil, fmt.Errorf("read tools_dir: %w", err)
	}

	var result []ToolConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// DoS protection: hard cap on discovered tools
		if len(result) >= maxDiscoveredTools {
			log.Warn().
				Int("limit", maxDiscoveredTools).
				Str("toolsDir", absDir).
				Msg("Too many tool directories; truncating discovery")
			break
		}

		manifestPath := filepath.Join(absDir, entry.Name(), manifestFileName)
		info, err := os.Stat(manifestPath)
		if err != nil || info.IsDir() {
			// No manifest in this subdirectory; skip silently.
			continue
		}

		tool, err := loadManifest(manifestPath)
		if err != nil {
			log.Warn().
				Str("manifest", manifestPath).
				Err(err).
				Msg("Skipping invalid tool manifest")
			continue
		}

		toolDir := filepath.Dir(manifestPath)
		if err := resolveAndValidateArgs(toolDir, tool); err != nil {
			log.Warn().
				Str("tool", tool.Name).
				Err(err).
				Msg("Skipping tool with unsafe args")
			continue
		}

		// If timeout is zero, leave it for the caller (Load) to fill with default.
		// If non-zero but negative, reject.
		if tool.Timeout < 0 {
			log.Warn().
				Str("tool", tool.Name).
				Dur("timeout", tool.Timeout).
				Msg("Skipping tool with negative timeout")
			continue
		}

		result = append(result, *tool)
	}

	return result, nil
}

// loadManifest reads and parses a single tool.yaml manifest into ToolConfig.
func loadManifest(path string) (*ToolConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var tool ToolConfig
	if err := yaml.Unmarshal(data, &tool); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	if err := validateManifestFields(&tool); err != nil {
		return nil, err
	}

	return &tool, nil
}

// validateManifestFields performs minimal schema validation.
func validateManifestFields(tool *ToolConfig) error {
	if strings.TrimSpace(tool.Name) == "" {
		return fmt.Errorf("manifest missing required field: name")
	}
	if strings.TrimSpace(tool.Description) == "" {
		return fmt.Errorf("manifest missing required field: description")
	}
	if strings.TrimSpace(tool.Command) == "" {
		return fmt.Errorf("manifest missing required field: command")
	}
	if len(tool.Args) == 0 {
		return fmt.Errorf("manifest missing required field: args")
	}
	if tool.InputSchema == nil {
		return fmt.Errorf("manifest missing required field: input_schema")
	}
	return nil
}

// resolveAndValidateArgs converts relative arguments to absolute paths and blocks
// any path that escapes the tool directory (path traversal).
func resolveAndValidateArgs(toolDir string, tool *ToolConfig) error {
	for i, arg := range tool.Args {
		// Only resolve arguments that look like relative paths.
		// Skip flags, URLs, and absolute paths.
		if arg == "" || strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
			continue
		}

		// Resolve relative path against tool directory.
		resolved := filepath.Join(toolDir, arg)
		cleanResolved := filepath.Clean(resolved)
		cleanToolDir := filepath.Clean(toolDir)

		// Ensure the resolved path is inside the tool directory.
		if !strings.HasPrefix(cleanResolved, cleanToolDir+string(filepath.Separator)) && cleanResolved != cleanToolDir {
			return fmt.Errorf("path traversal blocked for arg %q: resolves to %s", arg, cleanResolved)
		}

		// Ensure the resolved path exists (optional: we can keep it relaxed for nonexistent files,
		// but for scripts that must exist we validate).
		if _, err := os.Stat(cleanResolved); err != nil {
			return fmt.Errorf("arg %q resolved path does not exist: %s", arg, cleanResolved)
		}

		tool.Args[i] = cleanResolved
	}
	return nil
}

// discoveredToolWrapper is used to unmarshal the manifest while handling the
// timeout field flexibly (string or zero value). yaml.v3 already decodes
// string durations into time.Duration when they match time.ParseDuration.
// This file relies on that built-in behaviour.
