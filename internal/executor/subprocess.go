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
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

const (
	chunkPrefix  = "__CHUNK__:"
	resultPrefix = "__RESULT__:"
)

type Executor struct {
	config *config.Config
}

func New(cfg *config.Config) *Executor {
	return &Executor{config: cfg}
}

func (e *Executor) UpdateConfig(cfg *config.Config) {
	e.config = cfg
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
	toolCfg := e.config.GetToolByName(toolName)
	if toolCfg == nil {
		return nil, fmt.Errorf("tool '%s' not found in configuration", toolName)
	}

	timeout := toolCfg.Timeout
	if timeout == 0 {
		timeout = e.config.Execution.DefaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
	chunks := []map[string]interface{}{}

	if strings.HasPrefix(stdoutStr, chunkPrefix) || strings.HasPrefix(stdoutStr, resultPrefix) {
		chunks, subprocResp = e.parseStreamingOutput(stdoutStr, requestID)
	} else {
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
	}

	if subprocResp.RequestID != requestID {
		log.Warn().
			Str("expected", requestID).
			Str("got", subprocResp.RequestID).
			Msg("Request ID mismatch in subprocess response")
	}

	result := &ExecuteResult{
		Success:           subprocResp.Success,
		Content:           subprocResp.Content,
		StructuredContent: subprocResp.StructuredContent,
		Error:             subprocResp.Error,
		Stderr:            stderrStr,
		Chunks:            chunks,
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

func ValidateToolConfig(toolCfg *config.ToolConfig) error {
	if toolCfg.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if toolCfg.Command == "" {
		return fmt.Errorf("tool command is required")
	}
	return nil
}
