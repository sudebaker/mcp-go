package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/executor"
	"github.com/sudebaker/mcp-go/internal/health"
	"github.com/sudebaker/mcp-go/internal/prompts"
	"github.com/sudebaker/mcp-go/internal/session"
	"github.com/sudebaker/mcp-go/internal/tracing"
	"github.com/sudebaker/mcp-go/internal/transport"
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

	// Build health checker with dependency detection
	deps := health.BuildDependencies(cfg)

	// Open PostgreSQL connection from DATABASE_URL if configured
	var db *sql.DB
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL != "" {
		// Ensure sslmode=disable for internal Docker connections
		if !containsParam(databaseURL, "sslmode") {
			separator := "?"
			if containsChar(databaseURL, '?') {
				separator = "&"
			}
			databaseURL = databaseURL + separator + "sslmode=disable"
		}
		var dbErr error
		db, dbErr = sql.Open("postgres", databaseURL)
		if dbErr != nil {
			log.Error().Err(dbErr).Msg("Failed to open PostgreSQL connection for health checks")
			db = nil
		} else {
			db.SetMaxOpenConns(2) // Health checks only need minimal connections
			db.SetMaxIdleConns(1)
			log.Info().Msg("PostgreSQL health check connection initialized")
		}
	}

	// Filter out redis from deps if REDIS_URL is not set
	if os.Getenv("REDIS_URL") == "" {
		filtered := make([]health.DependencyCheck, 0, len(deps))
		for _, d := range deps {
			if d.Name != "redis" {
				filtered = append(filtered, d)
			}
		}
		deps = filtered
	}

	healthChecker := health.NewChecker(cfg, nil, db, deps)
	log.Info().Int("dependencies", len(deps)).Msg("Health checker initialized")

	log.Info().
		Str("server_name", cfg.Server.Name).
		Int("port", cfg.Server.Port).
		Int("tools_count", len(cfg.Tools)).
		Msg("Configuration loaded")

	// Initialize tracing
	tracer := tracing.NewTracer(cfg.Server.Name)
	log.Debug().Msg("Distributed tracing initialized")

	// Initialize session store for user_id tracking
	sessionStore := session.New()

	// Initialize hooks for session management
	hooks := &server.Hooks{}
	hooks.AddOnRegisterSession(func(ctx context.Context, sess server.ClientSession) {
		log.Debug().Str("session_id", sess.SessionID()).Msg("Session registered")
	})
	hooks.AddOnUnregisterSession(func(ctx context.Context, sess server.ClientSession) {
		sessionStore.Delete(sess.SessionID())
		log.Debug().Str("session_id", sess.SessionID()).Msg("Session unregistered, user_id removed")
	})
	hooks.AddAfterInitialize(func(ctx context.Context, id any, message *mcp.InitializeRequest, result *mcp.InitializeResult) {
		if message == nil || message.Params.Capabilities.Experimental == nil {
			return
		}
		if userID, ok := message.Params.Capabilities.Experimental["user_id"].(string); ok {
			if sess := server.ClientSessionFromContext(ctx); sess != nil {
				sessionStore.Set(sess.SessionID(), userID)
				log.Info().Str("session_id", sess.SessionID()).Str("user_id", userID).Msg("Session associated with user")
			}
		}
	})

	// Create executor with session store for user_id injection
	// and persistent process pool for KB tools (kb_ingest, kb_search).
	// Pool keeps 5 persistent Python processes per tool, avoiding model reloads.
	exec := executor.NewWithTracerSessionStoreAndPool(cfg, tracer, sessionStore, 5)

	// Create MCP server
	mcpServer := server.NewMCPServer(
		cfg.Server.Name,
		Version,
		server.WithToolCapabilities(true),
		server.WithPromptCapabilities(true),
		server.WithLogging(),
		server.WithRecovery(),
		server.WithHooks(hooks),
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

	log.Info().Msg("Server started with static configuration")

	// Create SSE server
	sseServer := transport.NewMCPServer(mcpServer, transport.MCPConfig{
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
		Upload:            cfg.Upload,
		FilesDir:          filepath.Join(cfg.Execution.WorkingDir, cfg.Execution.ReportsDir),
		HealthChecker:     healthChecker,
	})

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

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

	exec.Close()

	log.Info().Msg("Server stopped")
}

// truncateClientError caps error message length sent to clients.
// Full error details (including Details field) are always logged server-side
// to prevent leaking internal information.
func truncateClientError(msg string) string {
	if len(msg) > 500 {
		msg = msg[:500] + "... (truncated)"
	}
	return msg
}

// registerTool registers a tool with the MCP server.
func registerTool(mcpServer *server.MCPServer, exec *executor.Executor, toolCfg config.ToolConfig) {
	// Build input schema for the tool
	inputSchema := buildInputSchema(toolCfg)

	toolOpts := []mcp.ToolOption{
		mcp.WithDescription(toolCfg.Description),
	}

	// Apply tool annotations if defined in config
	if toolCfg.ReadOnlyHint != nil {
		toolOpts = append(toolOpts, mcp.WithReadOnlyHintAnnotation(*toolCfg.ReadOnlyHint))
	}
	if toolCfg.DestructiveHint != nil {
		toolOpts = append(toolOpts, mcp.WithDestructiveHintAnnotation(*toolCfg.DestructiveHint))
	}
	if toolCfg.IdempotentHint != nil {
		toolOpts = append(toolOpts, mcp.WithIdempotentHintAnnotation(*toolCfg.IdempotentHint))
	}
	if toolCfg.OpenWorldHint != nil {
		toolOpts = append(toolOpts, mcp.WithOpenWorldHintAnnotation(*toolCfg.OpenWorldHint))
	}

	tool := mcp.NewTool(toolCfg.Name, toolOpts...)

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
			log.Error().Err(err).Str("tool", toolName).Msg("Tool execution failed")
			return mcp.NewToolResultError("Tool execution failed. Check server logs for details."), nil
		}

		// Handle execution error from subprocess (sanitized)
		if !result.Success {
			if result.Error != nil {
				log.Error().
					Str("tool", toolName).
					Str("error_code", result.Error.Code).
					Str("error_message", result.Error.Message).
					Str("error_details", result.Error.Details).
					Msg("Tool returned error")
				return mcp.NewToolResultError(truncateClientError(result.Error.Message)), nil
			}
			return mcp.NewToolResultError("Tool execution failed with no error details"), nil
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
						log.Debug().
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

// containsParam checks if a URL string already contains a query parameter.
func containsParam(rawURL, param string) bool {
	return strings.Contains(rawURL, param+"=")
}

// containsChar checks if a string contains a specific character.
func containsChar(s string, c byte) bool {
	return strings.IndexByte(s, c) >= 0
}
