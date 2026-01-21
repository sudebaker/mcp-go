package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

var (
	ErrInvalidPort         = errors.New("invalid port number (must be 1-65535)")
	ErrInvalidToolName     = errors.New("tool name cannot be empty")
	ErrInvalidToolCommand  = errors.New("tool command cannot be empty")
	ErrInvalidTimeout      = errors.New("timeout must be positive")
	ErrWorkingDirNotExists = errors.New("working directory does not exist")
)

func Validate(cfg *Config) error {
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("%w: %d", ErrInvalidPort, cfg.Server.Port)
	}
	if cfg.Server.Name == "" {
		return errors.New("server name cannot be empty")
	}
	if cfg.Execution.DefaultTimeout <= 0 {
		return fmt.Errorf("%w: %v", ErrInvalidTimeout, cfg.Execution.DefaultTimeout)
	}
	if _, err := os.Stat(cfg.Execution.WorkingDir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrWorkingDirNotExists, cfg.Execution.WorkingDir)
	}
	for i := range cfg.Tools {
		if err := ValidateToolConfig(&cfg.Tools[i]); err != nil {
			return fmt.Errorf("tool #%d (%s): %w", i, cfg.Tools[i].Name, err)
		}
	}
	return nil
}

func ValidateToolConfig(tool *ToolConfig) error {
	if tool.Name == "" {
		return ErrInvalidToolName
	}
	if tool.Command == "" {
		return ErrInvalidToolCommand
	}
	if tool.Timeout < 0 {
		return ErrInvalidTimeout
	}
	if _, err := exec.LookPath(tool.Command); err != nil {
		return fmt.Errorf("command not found: %s", tool.Command)
	}
	return nil
}
