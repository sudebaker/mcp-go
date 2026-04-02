// Package executor provides tool execution functionality for the MCP orchestrator.
//
// This package is responsible for spawning subprocesses to execute Python tools,
// managing their lifecycles, handling timeouts, and parsing their JSON responses.
// It integrates with the tracing package for observability and the config package
// for tool definitions.
//
// # Tool Execution Protocol
//
// Tools communicate via JSON over stdin/stdout:
//
//  1. Server sends SubprocessRequest as JSON on stdin
//  2. Tool processes request and writes SubprocessResponse to stdout
//  3. Optional: Tools can stream chunks prefixed with "__CHUNK__:" or "__RESULT__:"
//  4. Stderr is captured and logged separately
//
// # Timeout Behavior
//
// Each tool has a configurable timeout (default: 60s). If execution exceeds
// the timeout, the subprocess is terminated via context cancellation and an
// error is returned with ErrorCodeTimeout.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	mcptypes "github.com/amphora/mcp-go/internal/mcp"
	"github.com/amphora/mcp-go/internal/tracing"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Prefixes for streaming output parsing
const (
	chunkPrefix  = "__CHUNK__:"  // Prefix for streaming chunks
	resultPrefix = "__RESULT__:" // Prefix for final result
)

// Executor manages the execution of tools as subprocesses.
// It validates inputs, spawns processes, handles timeouts, and parses responses.
type Executor struct {
	config *config.Config  // Server configuration including tool definitions
	tracer *tracing.Tracer // For distributed tracing of tool executions
}

// New creates a new Executor with no-op tracing enabled.
//
// This constructor is suitable for production use where tracing is handled
// at a higher level or not required.
//
// Parameters:
//   - cfg: the parsed configuration containing tool definitions
//
// Returns:
//
//	a new Executor instance with no-op tracer
func New(cfg *config.Config) *Executor {
	return &Executor{
		config: cfg,
		tracer: tracing.NoOpTracer(),
	}
}

// NewWithTracer creates a new Executor with custom tracing.
//
// Use this constructor when you need to trace tool executions as part of
// a distributed tracing system.
//
// Parameters:
//   - cfg: the parsed configuration containing tool definitions
//   - tracer: the tracer instance (if nil, a no-op tracer is used)
//
// Returns:
//
//	a new Executor instance with the provided or no-op tracer
func NewWithTracer(cfg *config.Config, tracer *tracing.Tracer) *Executor {
	if tracer == nil {
		tracer = tracing.NoOpTracer()
	}
	return &Executor{
		config: cfg,
		tracer: tracer,
	}
}

// ExecuteResult contains the outcome of a tool execution.
// It wraps the tool's response with additional metadata.
type ExecuteResult struct {
	// Success indicates whether the tool executed without errors
	Success bool
	// Content is the list of content items returned by the tool
	Content []mcptypes.ContentItem
	// StructuredContent contains parsed tool-specific data
	StructuredContent map[string]interface{}
	// Error contains error information if Success is false
	Error *mcptypes.SubprocessError
	// Stderr captures the tool's standard error output
	Stderr string
	// Chunks contains streaming data if the tool used chunked output
	Chunks []map[string]interface{}
}

// Execute runs a tool by name with the provided arguments.
//
// This is the main entry point for tool execution. It performs the following:
//
//  1. Looks up the tool configuration by name
//  2. Validates arguments against the tool's input schema
//  3. Creates a timeout context based on tool configuration
//  4. Spawns the subprocess with serialized request on stdin
//  5. Captures stdout/stderr
//  6. Parses the JSON response
//  7. Handles streaming output if present
//  8. Records execution metrics and tracing spans
//
// Parameters:
//   - ctx: context for cancellation and tracing
//   - toolName: the unique name of the tool to execute
//   - arguments: the tool-specific arguments (must match input schema)
//
// Returns:
//   - *ExecuteResult: the execution outcome including content or error
//   - error: only if tool lookup or validation fails (not for tool errors)
//
// Possible ExecuteResult.Error codes:
//   - ErrorCodeTimeout: execution exceeded the configured timeout
//   - ErrorCodeExecutionFailed: subprocess error, parsing error, or tool error
func (e *Executor) Execute(ctx context.Context, toolName string, arguments map[string]interface{}) (*ExecuteResult, error) {
	// Start tracing span for tool execution
	span, _ := e.tracer.StartSpan(ctx, fmt.Sprintf("execute_tool:%s", toolName))
	defer span.End()

	toolCfg := e.config.GetToolByName(toolName)
	if toolCfg == nil {
		span.RecordError(fmt.Errorf("tool not found"))
		return nil, fmt.Errorf("tool '%s' not found in configuration", toolName)
	}

	// Validate arguments against the tool's input schema
	if err := validateInputArguments(toolCfg.InputSchema, arguments); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	timeout := toolCfg.Timeout
	if timeout == 0 {
		timeout = e.config.Execution.DefaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	requestID := uuid.New().String()
	span.SetAttribute("request_id", requestID)
	span.SetAttribute("tool_name", toolName)
	span.SetAttribute("timeout_seconds", timeout.Seconds())
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

	inputJSON, err := json.Marshal(subprocReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	cmd := exec.CommandContext(execCtx, toolCfg.Command, toolCfg.Args...)
	cmd.Dir = e.config.Execution.WorkingDir
	cmd.Env = buildEnvironment(e.config.Execution.Environment)

	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Debug().
		Str("tool", toolName).
		Str("request_id", requestID).
		Str("command", toolCfg.Command).
		Strs("args", toolCfg.Args).
		Msg("Executing subprocess")

	startTime := time.Now()
	err = cmd.Run()
	duration := time.Since(startTime)

	span.SetAttribute("duration_ms", duration.Milliseconds())
	span.SetAttribute("exit_code", cmd.ProcessState.ExitCode())

	log.Debug().
		Str("tool", toolName).
		Str("request_id", requestID).
		Dur("duration", duration).
		Int("exit_code", cmd.ProcessState.ExitCode()).
		Msg("Subprocess completed")

	stderrStr := stderr.String()
	if stderrStr != "" {
		log.Warn().
			Str("tool", toolName).
			Str("request_id", requestID).
			Str("stderr", stderrStr).
			Msg("Subprocess stderr output")
	}

	if execCtx.Err() == context.DeadlineExceeded {
		timeoutErr := fmt.Errorf("tool '%s' execution timed out after %v", toolName, timeout)
		span.RecordError(timeoutErr)
		span.SetAttribute("error_code", mcptypes.ErrorCodeTimeout)
		return &ExecuteResult{
			Success: false,
			Error: &mcptypes.SubprocessError{
				Code:    mcptypes.ErrorCodeTimeout,
				Message: timeoutErr.Error(),
			},
			Stderr: stderrStr,
		}, nil
	}

	if execCtx.Err() == context.Canceled {
		cancelErr := fmt.Errorf("execution was cancelled")
		span.RecordError(cancelErr)
		span.SetAttribute("error_code", mcptypes.ErrorCodeExecutionFailed)
		return &ExecuteResult{
			Success: false,
			Error: &mcptypes.SubprocessError{
				Code:    mcptypes.ErrorCodeExecutionFailed,
				Message: cancelErr.Error(),
			},
			Stderr: stderrStr,
		}, nil
	}

	if err != nil {
		span.RecordError(err)
		span.SetAttribute("error_code", mcptypes.ErrorCodeExecutionFailed)
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

	stdoutStr := strings.TrimSpace(stdout.String())
	if stdoutStr == "" {
		noOutputErr := fmt.Errorf("subprocess produced no output")
		span.RecordError(noOutputErr)
		span.SetAttribute("error_code", mcptypes.ErrorCodeExecutionFailed)
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
	chunks := []map[string]interface{}{}

	if strings.HasPrefix(stdoutStr, chunkPrefix) || strings.HasPrefix(stdoutStr, resultPrefix) {
		chunks, subprocResp = e.parseStreamingOutput(stdoutStr, requestID)
	} else {
		if err := json.Unmarshal([]byte(stdoutStr), &subprocResp); err != nil {
			span.RecordError(err)
			span.SetAttribute("error_code", mcptypes.ErrorCodeExecutionFailed)
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
	}

	if subprocResp.RequestID != requestID {
		log.Warn().
			Str("expected", requestID).
			Str("got", subprocResp.RequestID).
			Msg("Request ID mismatch in subprocess response")
		span.SetAttribute("request_id_mismatch", true)
	}

	result := &ExecuteResult{
		Success:           subprocResp.Success,
		Content:           subprocResp.Content,
		StructuredContent: subprocResp.StructuredContent,
		Error:             subprocResp.Error,
		Stderr:            stderrStr,
		Chunks:            chunks,
	}

	// Record final result in span
	span.SetAttribute("success", result.Success)
	span.SetAttribute("content_count", len(result.Content))
	span.SetAttribute("chunks_count", len(chunks))

	if result.Error != nil {
		span.RecordError(fmt.Errorf("execution error: %s", result.Error.Message))
		span.SetAttribute("error_code", result.Error.Code)
	}

	if subprocResp.StructuredContent == nil {
		result.StructuredContent = make(map[string]interface{})
	}
	if len(chunks) > 0 {
		result.StructuredContent["streaming_chunks"] = chunks
	}

	return result, nil
}

// parseStreamingOutput handles chunked output from long-running tools.
//
// Some tools emit progress updates or partial results before completing.
// This function parses lines prefixed with "__CHUNK__:" as intermediate
// results and "__RESULT__:" as the final response.
//
// Parameters:
//   - output: the raw stdout string from the subprocess
//   - requestID: the original request ID for validation
//
// Returns:
//   - chunks: any parsed streaming chunks
//   - SubprocessResponse: the final response (from __RESULT__ line or last resort)
func (e *Executor) parseStreamingOutput(output string, requestID string) ([]map[string]interface{}, mcptypes.SubprocessResponse) {
	chunks := []map[string]interface{}{}
	var finalResp mcptypes.SubprocessResponse

	if !strings.HasPrefix(output, chunkPrefix) && !strings.HasPrefix(output, resultPrefix) {
		var resp mcptypes.SubprocessResponse
		err := json.Unmarshal([]byte(output), &resp)
		if err == nil {
			resp.RequestID = requestID
			return chunks, resp
		}
		return chunks, mcptypes.SubprocessResponse{
			RequestID: requestID,
			Success:   false,
			Error: &mcptypes.SubprocessError{
				Code:    mcptypes.ErrorCodeExecutionFailed,
				Message: fmt.Sprintf("failed to parse subprocess response: %v", err),
			},
		}
	}

	scanner := bufio.NewScanner(bytes.NewBufferString(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, chunkPrefix) {
			jsonStr := line[len(chunkPrefix):]
			var chunk map[string]interface{}
			if jsonErr := json.Unmarshal([]byte(jsonStr), &chunk); jsonErr == nil {
				chunks = append(chunks, chunk)
			}
		} else if strings.HasPrefix(line, resultPrefix) {
			jsonStr := line[len(resultPrefix):]
			if jsonErr := json.Unmarshal([]byte(jsonStr), &finalResp); jsonErr == nil {
				finalResp.RequestID = requestID
			}
		}
	}

	if finalResp.RequestID == "" {
		finalResp.RequestID = requestID
		finalResp.Success = false
		finalResp.Error = &mcptypes.SubprocessError{
			Code:    mcptypes.ErrorCodeExecutionFailed,
			Message: "no result found in streaming output",
		}
	}

	return chunks, finalResp
}

// buildEnvironment converts a string map to a slice of "KEY=value" strings.
//
// This format is required by os/exec.Cmd.Env. The function preserves
// the order of environment variables.
//
// Parameters:
//   - envMap: a map of environment variable names to values
//
// Returns:
//
//	a slice of "KEY=value" strings suitable for cmd.Env
func buildEnvironment(envMap map[string]string) []string {
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

// validateInputArguments validates that provided arguments match the tool's input schema.
//
// The validation is best-effort and allows extra fields. It checks:
//   - Required fields are present
//   - Types match the schema (string, number, integer, boolean, array, object)
//   - Enum values are in the allowed list
//
// Parameters:
//   - inputSchema: the JSON schema to validate against (nil = skip validation)
//   - args: the arguments provided by the client
//
// Returns:
//
//	error: if validation fails, including which field failed
func validateInputArguments(inputSchema map[string]interface{}, args map[string]interface{}) error {
	if inputSchema == nil {
		// No schema defined, accept anything
		return nil
	}

	// Check for required fields
	if required, ok := inputSchema["required"].([]interface{}); ok {
		for _, r := range required {
			field, ok := r.(string)
			if !ok {
				continue
			}
			if _, exists := args[field]; !exists {
				return fmt.Errorf("required field '%s' is missing", field)
			}
		}
	}

	// Get properties schema if defined
	properties, ok := inputSchema["properties"].(map[string]interface{})
	if !ok || properties == nil {
		// No properties defined, accept anything
		return nil
	}

	// Validate each provided argument against its property schema
	for argName, argValue := range args {
		prop, exists := properties[argName]
		if !exists {
			// Extra fields are allowed (best effort validation)
			continue
		}

		propSchema, ok := prop.(map[string]interface{})
		if !ok {
			continue
		}

		// Validate type if specified
		if expectedType, ok := propSchema["type"].(string); ok {
			if !validateType(argValue, expectedType) {
				return fmt.Errorf("argument '%s' has invalid type: expected %s, got %T", argName, expectedType, argValue)
			}
		}

		// Validate enum if specified
		if enumVals, ok := propSchema["enum"].([]interface{}); ok {
			if !validateEnum(argValue, enumVals) {
				return fmt.Errorf("argument '%s' has invalid value: not in allowed enum values", argName)
			}
		}
	}

	return nil
}

// validateType checks if a value matches the expected JSON schema type.
//
// JSON numbers are unmarshaled as float64 in Go, so integer values may
// appear as float64. This function handles that conversion.
//
// Parameters:
//   - value: the actual value to check
//   - schemaType: the expected JSON schema type (string, number, integer, boolean, array, object, null)
//
// Returns:
//
//	true if the value matches the expected type
func validateType(value interface{}, schemaType string) bool {
	switch schemaType {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		switch value.(type) {
		case float64, float32, int, int32, int64:
			return true
		}
		return false
	case "integer":
		switch value.(type) {
		case float64: // JSON unmarshals numbers as float64
			// Check if it's actually an integer
			f := value.(float64)
			return f == float64(int64(f))
		case int, int32, int64:
			return true
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]interface{})
		return ok
	case "object":
		_, ok := value.(map[string]interface{})
		return ok
	case "null":
		return value == nil
	default:
		return true // Unknown type, accept
	}
}

// validateEnum checks if a value is in the allowed enum values.
//
// Type coercion is performed for numeric types (float64 vs int).
//
// Parameters:
//   - value: the value to check
//   - enumVals: the list of allowed values
//
// Returns:
//
//	true if the value is in the enum list
func validateEnum(value interface{}, enumVals []interface{}) bool {
	for _, enumVal := range enumVals {
		if compareValues(value, enumVal) {
			return true
		}
	}
	return false
}

// compareValues compares two values for equality with type coercion.
//
// Handles string, float64, and bool comparisons. Floats are compared
// numerically, not by exact representation.
//
// Parameters:
//   - a: first value
//   - b: second value
//
// Returns:
//
//	true if the values are equal
func compareValues(a, b interface{}) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case float64:
		switch bv := b.(type) {
		case float64:
			return av == bv
		case int:
			return av == float64(bv)
		}
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	}
	return false
}

// ValidateToolConfig performs static validation on a tool configuration.
//
// This is useful for CI/CD pipelines or configuration editors to catch
// errors before runtime.
//
// Parameters:
//   - toolCfg: the tool configuration to validate
//
// Returns:
//
//	error: if validation fails (missing name or command)
func ValidateToolConfig(toolCfg *config.ToolConfig) error {
	if toolCfg.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if toolCfg.Command == "" {
		return fmt.Errorf("tool command is required")
	}
	return nil
}
