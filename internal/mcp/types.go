package mcp

import (
	"bytes"
	"encoding/json"
)

// SubprocessRequest is the JSON structure sent to Python scripts via STDIN
type SubprocessRequest struct {
	RequestID string                 `json:"request_id"`
	ToolName  string                 `json:"tool_name"`
	Arguments map[string]interface{} `json:"arguments"`
	Context   SubprocessContext      `json:"context"`
}

// SubprocessContext contains environment/configuration passed to scripts
type SubprocessContext struct {
	LLMAPIURL   string `json:"llm_api_url,omitempty"`
	LLMModel    string `json:"llm_model,omitempty"`
	DatabaseURL string `json:"database_url,omitempty"`
	WorkingDir  string `json:"working_dir,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
}

// SubprocessResponse is the JSON structure received from Python scripts via STDOUT
type SubprocessResponse struct {
	Success           bool                   `json:"success"`
	RequestID         string                 `json:"request_id"`
	Content           []ContentItem          `json:"content,omitempty"`
	StructuredContent map[string]interface{} `json:"structured_content,omitempty"`
	Error             *SubprocessError       `json:"error,omitempty"`
}

// ContentItem represents a content item in the response (text, image, resource, etc.)
type ContentItem struct {
	Type     string           `json:"type"`
	Text     string           `json:"text,omitempty"`
	Data     string           `json:"data,omitempty"`
	MIMEType string           `json:"mimeType,omitempty"`
	Resource *ResourceContent `json:"resource,omitempty"`
}

// ResourceContent represents a resource (file, PDF, etc.) in the response
type ResourceContent struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
}

// SubprocessError represents an error from a Python script
type SubprocessError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// UnmarshalJSON decodes a SubprocessError, tolerating any JSON shape for the
// optional `details` field. The documented contract is a string, but tools
// have emitted arrays or objects (e.g. per-file read errors); a non-string
// details must never abort parsing of the whole subprocess response, because
// code and message are what reach the client.
func (e *SubprocessError) UnmarshalJSON(data []byte) error {
	type shape struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details,omitempty"`
	}
	var s shape
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	e.Code = s.Code
	e.Message = s.Message
	e.Details = normalizeErrorDetails(s.Details)
	return nil
}

// normalizeErrorDetails converts the wire form of details into the internal
// string form: JSON strings are unwrapped, null/absent becomes empty, and any
// other JSON value is stored as its compact JSON encoding.
func normalizeErrorDetails(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err == nil {
		return compact.String()
	}
	return string(raw)
}

// Common error codes
const (
	ErrorCodeFileNotFound      = "FILE_NOT_FOUND"
	ErrorCodeInvalidInput      = "INVALID_INPUT"
	ErrorCodeExecutionFailed   = "EXECUTION_FAILED"
	ErrorCodeTimeout           = "TIMEOUT"
	ErrorCodeDependencyMissing = "DEPENDENCY_MISSING"
	ErrorCodeLLMError          = "LLM_ERROR"
	ErrorCodeDatabaseError     = "DATABASE_ERROR"
)
