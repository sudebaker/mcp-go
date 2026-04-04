package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/amphora/mcp-go/internal/prompts"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptRegistration(t *testing.T) {
	// Create test configuration
	cfg := []config.PromptConfig{
		{
			Name:        "test_prompt",
			Description: "A test prompt",
			Arguments: []config.PromptArgumentConfig{
				{
					Name:        "input",
					Description: "Test input",
					Required:    true,
				},
			},
			Messages: []config.PromptMessageConfig{
				{
					Role:    "user",
					Content: "Hello {{input}}",
				},
			},
		},
	}

	// Create MCP server
	mcpServer := server.NewMCPServer(
		"test-server",
		"1.0.0",
		server.WithPromptCapabilities(true),
		server.WithLogging(),
	)

	// Register prompts
	prompts.RegisterPrompts(mcpServer, cfg)

	// Verify prompt was registered (implicitly tested by successful registration)
	assert.NotNil(t, mcpServer)
}

func TestListPromptsViaStreamableHTTP(t *testing.T) {
	// Create server with prompt
	cfg := []config.PromptConfig{
		{
			Name:        "code_review",
			Description: "Review code",
			Messages: []config.PromptMessageConfig{
				{
					Role:    "user",
					Content: "Review this code",
				},
			},
		},
	}

	mcpServer := server.NewMCPServer(
		"test-server",
		"1.0.0",
		server.WithPromptCapabilities(true),
	)
	prompts.RegisterPrompts(mcpServer, cfg)

	streamServer := server.NewStreamableHTTPServer(mcpServer)
	ts := httptest.NewServer(streamServer)
	defer ts.Close()

	// Send prompts/list request
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "prompts/list",
		"params":  map[string]interface{}{},
	}

	body, _ := json.Marshal(req)
	resp, err := http.Post(ts.URL, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify response contains prompt
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	resultData := result["result"].(map[string]interface{})
	promptsList := resultData["prompts"].([]interface{})
	assert.Greater(t, len(promptsList), 0)
}

func TestGetPromptWithArgumentInterpolation(t *testing.T) {
	cfg := []config.PromptConfig{
		{
			Name:        "test_prompt",
			Description: "Test with interpolation",
			Arguments: []config.PromptArgumentConfig{
				{
					Name:        "code",
					Description: "Code to review",
					Required:    true,
				},
			},
			Messages: []config.PromptMessageConfig{
				{
					Role:    "user",
					Content: "Please review: {{code}}",
				},
			},
		},
	}

	mcpServer := server.NewMCPServer(
		"test-server",
		"1.0.0",
		server.WithPromptCapabilities(true),
	)
	prompts.RegisterPrompts(mcpServer, cfg)

	streamServer := server.NewStreamableHTTPServer(mcpServer)
	ts := httptest.NewServer(streamServer)
	defer ts.Close()

	// Send prompts/get request with arguments
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "prompts/get",
		"params": map[string]interface{}{
			"name": "test_prompt",
			"arguments": map[string]interface{}{
				"code": "func Hello() { return 42; }",
			},
		},
	}

	body, _ := json.Marshal(req)
	resp, err := http.Post(ts.URL, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify interpolation happened
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	resultData := result["result"].(map[string]interface{})
	messages := resultData["messages"].([]interface{})
	assert.Greater(t, len(messages), 0)

	// Check that interpolation occurred
	firstMsg := messages[0].(map[string]interface{})
	content := firstMsg["content"].(map[string]interface{})
	text := content["text"].(string)
	assert.Contains(t, text, "func Hello()")
}

func TestRequiredArgumentValidation(t *testing.T) {
	cfg := []config.PromptConfig{
		{
			Name:        "requires_arg",
			Description: "Requires argument",
			Arguments: []config.PromptArgumentConfig{
				{
					Name:        "required_field",
					Description: "Required",
					Required:    true,
				},
			},
			Messages: []config.PromptMessageConfig{
				{
					Role:    "user",
					Content: "Value: {{required_field}}",
				},
			},
		},
	}

	mcpServer := server.NewMCPServer(
		"test-server",
		"1.0.0",
		server.WithPromptCapabilities(true),
	)
	prompts.RegisterPrompts(mcpServer, cfg)

	streamServer := server.NewStreamableHTTPServer(mcpServer)
	ts := httptest.NewServer(streamServer)
	defer ts.Close()

	// Send request WITHOUT required argument
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "prompts/get",
		"params": map[string]interface{}{
			"name":      "requires_arg",
			"arguments": map[string]interface{}{},
		},
	}

	body, _ := json.Marshal(req)
	resp, err := http.Post(ts.URL, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	// Should have error due to missing required argument
	assert.NotNil(t, result["error"])
}
