package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog/log"
)

// MCPServer wraps the mcp-go Streamable HTTP server with additional functionality
type MCPServer struct {
	mcpServer    *server.MCPServer
	streamServer *server.StreamableHTTPServer
	httpServer   *http.Server
	addr         string
	serverName   string
	version      string
	tools        []config.ToolConfig
	rateLimiter  *RateLimiter
}

// MCPConfig holds MCP server configuration
type MCPConfig struct {
	Host              string
	Port              int
	KeepAliveInterval time.Duration
	ServerName        string
	Version           string
	Tools             []config.ToolConfig
	RateLimitRPS      float64
	RateLimitBurst    int
}

// NewMCPServer creates a new MCP server with Streamable HTTP transport
func NewMCPServer(mcpServer *server.MCPServer, cfg MCPConfig) *MCPServer {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	streamServer := server.NewStreamableHTTPServer(mcpServer)

	var rateLimiter *RateLimiter
	if cfg.RateLimitRPS > 0 {
		rateLimiter = NewRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst)
	}

	return &MCPServer{
		mcpServer:    mcpServer,
		streamServer: streamServer,
		addr:         addr,
		serverName:   cfg.ServerName,
		version:      cfg.Version,
		tools:        cfg.Tools,
		rateLimiter:  rateLimiter,
	}
}

// Start begins serving the MCP server with custom endpoints
func (s *MCPServer) Start() error {
	log.Info().
		Str("addr", s.addr).
		Msg("Starting MCP server (Streamable HTTP)")

	// Create custom mux with additional endpoints
	mux := http.NewServeMux()

	// Health endpoint (no rate limiting for health checks)
	mux.HandleFunc("/health", s.handleHealth)

	// OpenAPI spec (for compatibility info)
	mux.HandleFunc("/openapi.json", s.handleOpenAPI)

	// Info endpoint
	mux.HandleFunc("/", s.handleRoot)

	// Apply rate limiter middleware to MCP endpoint if configured
	var mcpHandler http.Handler
	if s.rateLimiter != nil {
		mcpHandler = s.rateLimiter.Middleware(s.streamServer)
		log.Info().
			Float64("rps", s.rateLimiter.rps).
			Int("burst", s.rateLimiter.burst).
			Msg("Rate limiting enabled")
	} else {
		mcpHandler = s.streamServer
	}

	// MCP Streamable HTTP endpoint (default: /mcp)
	mux.Handle("/mcp", mcpHandler)

	s.httpServer = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *MCPServer) Shutdown(ctx context.Context) error {
	log.Info().Msg("Shutting down MCP server")
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return s.streamServer.Shutdown(ctx)
}

// Handler returns the HTTP handler for the MCP server
func (s *MCPServer) Handler() http.Handler {
	return s.streamServer
}

// handleHealth returns server health status
func (s *MCPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "healthy",
		"service":   s.serverName,
		"version":   s.version,
		"protocol":  "mcp",
		"transport": "streamable-http",
		"endpoints": map[string]string{
			"mcp":    "/mcp",
			"health": "/health",
		},
	})
}

// handleRoot returns server info
func (s *MCPServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"name":        s.serverName,
		"version":     s.version,
		"protocol":    "MCP (Model Context Protocol)",
		"transport":   "Streamable HTTP",
		"description": "MCP server that orchestrates Python tools via subprocess execution",
		"endpoints": map[string]string{
			"GET /":             "This info page",
			"GET /health":       "Health check endpoint",
			"POST /mcp":         "MCP Streamable HTTP endpoint",
			"GET /openapi.json": "API documentation",
		},
		"mcp_methods": []string{
			"initialize",
			"ping",
			"tools/list",
			"tools/call",
		},
	})
}

// handleOpenAPI returns OpenAPI-like documentation
func (s *MCPServer) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	// Build tools list for documentation
	tools := make([]map[string]interface{}, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, map[string]interface{}{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}

	spec := map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":       s.serverName,
			"version":     s.version,
			"description": "MCP (Model Context Protocol) Server with Streamable HTTP transport",
		},
		"servers": []map[string]string{
			{"url": "/", "description": "MCP Server"},
		},
		"paths": map[string]interface{}{
			"/health": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Health check",
					"description": "Returns server health status",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Server is healthy",
						},
					},
				},
			},
			"/mcp": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "MCP Streamable HTTP",
					"description": "Send MCP JSON-RPC messages. Session ID returned via Mcp-Session-Id header.",
					"requestBody": map[string]interface{}{
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"jsonrpc": map[string]string{"type": "string", "example": "2.0"},
										"id":      map[string]string{"type": "integer"},
										"method":  map[string]string{"type": "string"},
										"params":  map[string]string{"type": "object"},
									},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "JSON-RPC response or SSE stream",
							"headers": map[string]interface{}{
								"Mcp-Session-Id": map[string]interface{}{
									"description": "Session ID for subsequent requests",
									"schema":      map[string]string{"type": "string"},
								},
							},
						},
					},
				},
				"get": map[string]interface{}{
					"summary":     "MCP SSE Stream",
					"description": "Establish SSE stream for server-initiated messages (requires Mcp-Session-Id header)",
				},
			},
		},
		"x-mcp-info": map[string]interface{}{
			"protocol":        "MCP (Model Context Protocol)",
			"transport":       "Streamable HTTP (spec 2025-03-26)",
			"specification":   "https://modelcontextprotocol.io/specification/2025-03-26/basic/transports",
			"available_tools": tools,
			"usage": map[string]interface{}{
				"step1": "POST /mcp with initialize request, receive Mcp-Session-Id header",
				"step2": "Include Mcp-Session-Id header in subsequent requests",
				"step3": "POST /mcp with tools/list to discover available tools",
				"step4": "POST /mcp with tools/call to execute tools",
			},
			"example_initialize": map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "initialize",
				"params": map[string]interface{}{
					"protocolVersion": "2025-03-26",
					"capabilities":    map[string]interface{}{},
					"clientInfo": map[string]string{
						"name":    "my-client",
						"version": "1.0",
					},
				},
			},
			"example_tools_call": map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      2,
				"method":  "tools/call",
				"params": map[string]interface{}{
					"name": "echo",
					"arguments": map[string]string{
						"text": "Hello!",
					},
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(spec)
}

// Legacy aliases for backwards compatibility
type SSEServer = MCPServer
type SSEConfig = MCPConfig

func NewSSEServer(mcpServer *server.MCPServer, cfg SSEConfig) *SSEServer {
	return NewMCPServer(mcpServer, cfg)
}
