// Package transport provides HTTP transport layer implementations for the MCP server.
//
// This package implements the server-side HTTP handlers for the MCP protocol,
// supporting both the legacy SSE (Server-Sent Events) transport and the modern
// Streamable HTTP specification. It wraps the mcp-go library server and adds
// middleware for CORS, rate limiting, request logging, and distributed tracing.
//
// # Supported Transports
//
//   - Streamable HTTP (2025 spec): POST /mcp - Modern bidirectional transport
//   - SSE (2024 spec): GET /sse, POST /message - Legacy unidirectional transport
//
// # Middleware Chain
//
// The middleware is applied in this order for each request:
//
//	Client Request
//	    ↓
//	CORS Middleware (origin validation)
//	    ↓
//	Rate Limiter (requests per second)
//	    ↓
//	Tracing Middleware (span creation)
//	    ↓
//	Logging Middleware (request/response logging)
//	    ↓
//	MCPServer Handler
//
// # Endpoint Summary
//
//	/           - Server info (GET)
//	/health     - Basic health check (GET)
//	/health/detailed - Component health status (GET)
//	/metrics    - Prometheus metrics (GET)
//	/openapi.json - OpenAPI spec (GET)
//	/mcp        - MCP Streamable HTTP endpoint (POST)
//	/sse        - MCP SSE endpoint (GET)
//	/message    - MCP SSE message endpoint (POST)
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/tracing"
)

// MCPServer wraps the mcp-go Streamable HTTP server with additional functionality.
// It provides HTTP serving, middleware chaining, and management endpoints.
type MCPServer struct {
	mcpServer      *server.MCPServer            // Core MCP server implementation
	streamServer   *server.StreamableHTTPServer // Streamable HTTP handler
	sseServer      *server.SSEServer            // Legacy SSE server
	httpServer     *http.Server                 // Go HTTP server instance
	addr           string                       // Listen address (host:port)
	serverName     string                       // Service name for logging/health
	version        string                       // Semantic version
	tools          []config.ToolConfig          // Tool configurations for docs
	rateLimiter    *RateLimiter                 // Rate limiting middleware (nil if disabled)
	tracer         *tracing.Tracer              // Distributed tracing
	allowedOrigins []string                     // CORS allowed origins (empty = all)
}

// MCPConfig holds configuration for creating a new MCPServer.
type MCPConfig struct {
	// Host is the network address to bind (default: "0.0.0.0")
	Host string
	// Port is the TCP port to listen on (default: 8080)
	Port int
	// BaseURL is the public-facing URL for SSE endpoint resolution
	BaseURL string
	// KeepAliveInterval is the SSE keep-alive interval (default: 30s)
	KeepAliveInterval time.Duration
	// ServerName is the service name for health checks and logging
	ServerName string
	// Version is the semantic version string
	Version string
	// Tools is the list of tool configurations for documentation
	Tools []config.ToolConfig
	// RateLimitRPS is requests per second limit (0 = disabled)
	RateLimitRPS float64
	// RateLimitBurst is the maximum burst for rate limiting
	RateLimitBurst int
	// AllowedOrigins is the CORS origin whitelist (nil/empty = all)
	AllowedOrigins []string
	// Tracer is the distributed tracing instance (nil = no-op)
	Tracer *tracing.Tracer
}

// NewMCPServer creates a new MCP server with configured transports and middleware.
//
// This constructor initializes both Streamable HTTP (modern) and SSE (legacy)
// transports, along with optional rate limiting and CORS middleware.
//
// The server uses WithUseFullURLForMessageEndpoint(false) for SSE, which makes
// clients interpret message endpoints relative to their connection origin. This
// supports multi-network deployments (e.g., localhost development vs host.docker.internal).
//
// Parameters:
//   - mcpServer: the underlying mcp-go server instance (from mcp.NewServer())
//   - cfg: the server configuration
//
// Returns:
//
//	a configured MCPServer ready to start with Start()
//
// Example:
//
//	cfg := transport.MCPConfig{
//	    Host: "0.0.0.0",
//	    Port: 8080,
//	    ServerName: "mcp-orchestrator",
//	    Version: "1.0.0",
//	    RateLimitRPS: 10,
//	    RateLimitBurst: 20,
//	}
//	mcpServer := server.NewMCPServer(transport.NewMCPServer(mcpServer, cfg))
func NewMCPServer(mcpServer *server.MCPServer, cfg MCPConfig) *MCPServer {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	streamServer := server.NewStreamableHTTPServer(mcpServer)

	// Create SSE server for legacy MCP 2024 spec
	// WithUseFullURLForMessageEndpoint(false) makes clients interpret the message
	// endpoint relative to their connection origin. This supports multi-network deployments
	// (e.g., localhost vs host.docker.internal). BaseURL is not needed with this mode.
	sseServer := server.NewSSEServer(
		mcpServer,
		server.WithKeepAlive(true),
		server.WithKeepAliveInterval(cfg.KeepAliveInterval),
		server.WithUseFullURLForMessageEndpoint(false),
	)

	var rateLimiter *RateLimiter
	if cfg.RateLimitRPS > 0 {
		rateLimiter = NewRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst)
	}

	tracer := cfg.Tracer
	if tracer == nil {
		tracer = tracing.NoOpTracer()
	}

	return &MCPServer{
		mcpServer:      mcpServer,
		streamServer:   streamServer,
		sseServer:      sseServer,
		addr:           addr,
		serverName:     cfg.ServerName,
		version:        cfg.Version,
		tools:          cfg.Tools,
		rateLimiter:    rateLimiter,
		tracer:         tracer,
		allowedOrigins: cfg.AllowedOrigins,
	}
}

// Start begins serving the MCP server and blocks until shutdown.
//
// This method:
//   - Creates an HTTP mux with all endpoints registered
//   - Applies the middleware chain (CORS → Rate Limit → Tracing → Logging)
//   - Starts the Go HTTP server with sensible timeouts
//   - Returns when the server exits (error or shutdown signal)
//
// The server handles graceful shutdown via Shutdown(ctx).
//
// Returns:
//
//	error: from ListenAndServe (after graceful shutdown, usually nil)
func (s *MCPServer) Start() error {
	log.Info().
		Str("addr", s.addr).
		Msg("Starting MCP server (Streamable HTTP + SSE)")

	// Create custom mux with additional endpoints
	mux := http.NewServeMux()

	// Health endpoint (no rate limiting for health checks)
	mux.HandleFunc("/health", s.handleHealth)

	// Detailed health endpoint
	mux.HandleFunc("/health/detailed", s.handleHealthDetailed)

	// Prometheus metrics endpoint
	mux.Handle("/metrics", promhttp.Handler())

	// OpenAPI spec and Docs
	s.setupDocsEndpoints(mux)

	// Info endpoint
	mux.HandleFunc("/", s.handleRoot)

	// Prepare middleware chain: CORS -> Rate Limiter -> Handler
	var streamHandler http.Handler = s.streamServer
	if s.rateLimiter != nil {
		streamHandler = s.rateLimiter.Middleware(streamHandler)
	}
	streamHandler = CORSMiddleware(s.allowedOrigins)(streamHandler)

	// Prepare SSE handlers with same middleware chain
	// Cache handlers to avoid allocating new function values per request
	sseServerHandler := s.sseServer.SSEHandler()
	messageServerHandler := s.sseServer.MessageHandler()

	sseHandler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseServerHandler.ServeHTTP(w, r)
	}))
	if s.rateLimiter != nil {
		sseHandler = s.rateLimiter.Middleware(sseHandler)
	}
	sseHandler = CORSMiddleware(s.allowedOrigins)(sseHandler)

	messageHandler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		messageServerHandler.ServeHTTP(w, r)
	}))
	if s.rateLimiter != nil {
		messageHandler = s.rateLimiter.Middleware(messageHandler)
	}
	messageHandler = CORSMiddleware(s.allowedOrigins)(messageHandler)

	// Register handlers
	// MCP Streamable HTTP endpoint (2025 spec)
	mux.Handle("/mcp", streamHandler)

	// SSE endpoints (legacy 2024 spec)
	mux.Handle("/sse", sseHandler)
	mux.Handle("/message", messageHandler)

	// Log rate limiting status
	if s.rateLimiter != nil {
		log.Info().
			Float64("rps", s.rateLimiter.rps).
			Int("burst", s.rateLimiter.burst).
			Msg("Rate limiting enabled for /mcp, /sse, /message")
	}

	// Log CORS status
	if len(s.allowedOrigins) == 0 {
		log.Info().Msg("CORS configured in permissive mode (allow all origins)")
	} else {
		log.Info().
			Strs("allowed_origins", s.allowedOrigins).
			Msg("CORS configured with restricted origin list")
	}

	// Log transport activation
	log.Info().Msg("SSE transport active on /sse (GET) and /message (POST)")

	// Wrap entire mux with tracing and logging middleware
	var handler http.Handler = mux
	handler = TracingMiddleware(s.tracer, handler)
	handler = LoggingMiddleware(handler)

	s.httpServer = &http.Server{
		Addr:           s.addr,
		Handler:        handler,
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   0, // No write timeout for SSE long-lived connections
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
//
// It stops accepting new connections, waits for in-flight requests to complete
// (up to the context deadline), and stops the rate limiter cleanup goroutine.
//
// Parameters:
//   - ctx: context with deadline for the shutdown operation
//
// Returns:
//
//	error: if shutdown times out or fails
func (s *MCPServer) Shutdown(ctx context.Context) error {
	log.Info().Msg("Shutting down MCP server")

	// Stop rate limiter cleanup goroutine to prevent memory leak
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}

	// httpServer is always initialized in Start()
	if s.httpServer == nil {
		log.Warn().Msg("HTTP server not initialized")
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the underlying HTTP handler for the MCP server.
//
// This is useful for embedding the MCP server in another HTTP server or
// for testing purposes.
//
// Returns:
//
//	http.Handler: the Streamable HTTP handler
func (s *MCPServer) Handler() http.Handler {
	return s.streamServer
}

// handleHealth returns basic server health status.
//
// This endpoint is intended for load balancers and orchestrators (Kubernetes,
// Docker Compose health checks). It does not perform deep health checks.
//
// Response format:
//
//	{
//	  "status": "healthy",
//	  "service": "mcp-orchestrator",
//	  "version": "1.0.0",
//	  "protocol": "mcp",
//	  "transport": "streamable-http + sse",
//	  "endpoints": {...}
//	}
func (s *MCPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "healthy",
		"service":   s.serverName,
		"version":   s.version,
		"protocol":  "mcp",
		"transport": "streamable-http + sse",
		"endpoints": map[string]string{
			"mcp":             "/mcp",
			"sse":             "/sse",
			"message":         "/message",
			"health":          "/health",
			"detailed_health": "/health/detailed",
			"metrics":         "/metrics",
		},
	})
}

// handleHealthDetailed returns comprehensive health status of all components.
//
// Unlike handleHealth, this endpoint provides detailed information about
// individual server components and their operational status.
//
// Response format:
//
//	{
//	  "status": "healthy",
//	  "timestamp": "2024-01-15T10:30:00Z",
//	  "service": "mcp-orchestrator",
//	  "version": "1.0.0",
//	  "components": {
//	    "server": {"status": "healthy", ...},
//	    "http": {"status": "operational", ...},
//	    "rate_limiter": {"status": "operational", ...}
//	  }
//	}
func (s *MCPServer) handleHealthDetailed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Basic component health status
	components := map[string]interface{}{
		"server": map[string]interface{}{
			"status":  "healthy",
			"name":    s.serverName,
			"version": s.version,
		},
		"http": map[string]interface{}{
			"status":    "operational",
			"endpoints": []string{"/mcp", "/sse", "/message", "/health", "/health/detailed", "/metrics"},
		},
		"rate_limiter": map[string]interface{}{
			"status":  "operational",
			"enabled": s.rateLimiter != nil,
		},
	}

	response := map[string]interface{}{
		"status":     "healthy",
		"timestamp":  time.Now().Format(time.RFC3339),
		"service":    s.serverName,
		"version":    s.version,
		"components": components,
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// handleRoot returns server information and available endpoints.
//
// This is the root endpoint (GET /) providing an overview of the server,
// supported protocols, and available MCP methods.
//
// Response includes:
//   - Server name and version
//   - Protocol description
//   - Transport information
//   - Available endpoints
//   - Supported MCP methods (initialize, ping, tools/list, tools/call)
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
		"transport":   "Streamable HTTP (2025) + SSE (2024)",
		"description": "MCP server that orchestrates Python tools via subprocess execution",
		"endpoints": map[string]string{
			"GET /":             "This info page",
			"GET /health":       "Health check endpoint",
			"POST /mcp":         "MCP Streamable HTTP endpoint (2025 spec)",
			"GET /sse":          "MCP SSE endpoint (2024 spec)",
			"POST /message":     "MCP SSE message endpoint (2024 spec)",
			"GET /openapi.json": "API documentation",
		},
		"mcp_methods": []string{
			"initialize",
			"ping",
			"tools/list",
			"tools/call",
			"prompts/list",
			"prompts/get",
		},
	})
}

// handleOpenAPI returns OpenAPI-like documentation.
//
// This is a convenience alias for handleOpenAPISpec to maintain API
// compatibility.
func (s *MCPServer) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	s.handleOpenAPISpec(w, r)
}

// Legacy aliases for backwards compatibility with existing code.
type SSEServer = MCPServer
type SSEConfig = MCPConfig

// NewSSEServer creates an SSEServer (alias for NewMCPServer).
//
// Deprecated: Use NewMCPServer directly.
func NewSSEServer(mcpServer *server.MCPServer, cfg SSEConfig) *SSEServer {
	return NewMCPServer(mcpServer, cfg)
}
