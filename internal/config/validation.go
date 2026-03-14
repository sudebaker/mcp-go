package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
)

var (
	ErrInvalidPort         = errors.New("invalid port number (must be 1-65535)")
	ErrInvalidBaseURL      = errors.New("invalid or empty base URL")
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
	if cfg.Server.BaseURL == "" {
		return ErrInvalidBaseURL
	}
	if _, err := url.ParseRequestURI(cfg.Server.BaseURL); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidBaseURL, err)
	}
	if cfg.Execution.DefaultTimeout <= 0 {
		return fmt.Errorf("%w: %v", ErrInvalidTimeout, cfg.Execution.DefaultTimeout)
	}
	if info, err := os.Stat(cfg.Execution.WorkingDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrWorkingDirNotExists, cfg.Execution.WorkingDir)
		}
		return fmt.Errorf("cannot access working directory %s: %w", cfg.Execution.WorkingDir, err)
	} else if !info.IsDir() {
		return fmt.Errorf("working directory path is not a directory: %s", cfg.Execution.WorkingDir)
	}

	if err := checkDirectoryWritable(cfg.Execution.WorkingDir); err != nil {
		return fmt.Errorf("working directory not writable: %w", err)
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

func checkDirectoryWritable(dir string) error {
	testFile := dir + "/.write_test"
	file, err := os.Create(testFile)
	if err != nil {
		return err
	}
	file.Close()
	return os.Remove(testFile)
}
