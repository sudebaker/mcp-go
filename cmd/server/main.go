package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/amphora/mcp-go/internal/executor"
	"github.com/amphora/mcp-go/internal/prompts"
	"github.com/amphora/mcp-go/internal/tracing"
	"github.com/amphora/mcp-go/internal/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	Version       = "0.1.0"
	maxArgsSize   = 1 << 20 // 1MB max argument size
	maxArgsSizeMB = 1
)

// main function initializes and runs the MCP Orchestrator server.
func main() {
	// Parse command line flags
	configPath := flag.String("config", "configs/config.yaml", "Path to configuration file")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	// Setup logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	log.Info().
		Str("version", Version).
		Str("config", *configPath).
		Msg("Starting MCP Orchestrator")

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Validate config
	if err := config.Validate(cfg); err != nil {
		log.Fatal().Err(err).Msg("Configuration validation failed")
	}

	log.Info().
		Str("server_name", cfg.Server.Name).
		Int("port", cfg.Server.Port).
		Int("tools_count", len(cfg.Tools)).
		Msg("Configuration loaded")

	// Initialize tracing
	tracer := tracing.NewTracer(cfg.Server.Name)
	log.Debug().Msg("Distributed tracing initialized")

	// Create executor
	exec := executor.NewWithTracer(cfg, tracer)

	// Create MCP server
	mcpServer := server.NewMCPServer(
		cfg.Server.Name,
		Version,
		server.WithToolCapabilities(true),
		server.WithPromptCapabilities(true),
		server.WithLogging(),
		server.WithRecovery(),
	)

	// Validate and register tools from configuration
	for _, toolCfg := range cfg.Tools {
		if err := executor.ValidateToolConfig(&toolCfg); err != nil {
			log.Fatal().
				Err(err).
				Str("tool", toolCfg.Name).
				Msg("Invalid tool configuration")
		}
		registerTool(mcpServer, exec, toolCfg)
	}

	// Register prompts from configuration
	if len(cfg.Prompts) > 0 {
		log.Info().Int("count", len(cfg.Prompts)).Msg("Registering prompts from configuration")
		prompts.RegisterPrompts(mcpServer, cfg.Prompts)
	} else {
		log.Debug().Msg("No prompts configured")
	}

	log.Info().Msg("Configuration changes require server restart")

	// Create SSE server
	sseServer := transport.NewSSEServer(mcpServer, transport.SSEConfig{
		Host:              cfg.Server.Host,
		Port:              cfg.Server.Port,
		BaseURL:           cfg.Server.BaseURL,
		KeepAliveInterval: 30 * time.Second,
		ServerName:        cfg.Server.Name,
		Version:           Version,
		Tools:             cfg.Tools,
		RateLimitRPS:      cfg.Server.RateLimitRPS,
		RateLimitBurst:    cfg.Server.RateLimitBurst,
		AllowedOrigins:    cfg.Server.AllowedOrigins,
		Tracer:            tracer,
	})

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start server in goroutine
	go func() {
		if err := sseServer.Start(); err != nil {
			log.Error().Err(err).Msg("Server error")
			cancel()
		}
	}()

	// Wait for shutdown signal
	select {
	case sig := <-sigChan:
		log.Info().Str("signal", sig.String()).Msg("Received shutdown signal")
	case <-ctx.Done():
	}

	// Graceful shutdown with configurable timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()

	log.Info().
		Dur("timeout", cfg.Server.ShutdownTimeout).
		Msg("Shutting down server")

	if err := sseServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("Error during shutdown")
	}

	log.Info().Msg("Server stopped")
}

// registerTool registers a tool with the MCP server.
func registerTool(mcpServer *server.MCPServer, exec *executor.Executor, toolCfg config.ToolConfig) {
	// Build input schema for the tool
	inputSchema := buildInputSchema(toolCfg)

	tool := mcp.NewTool(
		toolCfg.Name,
		mcp.WithDescription(toolCfg.Description),
		mcp.WithString(
			"__raw_arguments",
			mcp.Description("Raw arguments as JSON (internal use)"),
		),
	)

	// Apply input schema properties if defined
	if inputSchema != nil {
		tool.InputSchema = *inputSchema
	}

	// Create handler that delegates to executor
	handler := createToolHandler(exec, toolCfg.Name)

	mcpServer.AddTool(tool, handler)

	log.Debug().
		Str("tool", toolCfg.Name).
		Str("command", toolCfg.Command).
		Msg("Registered tool")
}

// buildInputSchema converts config input schema to MCP input schema.
func buildInputSchema(toolCfg config.ToolConfig) *mcp.ToolInputSchema {
	if toolCfg.InputSchema == nil {
		return nil
	}

	schema := &mcp.ToolInputSchema{
		Type:       "object",
		Properties: make(map[string]interface{}),
	}

	if props, ok := toolCfg.InputSchema["properties"].(map[string]interface{}); ok {
		schema.Properties = props
	} else {
		log.Warn().
			Str("tool", toolCfg.Name).
			Msg("InputSchema 'properties' field is not a map or is missing")
	}

	if required, ok := toolCfg.InputSchema["required"].([]interface{}); ok {
		for _, r := range required {
			if s, ok := r.(string); ok {
				schema.Required = append(schema.Required, s)
			} else {
				log.Warn().
					Str("tool", toolCfg.Name).
					Interface("value", r).
					Msg("InputSchema 'required' contains non-string value")
			}
		}
	}

	return schema
}

// createToolHandler creates a tool handler that delegates to the executor.
func createToolHandler(exec *executor.Executor, toolName string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		log.Debug().
			Str("tool", toolName).
			Interface("arguments", request.Params.Arguments).
			Msg("Executing tool")

		// Convert arguments to map[string]interface{}
		args, ok := request.Params.Arguments.(map[string]interface{})
		if !ok {
			return mcp.NewToolResultError("Invalid arguments format"), nil
		}

		// Validate argument size to prevent DoS
		argsJSON, err := json.Marshal(args)
		if err != nil {
			return mcp.NewToolResultError("Failed to serialize arguments"), nil
		}
		if len(argsJSON) > maxArgsSize {
			log.Warn().
				Str("tool", toolName).
				Int("size", len(argsJSON)).
				Int("max_size", maxArgsSize).
				Msg("Arguments exceed maximum size")
			return mcp.NewToolResultError(fmt.Sprintf("Arguments exceed maximum size of %dMB", maxArgsSizeMB)), nil
		}

		// Execute via subprocess
		result, err := exec.Execute(ctx, toolName, args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Handle execution error from subprocess
		if !result.Success && result.Error != nil {
			errorMsg := result.Error.Message
			if result.Error.Details != "" {
				errorMsg += "\n" + result.Error.Details
			}
			return mcp.NewToolResultError(errorMsg), nil
		}

		// Convert content items to MCP content
		if len(result.Content) > 0 {
			contents := make([]mcp.Content, 0, len(result.Content))
			for _, item := range result.Content {
				switch item.Type {
				case "text":
					contents = append(contents, mcp.TextContent{
						Type: "text",
						Text: item.Text,
					})
				case "image":
					contents = append(contents, mcp.ImageContent{
						Type:     "image",
						Data:     item.Data,
						MIMEType: item.MIMEType,
					})
				case "resource":
					if item.Resource != nil {
						// Create embedded resource content for MCP
						// Use TextResourceContents since we're sending base64 text
						resourceContent := mcp.EmbeddedResource{
							Type: "resource",
							Resource: mcp.TextResourceContents{
								URI:      item.Resource.URI,
								MIMEType: item.Resource.MIMEType,
								Text:     item.Resource.Text,
							},
						}
						contents = append(contents, resourceContent)
						log.Info().
							Str("tool", toolName).
							Str("uri", item.Resource.URI).
							Str("mime_type", item.Resource.MIMEType).
							Int("text_length", len(item.Resource.Text)).
							Msg("Returning resource content")
					} else {
						log.Warn().
							Str("tool", toolName).
							Msg("Resource type with nil resource field, skipping")
					}
				default:
					log.Warn().
						Str("tool", toolName).
						Str("content_type", item.Type).
						Msg("Unknown content type, skipping")
				}
			}
			return &mcp.CallToolResult{
				Content: contents,
			}, nil
		}

		// Default to empty text result
		return mcp.NewToolResultText(""), nil
	}
}
