package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type LLMClient struct {
	client   *http.Client
	endpoint string
	model    string
}

type LLMRequest struct {
	Model   string            `json:"model"`
	Prompt  string            `json:"prompt"`
	Stream  bool              `json:"stream"`
	Options LLMRequestOptions `json:"options,omitempty"`
}

type LLMRequestOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
	TopK        int     `json:"top_k,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`
}

type LLMResponse struct {
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

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

func (c *LLMClient) Call(ctx context.Context, prompt string) (string, error) {
	return c.CallWithModel(ctx, c.model, prompt)
}

func (c *LLMClient) CallWithModel(ctx context.Context, model string, prompt string) (string, error) {
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

func (c *LLMClient) CallWithOptions(ctx context.Context, prompt string, opts LLMRequestOptions) (string, error) {
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

type LLMError struct {
	StatusCode int
	Message    string
}

func (e *LLMError) Error() string {
	if e.StatusCode != 0 {
		return "LLM error: " + e.Message
	}
	return "LLM error: " + e.Message
}

func (c *LLMClient) CloseIdleConnections() {
	c.client.CloseIdleConnections()
}
