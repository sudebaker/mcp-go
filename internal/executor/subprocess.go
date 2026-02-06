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

const (
	chunkPrefix  = "__CHUNK__:"
	resultPrefix = "__RESULT__:"
)

type Executor struct {
	config *config.Config
	tracer *tracing.Tracer
}

func New(cfg *config.Config) *Executor {
	return &Executor{
		config: cfg,
		tracer: tracing.NoOpTracer(),
	}
}

func NewWithTracer(cfg *config.Config, tracer *tracing.Tracer) *Executor {
	if tracer == nil {
		tracer = tracing.NoOpTracer()
	}
	return &Executor{
		config: cfg,
		tracer: tracer,
	}
}

type ExecuteResult struct {
	Success           bool
	Content           []mcptypes.ContentItem
	StructuredContent map[string]interface{}
	Error             *mcptypes.SubprocessError
	Stderr            string
	Chunks            []map[string]interface{}
}

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

func buildEnvironment(envMap map[string]string) []string {
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

// validateInputArguments validates that the provided arguments match the tool's input schema
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

// validateType checks if a value matches the expected JSON schema type
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

// validateEnum checks if a value is in the allowed enum values
func validateEnum(value interface{}, enumVals []interface{}) bool {
	for _, enumVal := range enumVals {
		if compareValues(value, enumVal) {
			return true
		}
	}
	return false
}

// compareValues compares two values for equality
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

func ValidateToolConfig(toolCfg *config.ToolConfig) error {
	if toolCfg.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if toolCfg.Command == "" {
		return fmt.Errorf("tool command is required")
	}
	return nil
}
