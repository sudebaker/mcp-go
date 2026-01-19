package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	mcptypes "github.com/amphora/mcp-go/internal/mcp"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Executor handles subprocess execution for tool calls
type Executor struct {
	config *config.Config
}

// New creates a new Executor with the given configuration
func New(cfg *config.Config) *Executor {
	return &Executor{config: cfg}
}

// UpdateConfig updates the executor's configuration (for hot-reload)
func (e *Executor) UpdateConfig(cfg *config.Config) {
	e.config = cfg
}

// ExecuteResult contains the result of a tool execution
type ExecuteResult struct {
	Success           bool
	Content           []mcptypes.ContentItem
	StructuredContent map[string]interface{}
	Error             *mcptypes.SubprocessError
	Stderr            string
}

// Execute runs a tool by spawning a subprocess and communicating via STDIN/STDOUT
func (e *Executor) Execute(ctx context.Context, toolName string, arguments map[string]interface{}) (*ExecuteResult, error) {
	// Find the tool configuration
	toolCfg := e.config.GetToolByName(toolName)
	if toolCfg == nil {
		return nil, fmt.Errorf("tool '%s' not found in configuration", toolName)
	}

	// Create timeout context
	timeout := toolCfg.Timeout
	if timeout == 0 {
		timeout = e.config.Execution.DefaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Prepare the subprocess request
	requestID := uuid.New().String()
	subprocReq := mcptypes.SubprocessRequest{
		RequestID: requestID,
		ToolName:  toolName,
		Arguments: arguments,
		Context: mcptypes.SubprocessContext{
			LLMAPIURL:   e.config.Execution.Environment["LLM_API_URL"],
			LLMModel:    e.config.Execution.Environment["LLM_MODEL"],
			DatabaseURL: e.config.Execution.Environment["DATABASE_URL"],
			WorkingDir:  e.config.Execution.WorkingDir,
		},
	}

	// Marshal request to JSON
	inputJSON, err := json.Marshal(subprocReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create the command
	cmd := exec.CommandContext(execCtx, toolCfg.Command, toolCfg.Args...)
	cmd.Dir = e.config.Execution.WorkingDir

	// Set up environment
	cmd.Env = buildEnvironment(e.config.Execution.Environment)

	// Set up stdin, stdout, stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Log execution start
	log.Debug().
		Str("tool", toolName).
		Str("request_id", requestID).
		Str("command", toolCfg.Command).
		Strs("args", toolCfg.Args).
		Msg("Executing subprocess")

	// Run the command
	startTime := time.Now()
	err = cmd.Run()
	duration := time.Since(startTime)

	// Log execution result
	log.Debug().
		Str("tool", toolName).
		Str("request_id", requestID).
		Dur("duration", duration).
		Int("exit_code", cmd.ProcessState.ExitCode()).
		Msg("Subprocess completed")

	// Log stderr if present
	stderrStr := stderr.String()
	if stderrStr != "" {
		log.Warn().
			Str("tool", toolName).
			Str("request_id", requestID).
			Str("stderr", stderrStr).
			Msg("Subprocess stderr output")
	}

	// Handle context timeout/cancellation
	if execCtx.Err() == context.DeadlineExceeded {
		return &ExecuteResult{
			Success: false,
			Error: &mcptypes.SubprocessError{
				Code:    mcptypes.ErrorCodeTimeout,
				Message: fmt.Sprintf("tool '%s' execution timed out after %v", toolName, timeout),
			},
			Stderr: stderrStr,
		}, nil
	}

	if execCtx.Err() == context.Canceled {
		return &ExecuteResult{
			Success: false,
			Error: &mcptypes.SubprocessError{
				Code:    mcptypes.ErrorCodeExecutionFailed,
				Message: "execution was cancelled",
			},
			Stderr: stderrStr,
		}, nil
	}

	// Handle command execution error
	if err != nil {
		return &ExecuteResult{
			Success: false,
			Error: &mcptypes.SubprocessError{
				Code:    mcptypes.ErrorCodeExecutionFailed,
				Message: fmt.Sprintf("command execution failed: %v", err),
				Details: stderrStr,
			},
			Stderr: stderrStr,
		}, nil
	}

	// Parse the JSON response from stdout
	stdoutStr := strings.TrimSpace(stdout.String())
	if stdoutStr == "" {
		return &ExecuteResult{
			Success: false,
			Error: &mcptypes.SubprocessError{
				Code:    mcptypes.ErrorCodeExecutionFailed,
				Message: "subprocess produced no output",
				Details: stderrStr,
			},
			Stderr: stderrStr,
		}, nil
	}

	var subprocResp mcptypes.SubprocessResponse
	if err := json.Unmarshal([]byte(stdoutStr), &subprocResp); err != nil {
		return &ExecuteResult{
			Success: false,
			Error: &mcptypes.SubprocessError{
				Code:    mcptypes.ErrorCodeExecutionFailed,
				Message: fmt.Sprintf("failed to parse subprocess response: %v", err),
				Details: stdoutStr,
			},
			Stderr: stderrStr,
		}, nil
	}

	// Validate request ID matches
	if subprocResp.RequestID != requestID {
		log.Warn().
			Str("expected", requestID).
			Str("got", subprocResp.RequestID).
			Msg("Request ID mismatch in subprocess response")
	}

	return &ExecuteResult{
		Success:           subprocResp.Success,
		Content:           subprocResp.Content,
		StructuredContent: subprocResp.StructuredContent,
		Error:             subprocResp.Error,
		Stderr:            stderrStr,
	}, nil
}

// buildEnvironment creates the environment variables for the subprocess
func buildEnvironment(envMap map[string]string) []string {
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

// ValidateToolConfig checks if a tool configuration is valid
func ValidateToolConfig(toolCfg *config.ToolConfig) error {
	if toolCfg.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if toolCfg.Command == "" {
		return fmt.Errorf("tool command is required")
	}
	return nil
}
