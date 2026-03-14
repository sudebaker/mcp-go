package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/amphora/mcp-go/internal/tracing"
	"github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

// MCPServer wraps the mcp-go Streamable HTTP server with additional functionality
type MCPServer struct {
	mcpServer      *server.MCPServer
	streamServer   *server.StreamableHTTPServer
	sseServer      *server.SSEServer
	httpServer     *http.Server
	addr           string
	serverName     string
	version        string
	tools          []config.ToolConfig
	rateLimiter    *RateLimiter
	tracer         *tracing.Tracer
	allowedOrigins []string
}

// MCPConfig holds MCP server configuration
type MCPConfig struct {
	Host              string
	Port              int
	BaseURL           string
	KeepAliveInterval time.Duration
	ServerName        string
	Version           string
	Tools             []config.ToolConfig
	RateLimitRPS      float64
	RateLimitBurst    int
	AllowedOrigins    []string
	Tracer            *tracing.Tracer
}

// NewMCPServer creates a new MCP server with Streamable HTTP transport
func NewMCPServer(mcpServer *server.MCPServer, cfg MCPConfig) *MCPServer {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	streamServer := server.NewStreamableHTTPServer(mcpServer)

	// Create SSE server for legacy MCP 2024 spec
	sseServer := server.NewSSEServer(
		mcpServer,
		server.WithBaseURL(cfg.BaseURL),
		server.WithKeepAlive(true),
		server.WithKeepAliveInterval(cfg.KeepAliveInterval),
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

// Start begins serving the MCP server with custom endpoints
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
	sseHandler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.sseServer.SSEHandler().ServeHTTP(w, r)
	}))
	if s.rateLimiter != nil {
		sseHandler = s.rateLimiter.Middleware(sseHandler)
	}
	sseHandler = CORSMiddleware(s.allowedOrigins)(sseHandler)

	messageHandler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.sseServer.MessageHandler().ServeHTTP(w, r)
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
		WriteTimeout:   15 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *MCPServer) Shutdown(ctx context.Context) error {
	log.Info().Msg("Shutting down MCP server")

	// Stop rate limiter cleanup goroutine to prevent memory leak
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}

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

// handleHealthDetailed returns detailed health status of all components
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
			"endpoints": []string{"/mcp", "/health", "/health/detailed", "/metrics"},
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
		},
	})
}

// handleOpenAPI returns OpenAPI-like documentation
func (s *MCPServer) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	s.handleOpenAPISpec(w, r)
}

// Legacy aliases for backwards compatibility
type SSEServer = MCPServer
type SSEConfig = MCPConfig

func NewSSEServer(mcpServer *server.MCPServer, cfg SSEConfig) *SSEServer {
	return NewMCPServer(mcpServer, cfg)
}
