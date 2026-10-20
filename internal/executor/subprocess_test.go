package executor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sudebaker/mcp-go/internal/config"
)

func TestParseStreamingOutput(t *testing.T) {
	output := `__CHUNK__:{"type": "status", "data": {"message": "Loading data"}}
__CHUNK__:{"type": "data_loaded", "data": {"rows": 100, "columns": 5}}
__CHUNK__:{"type": "status", "data": {"message": "Executing code"}}
__RESULT__:{"success": true, "request_id": "test-123", "content": [{"type": "text", "text": "Done"}]}`

	chunks, resp := parseStreamingOutput(output, "test-123")

	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d", len(chunks))
	}

	if !resp.Success {
		t.Error("expected success to be true")
	}

	if resp.RequestID != "test-123" {
		t.Errorf("expected request_id 'test-123', got '%s'", resp.RequestID)
	}

	if len(resp.Content) != 1 {
		t.Errorf("expected 1 content item, got %d", len(resp.Content))
	}
}

func TestParseStreamingOutput_NoResult(t *testing.T) {
	output := `__CHUNK__:{"type": "status", "data": {"message": "Loading data"}}`

	chunks, resp := parseStreamingOutput(output, "test-456")

	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}

	if resp.Success {
		t.Error("expected success to be false when no result")
	}

	if resp.Error == nil {
		t.Error("expected error to be set")
	}
}

func TestParseStreamingOutput_Empty(t *testing.T) {
	chunks, resp := parseStreamingOutput("", "test-789")

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}

	if resp.Success {
		t.Error("expected success to be false")
	}
}

func TestParseStreamingOutput_InvalidJSON(t *testing.T) {
	output := `__CHUNK__:{invalid json}
__RESULT__:{"success": true}`

	chunks, _ := parseStreamingOutput(output, "test-invalid")

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for invalid JSON, got %d", len(chunks))
	}
}

func TestParseStreamingOutput_StandardJSON(t *testing.T) {
	output := `{"success": true, "request_id": "test-standard", "content": [{"type": "text", "text": "hello"}]}`

	chunks, resp := parseStreamingOutput(output, "test-standard")

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for standard JSON, got %d", len(chunks))
	}

	if !resp.Success {
		t.Error("expected success to be true")
	}
}

func TestExecuteResult_StructuredContent(t *testing.T) {
	result := &ExecuteResult{
		Success:           true,
		Content:           nil,
		StructuredContent: map[string]interface{}{},
		Chunks:            []map[string]interface{}{},
	}

	result.StructuredContent["streaming_chunks"] = result.Chunks

	data, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(data), "streaming_chunks") {
		t.Error("expected streaming_chunks in structured content")
	}
}

func TestValidateToolConfig(t *testing.T) {
	tests := []struct {
		name    string
		tool    config.ToolConfig
		wantErr bool
	}{
		{
			name: "valid tool",
			tool: config.ToolConfig{
				Name:    "test",
				Command: "python3",
			},
			wantErr: false,
		},
		{
			name: "empty name",
			tool: config.ToolConfig{
				Name:    "",
				Command: "python3",
			},
			wantErr: true,
		},
		{
			name: "empty command",
			tool: config.ToolConfig{
				Name:    "test",
				Command: "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolConfig(&tt.tool)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateToolConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExecute_NotFound(t *testing.T) {
	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 5
	exec := New(cfg)

	_, err := exec.Execute(context.Background(), "nonexistent", map[string]interface{}{})

	if err == nil {
		t.Error("expected error for nonexistent tool")
	}
}

// Regression for t_b55e3c26: config_auditor answered errors whose details
// were an array; Execute must decode the response and surface the tool's
// real code and message instead of a "failed to parse subprocess response"
// wrapper. Exercises the real Execute() unmarshal path.
func TestExecute_SurfacesToolErrorWithArrayDetails(t *testing.T) {
	payload := `{"success":false,"request_id":"req-1","error":{"code":"NO_FILES","message":"resource not found","details":[{"file":"res://XXXX","error":"resource not found"}]}}`

	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 1
	cfg.Execution.DefaultTimeout = 5 * time.Second
	cfg.Execution.WorkingDir = t.TempDir()
	cfg.Tools = []config.ToolConfig{{
		Name:    "config_auditor",
		Command: "/bin/sh",
		Args:    []string{"-c", "echo '" + payload + "'"},
	}}
	exec := New(cfg)

	result, err := exec.Execute(context.Background(), "config_auditor", map[string]interface{}{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Error("expected success to be false")
	}
	if result.Error == nil {
		t.Fatal("expected error to be set")
	}
	if result.Error.Code != "NO_FILES" {
		t.Errorf("expected code 'NO_FILES', got %q", result.Error.Code)
	}
	if result.Error.Message != "resource not found" {
		t.Errorf("expected the tool's message, got %q", result.Error.Message)
	}
	if strings.Contains(result.Error.Message, "failed to parse") {
		t.Errorf("tool message replaced by parse error: %q", result.Error.Message)
	}
}
