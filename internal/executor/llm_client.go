package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"
)

const (
	maxPromptLength = 100000 // Maximum character count in prompt
	maxPromptBytes  = 1 << 20 // Maximum byte size of prompt (1MB)
)

var (
	ErrPromptTooLong  = errors.New("prompt exceeds maximum length")
	ErrPromptTooLarge = errors.New("prompt exceeds maximum size")
	ErrInvalidUTF8    = errors.New("prompt contains invalid UTF-8")
)

// LLMClient manages HTTP connections to LLM backends (Ollama, vLLM, etc.).
// It provides connection pooling and implements safety checks for prompt validation
// before sending to the LLM API.
//
// Thread-safe: Yes (exported methods are safe for concurrent use)
type LLMClient struct {
	client   *http.Client // HTTP client with connection pooling
	endpoint string       // Base URL for LLM API (e.g., "http://ollama:11434")
	model    string       // Default model name
}

// LLMRequest represents a request payload to the LLM API.
type LLMRequest struct {
	Model   string            `json:"model"`    // Model name
	Prompt  string            `json:"prompt"`   // Input prompt
	Stream  bool              `json:"stream"`    // Streaming mode (currently always false)
	Options LLMRequestOptions `json:"options,omitempty"` // Model-specific options
}

// LLMRequestOptions configures LLM inference behavior.
type LLMRequestOptions struct {
	Temperature float64 `json:"temperature,omitempty"` // Sampling temperature (0-2)
	NumPredict  int     `json:"num_predict,omitempty"`  // Max tokens to generate
	TopK        int     `json:"top_k,omitempty"`        // Top-K sampling parameter
	TopP        float64 `json:"top_p,omitempty"`        // Nucleus sampling threshold
}

// LLMResponse represents a response from the LLM API.
type LLMResponse struct {
	Response string `json:"response"` // Generated text
	Error    string `json:"error,omitempty"` // Error message if failed
}

// NewLLMClient creates an LLM client with connection pooling for efficiency.
//
// Args:
//   endpoint: Base URL of LLM API (e.g., "http://ollama:11434")
//   model: Default model name for all Call() requests
//   timeout: HTTP request timeout duration
//
// Returns:
//   Configured LLMClient with 10 max idle connections per host
func NewLLMClient(endpoint string, model string, timeout time.Duration) *LLMClient {
	return &LLMClient{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		endpoint: endpoint,
		model:    model,
	}
}

// Call sends a prompt to the LLM using the default model.
//
// Args:
//   ctx: Context for cancellation and timeout
//   prompt: Text prompt to send to LLM
//
// Returns:
//   Generated text response, or error if validation fails or API returns error
//
// Errors:
//   ErrPromptTooLong: prompt exceeds 100,000 characters
//   ErrPromptTooLarge: prompt exceeds 1MB in bytes
//   ErrInvalidUTF8: prompt contains invalid UTF-8 sequences
func (c *LLMClient) Call(ctx context.Context, prompt string) (string, error) {
	if err := validatePrompt(prompt); err != nil {
		return "", err
	}
	return c.CallWithModel(ctx, c.model, prompt)
}

// CallWithModel sends a prompt to a specific LLM model.
//
// Args:
//   ctx: Context for cancellation and timeout
//   model: Model name to use (e.g., "llama3", "mistral")
//   prompt: Text prompt to send
//
// Returns:
//   Generated text response
//
// Errors:
//   Same as Call() plus HTTP errors from the API
func (c *LLMClient) CallWithModel(ctx context.Context, model string, prompt string) (string, error) {
	if err := validatePrompt(prompt); err != nil {
		return "", err
	}

	req := LLMRequest{
		Model:  model,
		Prompt: prompt,
		Stream: false,
		Options: LLMRequestOptions{
			Temperature: 0.1,
			NumPredict:  2000,
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &LLMError{StatusCode: resp.StatusCode, Message: "LLM API returned non-OK status"}
	}

	var result LLMResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if result.Error != "" {
		return "", &LLMError{Message: result.Error}
	}

	return result.Response, nil
}

// CallWithOptions sends a prompt with custom inference options.
//
// Args:
//   ctx: Context for cancellation and timeout
//   prompt: Text prompt to send
//   opts: Custom inference options (temperature, top_k, top_p, num_predict)
//
// Returns:
//   Generated text response
func (c *LLMClient) CallWithOptions(ctx context.Context, prompt string, opts LLMRequestOptions) (string, error) {
	if err := validatePrompt(prompt); err != nil {
		return "", err
	}

	req := LLMRequest{
		Model:   c.model,
		Prompt:  prompt,
		Stream:  false,
		Options: opts,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &LLMError{StatusCode: resp.StatusCode, Message: "LLM API returned non-OK status"}
	}

	var result LLMResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if result.Error != "" {
		return "", &LLMError{Message: result.Error}
	}

	return result.Response, nil
}

// LLMError represents an error from the LLM API or client.
type LLMError struct {
	StatusCode int    // HTTP status code (0 if not applicable)
	Message    string // Error description
}

func (e *LLMError) Error() string {
	if e.StatusCode != 0 {
		return "LLM error: " + e.Message
	}
	return "LLM error: " + e.Message
}

// CloseIdleConnections releases idle HTTP connections.
// Call this during graceful shutdown to prevent connection leaks.
func (c *LLMClient) CloseIdleConnections() {
	c.client.CloseIdleConnections()
}

// validatePrompt performs safety checks on the input prompt.
//
// Args:
//   prompt: Raw input string to validate
//
// Returns:
//   nil if valid, or a specific error (ErrPromptTooLong, ErrPromptTooLarge, ErrInvalidUTF8)
func validatePrompt(prompt string) error {
	if len(prompt) > maxPromptLength {
		return ErrPromptTooLong
	}

	if len([]byte(prompt)) > maxPromptBytes {
		return ErrPromptTooLarge
	}

	if !utf8.ValidString(prompt) {
		return ErrInvalidUTF8
	}

	return nil
}
