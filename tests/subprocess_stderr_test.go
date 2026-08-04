package tests

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/executor"
)

// TestStderrLogging tests that stderr output from subprocess tools is properly captured and logged.
// Requires python3 to be available in PATH; skipped otherwise (integration test).
func TestStderrLogging(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found in PATH — skipping subprocess stderr integration test")
	}
	cfg := &config.Config{
		Execution: config.ExecutionConfig{
			DefaultTimeout: 10 * time.Second,
			MaxConcurrency: 5,
			WorkingDir:     ".",
			Environment: map[string]string{
				"LLM_API_URL": "http://localhost:11434",
				"LLM_MODEL":   "test-model",
			},
		},
		Tools: []config.ToolConfig{
			{
				Name:        "test_stderr_logging",
				Description: "Test tool to verify stderr logging behavior",
				Command:     "python3",
				Args:        []string{"../tools/test_stderr_logging/main.py"},
				Timeout:     30 * time.Second,
			},
		},
	}

	exec := executor.New(cfg)
	require.NotNil(t, exec)

	// Execute the test tool
	result, err := exec.Execute(context.Background(), "test_stderr_logging", map[string]interface{}{})
	
	// Should not return an error for execution
	require.NoError(t, err)
	
	// Should be successful (the tool itself succeeds even though it writes to stderr)
	require.True(t, result.Success)
	
	// Should have stderr content
	require.NotEmpty(t, result.Stderr)
	
	// Verify stderr contains our expected content
	require.Contains(t, result.Stderr, "This is a test stderr message")
	require.Contains(t, result.Stderr, "Error: Something went wrong!")
	
	// Log the result for verification
	log.Info().Str("stderr_content", result.Stderr).Msg("Captured stderr content")
}