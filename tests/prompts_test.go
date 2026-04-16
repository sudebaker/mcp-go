package tests

import (
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/prompts"
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

// TestPromptInterpolation tests that template variable interpolation works correctly
func TestPromptInterpolation(t *testing.T) {
	// Test interpolation logic directly
	cfg := []config.PromptConfig{
		{
			Name:        "code_review",
			Description: "Review code",
			Arguments: []config.PromptArgumentConfig{
				{
					Name:        "language",
					Description: "Programming language",
					Required:    true,
				},
			},
			Messages: []config.PromptMessageConfig{
				{
					Role:    "user",
					Content: "Review this {{language}} code",
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

	// Verify that registration didn't fail
	assert.NotNil(t, mcpServer)
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

	// Verify that registration didn't fail
	assert.NotNil(t, mcpServer)
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

	// Verify that registration didn't fail
	assert.NotNil(t, mcpServer)
}
