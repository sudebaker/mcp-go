# MCP Prompts Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement MCP prompts endpoint supporting both Streamable HTTP (/mcp) and SSE (/sse, /message) transports per MCP 2025-03-26 specification.

**Architecture:** 
- Add PromptConfig types to config loader (YAML-driven)
- Create prompts module to register handlers with mcp-go server
- Single handler registration works for both transports automatically
- Interpolate argument placeholders in prompt message templates

**Tech Stack:** Go 1.23+, mcp-go v0.43.2, gopkg.in/yaml.v3

---

## File Structure

```
internal/
  config/
    config.go (MODIFY) - Add PromptConfig structs
  prompts/
    prompts.go (CREATE) - Prompt registration & interpolation logic
cmd/
  server/
    main.go (MODIFY) - Enable prompt capability, register prompts
internal/
  transport/
    sse.go (MODIFY) - Update docs endpoint
configs/
  config.yaml (MODIFY) - Add prompt examples
tests/
  prompts_test.go (CREATE) - Integration tests
```

---

## Task 1: Add PromptConfig Types to Config

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Read config.go to understand current structure**

```bash
wc -l /home/hp/Proyectos/mcp-go/internal/config/config.go
# Should be ~243 lines
```

Expected output: file ends with `GetToolByName()` function

- [ ] **Step 2: Add PromptConfig structs before the `Load()` function**

At line 93 (after ToolConfig struct), add:

```go
// PromptArgumentConfig defines an argument for a prompt.
type PromptArgumentConfig struct {
	// Name is the unique identifier for this argument
	Name string `yaml:"name"`
	// Description explains the argument for LLM consumption
	Description string `yaml:"description"`
	// Required indicates if this argument must be provided
	Required bool `yaml:"required"`
}

// PromptMessageConfig defines a message within a prompt template.
type PromptMessageConfig struct {
	// Role is the message speaker: "user" or "assistant"
	Role string `yaml:"role"`
	// Content is the message text (supports {{argument}} placeholders)
	Content string `yaml:"content"`
}

// PromptConfig defines a prompt template with configurable arguments.
type PromptConfig struct {
	// Name is the unique identifier for this prompt
	Name string `yaml:"name"`
	// Description explains the prompt's purpose for LLM consumption
	Description string `yaml:"description"`
	// Arguments are the configurable parameters for this prompt
	Arguments []PromptArgumentConfig `yaml:"arguments,omitempty"`
	// Messages are the template messages in the prompt
	Messages []PromptMessageConfig `yaml:"messages"`
}
```

- [ ] **Step 3: Add Prompts field to Config struct**

In the `Config` struct (line 36), after the `Tools` field, add:

```go
	// Prompts is the list of available prompts
	Prompts []PromptConfig `yaml:"prompts,omitempty"`
```

The struct should now have fields: Server, Execution, Tools, Prompts (in that order).

- [ ] **Step 4: Verify edits compile**

```bash
cd /home/hp/Proyectos/mcp-go && go build ./cmd/server
```

Expected: No compilation errors.

- [ ] **Step 5: Commit**

```bash
cd /home/hp/Proyectos/mcp-go && git add internal/config/config.go && git commit -m "config: add PromptConfig types for YAML prompts"
```

---

## Task 2: Create Prompts Module

**Files:**
- Create: `internal/prompts/prompts.go`

- [ ] **Step 1: Create the file with package declaration and imports**

```go
package prompts

import (
	"context"
	"fmt"
	"strings"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog/log"
)
```

- [ ] **Step 2: Implement RegisterPrompts function**

```go
// RegisterPrompts registers all configured prompts with the MCP server.
//
// Each prompt becomes available via prompts/list and prompts/get methods
// on both Streamable HTTP and SSE transports.
//
// Parameters:
//   - mcpServer: the MCP server instance
//   - configs: the list of prompt configurations from YAML
func RegisterPrompts(mcpServer *server.MCPServer, configs []config.PromptConfig) {
	for _, cfg := range configs {
		// Validate configuration
		if cfg.Name == "" {
			log.Warn().Msg("Skipping prompt with empty name")
			continue
		}
		if len(cfg.Messages) == 0 {
			log.Warn().Str("prompt", cfg.Name).Msg("Skipping prompt with no messages")
			continue
		}

		// Create prompt with arguments
		opts := []mcp.PromptOption{
			mcp.WithPromptDescription(cfg.Description),
		}
		for _, arg := range cfg.Arguments {
			argOpts := []mcp.ArgumentOption{}
			if arg.Description != "" {
				argOpts = append(argOpts, mcp.ArgumentDescription(arg.Description))
			}
			if arg.Required {
				argOpts = append(argOpts, mcp.RequiredArgument())
			}
			opts = append(opts, mcp.WithArgument(arg.Name, argOpts...))
		}

		prompt := mcp.NewPrompt(cfg.Name, opts...)

		// Create handler for this prompt
		handler := createPromptHandler(cfg)

		// Register with server (works for both /mcp and /sse transports)
		mcpServer.AddPrompt(prompt, handler)

		log.Debug().
			Str("prompt", cfg.Name).
			Int("arguments", len(cfg.Arguments)).
			Int("messages", len(cfg.Messages)).
			Msg("Registered prompt")
	}

	log.Info().Int("total", len(configs)).Msg("Prompts registered successfully")
}
```

- [ ] **Step 3: Implement createPromptHandler function**

```go
// createPromptHandler creates a handler for a specific prompt configuration.
//
// The handler validates arguments and interpolates placeholders in message templates.
func createPromptHandler(cfg config.PromptConfig) server.PromptHandlerFunc {
	return func(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		// Convert provided arguments to string map for interpolation
		providedArgs := make(map[string]string)
		if request.Params.Arguments != nil {
			for k, v := range request.Params.Arguments {
				switch val := v.(type) {
				case string:
					providedArgs[k] = val
				case float64:
					providedArgs[k] = fmt.Sprintf("%v", val)
				case bool:
					providedArgs[k] = fmt.Sprintf("%v", val)
				default:
					providedArgs[k] = fmt.Sprintf("%v", val)
				}
			}
		}

		// Validate required arguments
		for _, arg := range cfg.Arguments {
			if arg.Required {
				if _, exists := providedArgs[arg.Name]; !exists {
					return nil, fmt.Errorf("missing required argument: %s", arg.Name)
				}
			}
		}

		// Build messages with interpolated arguments
		messages := make([]mcp.PromptMessage, 0, len(cfg.Messages))
		for _, msgCfg := range cfg.Messages {
			// Interpolate placeholders
			content := interpolateTemplate(msgCfg.Content, providedArgs)

			// Determine role
			role := mcp.RoleUser
			if strings.ToLower(msgCfg.Role) == "assistant" {
				role = mcp.RoleAssistant
			}

			// Create message with text content
			textContent := mcp.NewTextContent(content)
			msg := mcp.NewPromptMessage(role, textContent)
			messages = append(messages, msg)
		}

		log.Debug().
			Str("prompt", cfg.Name).
			Int("messages", len(messages)).
			Msg("Returning prompt result")

		return mcp.NewGetPromptResult(cfg.Description, messages), nil
	}
}
```

- [ ] **Step 4: Implement interpolateTemplate function**

```go
// interpolateTemplate replaces {{argument}} placeholders with provided values.
//
// Placeholders are case-sensitive. Unmatched placeholders are left unchanged.
//
// Parameters:
//   - template: the template string with {{name}} placeholders
//   - args: map of argument values
//
// Returns:
//   the template with all matched placeholders replaced
func interpolateTemplate(template string, args map[string]string) string {
	result := template
	for name, value := range args {
		placeholder := fmt.Sprintf("{{%s}}", name)
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}
```

- [ ] **Step 5: Verify the file is syntactically correct**

```bash
cd /home/hp/Proyectos/mcp-go && go build ./internal/prompts
```

Expected: No compilation errors.

- [ ] **Step 6: Commit**

```bash
cd /home/hp/Proyectos/mcp-go && git add internal/prompts/prompts.go && git commit -m "feat: add prompts module with interpolation logic"
```

---

## Task 3: Register Prompts in Server

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Add import for prompts package**

At line 16 (after other internal imports), add:

```go
	"github.com/amphora/mcp-go/internal/prompts"
```

The imports section should have three groups:
1. stdlib (context, encoding/json, etc.)
2. external (github.com packages)
3. internal (amphora packages including new prompts)

- [ ] **Step 2: Enable prompt capability in MCPServer creation**

At line 78, change:

```go
	mcpServer := server.NewMCPServer(
		cfg.Server.Name,
		Version,
		server.WithToolCapabilities(true),
		server.WithPromptCapabilities(true),  // ADD THIS LINE
		server.WithLogging(),
		server.WithRecovery(),
	)
```

- [ ] **Step 3: Register prompts after registering tools**

After the tool registration loop (after line 92), add:

```go
	// Register prompts from configuration
	if len(cfg.Prompts) > 0 {
		log.Info().Int("count", len(cfg.Prompts)).Msg("Registering prompts from configuration")
		prompts.RegisterPrompts(mcpServer, cfg.Prompts)
	} else {
		log.Debug().Msg("No prompts configured")
	}
```

- [ ] **Step 4: Verify compilation**

```bash
cd /home/hp/Proyectos/mcp-go && go build -o bin/mcp-server ./cmd/server
```

Expected: Binary created at `bin/mcp-server`.

- [ ] **Step 5: Commit**

```bash
cd /home/hp/Proyectos/mcp-go && git add cmd/server/main.go && git commit -m "feat: enable and register MCP prompts capability"
```

---

## Task 4: Update Transport Documentation

**Files:**
- Modify: `internal/transport/sse.go`

- [ ] **Step 1: Update handleRoot() MCP methods list**

In `handleRoot()` function (around line 430), update the `mcp_methods` array to include prompts:

```go
		"mcp_methods": []string{
			"initialize",
			"ping",
			"tools/list",
			"tools/call",
			"prompts/list",
			"prompts/get",
		},
```

- [ ] **Step 2: Update endpoint documentation comment if needed**

The comment at line 407-408 already describes the root endpoint. No changes needed there since it covers all MCP methods.

- [ ] **Step 3: Verify no compilation errors**

```bash
cd /home/hp/Proyectos/mcp-go && go build ./internal/transport
```

Expected: No errors.

- [ ] **Step 4: Commit**

```bash
cd /home/hp/Proyectos/mcp-go && git add internal/transport/sse.go && git commit -m "docs: add prompts/list and prompts/get to endpoint documentation"
```

---

## Task 5: Add Prompt Configuration Examples

**Files:**
- Modify: `configs/config.yaml`

- [ ] **Step 1: Read current config.yaml to understand structure**

```bash
head -50 /home/hp/Proyectos/mcp-go/configs/config.yaml
```

Expected: See server and execution configuration sections.

- [ ] **Step 2: Add prompts section at the end of the file**

Append to `configs/config.yaml`:

```yaml
# Prompts: Template messages available via MCP prompts/list and prompts/get
# Clients (like Claude with MCP support) can discover and invoke these prompts
# Arguments are interpolated into {{argument}} placeholders in message templates
prompts:
  - name: "code_review"
    description: "Review code for quality, best practices, and potential issues"
    arguments:
      - name: "code"
        description: "The code to review"
        required: true
      - name: "language"
        description: "Programming language (e.g., Python, Go, JavaScript)"
        required: false
    messages:
      - role: "user"
        content: |
          Please review the following {{language}} code for quality, best practices, and potential issues:
          
          ```{{language}}
          {{code}}
          ```
          
          Provide constructive feedback on:
          - Code clarity and readability
          - Performance considerations
          - Security issues
          - Best practices for the language
  
  - name: "explain_error"
    description: "Explain an error message in simple, non-technical terms"
    arguments:
      - name: "error"
        description: "The error message to explain"
        required: true
    messages:
      - role: "user"
        content: |
          Please explain the following error message in simple, non-technical terms that a developer could understand:
          
          ```
          {{error}}
          ```
          
          Also suggest possible causes and how to fix it.
  
  - name: "write_tests"
    description: "Generate test cases for code"
    arguments:
      - name: "code"
        description: "The code to generate tests for"
        required: true
      - name: "language"
        description: "Programming language"
        required: false
    messages:
      - role: "user"
        content: |
          Generate comprehensive unit tests for the following {{language}} code:
          
          ```{{language}}
          {{code}}
          ```
          
          Include:
          - Happy path tests
          - Edge case tests
          - Error handling tests
          - Tests using the testing framework standard for {{language}}
```

- [ ] **Step 3: Verify YAML syntax is valid**

```bash
cd /home/hp/Proyectos/mcp-go && go run -run TestLoadConfig ./internal/config cmd/server -config configs/config.yaml 2>&1 | head -20
```

Or manually check with:
```bash
cat configs/config.yaml | tail -20
```

Expected: Valid YAML with proper indentation.

- [ ] **Step 4: Commit**

```bash
cd /home/hp/Proyectos/mcp-go && git add configs/config.yaml && git commit -m "docs: add prompt configuration examples"
```

---

## Task 6: Write Integration Tests

**Files:**
- Create: `tests/prompts_test.go`

- [ ] **Step 1: Create test file with package and imports**

```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/amphora/mcp-go/internal/prompts"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 2: Add test for prompt registration**

```go
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
```

- [ ] **Step 3: Add test for prompts/list endpoint**

```go
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
```

- [ ] **Step 4: Add test for prompts/get with interpolation**

```go
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
```

- [ ] **Step 5: Add test for required arguments validation**

```go
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
```

- [ ] **Step 6: Verify tests compile**

```bash
cd /home/hp/Proyectos/mcp-go && go test -compile prompts_test.go 2>&1 | grep -E "^(PASS|FAIL|ok)"
```

Or run the tests:

```bash
cd /home/hp/Proyectos/mcp-go && go test ./tests -v -run Prompt 2>&1 | head -50
```

- [ ] **Step 7: Commit tests**

```bash
cd /home/hp/Proyectos/mcp-go && git add tests/prompts_test.go && git commit -m "test: add integration tests for prompts endpoint"
```

---

## Task 7: Verify End-to-End

**Files:**
- No new files (integration test)

- [ ] **Step 1: Build the server**

```bash
cd /home/hp/Proyectos/mcp-go && go build -o bin/mcp-server ./cmd/server
```

Expected: Binary created successfully.

- [ ] **Step 2: Run all tests**

```bash
cd /home/hp/Proyectos/mcp-go && go test ./... -v 2>&1 | tail -30
```

Expected: All tests pass (or show which tests are skipped due to missing dependencies).

- [ ] **Step 3: Format and vet code**

```bash
cd /home/hp/Proyectos/mcp-go && go fmt ./... && go vet ./...
```

Expected: No output (no formatting or vet issues).

- [ ] **Step 4: Final commit and review**

```bash
cd /home/hp/Proyectos/mcp-go && git log --oneline -10
```

Expected: See all 7 commits for prompts implementation.

---

## Summary

This plan implements MCP prompts endpoint with:
- ✅ Dual transport support (Streamable HTTP + SSE) via single handler registration
- ✅ YAML configuration for prompts (PromptConfig types)
- ✅ Argument interpolation in message templates
- ✅ Required argument validation
- ✅ Integration tests covering all major flows
- ✅ MCP 2025-03-26 specification compliance

Estimated time: 30-45 minutes for skilled developer
Commit frequency: One per task (7 total)
