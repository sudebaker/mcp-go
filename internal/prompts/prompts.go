package prompts

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog/log"
)

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

// createPromptHandler creates a handler for a specific prompt configuration.
//
// The handler validates arguments and interpolates placeholders in message templates.
func createPromptHandler(cfg config.PromptConfig) server.PromptHandlerFunc {
	return func(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		// Convert provided arguments to string map for interpolation
		providedArgs := request.Params.Arguments
		if providedArgs == nil {
			providedArgs = make(map[string]string)
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

// interpolateTemplate replaces {{argument}} placeholders with provided values.
//
// The function uses sorted iteration over argument keys to ensure deterministic
// behavior. Placeholders are case-sensitive. Unmatched placeholders are left unchanged.
//
// Parameters:
//   - template: the template string with {{name}} placeholders
//   - args: map of argument values
//
// Returns:
//
//	the template with all matched placeholders replaced
func interpolateTemplate(template string, args map[string]string) string {
	result := template

	// Sort keys to ensure deterministic iteration order
	// (maps in Go have random iteration order which can affect results)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Replace placeholders in sorted order
	for _, name := range keys {
		value := args[name]
		placeholder := fmt.Sprintf("{{%s}}", name)
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}
