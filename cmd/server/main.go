package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/amphora/mcp-go/internal/executor"
	"github.com/amphora/mcp-go/internal/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	Version = "0.1.0"
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

	log.Info().
		Str("server_name", cfg.Server.Name).
		Int("port", cfg.Server.Port).
		Int("tools_count", len(cfg.Tools)).
		Msg("Configuration loaded")

	// Create executor
	exec := executor.New(cfg)

	// Create MCP server
	mcpServer := server.NewMCPServer(
		cfg.Server.Name,
		Version,
		server.WithToolCapabilities(true),
		server.WithLogging(),
		server.WithRecovery(),
	)

	// Register tools from configuration
	for _, toolCfg := range cfg.Tools {
		registerTool(mcpServer, exec, toolCfg)
	}

	// Setup configuration hot-reload
	if err := config.Watch(*configPath, func(newCfg *config.Config) {
		log.Info().Msg("Reloading configuration")
		exec.UpdateConfig(newCfg)
		// Re-register tools with new configuration
		for _, toolCfg := range newCfg.Tools {
			registerTool(mcpServer, exec, toolCfg)
		}
	}); err != nil {
		log.Warn().Err(err).Msg("Failed to setup config watcher")
	}

	// Create SSE server
	sseServer := transport.NewSSEServer(mcpServer, transport.SSEConfig{
		Host:              cfg.Server.Host,
		Port:              cfg.Server.Port,
		KeepAliveInterval: 30 * time.Second,
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

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

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
	}


	if required, ok := toolCfg.InputSchema["required"].([]interface{}); ok {
		for _, r := range required {
			if s, ok := r.(string); ok {
				schema.Required = append(schema.Required, s)
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

		// Execute via subprocess
		result, err := exec.Execute(ctx, toolName, request.Params.Arguments)
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
