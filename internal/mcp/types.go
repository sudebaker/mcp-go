package mcp

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
}

// SubprocessResponse is the JSON structure received from Python scripts via STDOUT
type SubprocessResponse struct {
	Success           bool                   `json:"success"`
	RequestID         string                 `json:"request_id"`
	Content           []ContentItem          `json:"content,omitempty"`
	StructuredContent map[string]interface{} `json:"structured_content,omitempty"`
	Error             *SubprocessError       `json:"error,omitempty"`
}

// ContentItem represents a content item in the response (text, image, etc.)
type ContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// SubprocessError represents an error from a Python script
type SubprocessError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// Common error codes
const (
	ErrorCodeFileNotFound     = "FILE_NOT_FOUND"
	ErrorCodeInvalidInput     = "INVALID_INPUT"
	ErrorCodeExecutionFailed  = "EXECUTION_FAILED"
	ErrorCodeTimeout          = "TIMEOUT"
	ErrorCodeDependencyMissing = "DEPENDENCY_MISSING"
	ErrorCodeLLMError         = "LLM_ERROR"
	ErrorCodeDatabaseError    = "DATABASE_ERROR"
)
