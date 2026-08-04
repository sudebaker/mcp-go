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
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/sudebaker/mcp-go/internal/admin"
	"github.com/sudebaker/mcp-go/internal/auth"
	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/health"
	"github.com/sudebaker/mcp-go/internal/metrics"
	"github.com/sudebaker/mcp-go/internal/resources"
	"github.com/sudebaker/mcp-go/internal/tracing"
)

// uploadKeyHash is the SHA-256 hash of MCP_UPLOAD_API_KEY, pre-computed at startup.
var uploadKeyHash [32]byte

func init() {
	key := os.Getenv("MCP_UPLOAD_API_KEY")
	if key != "" {
		uploadKeyHash = sha256.Sum256([]byte(key))
	}
}

// MCPServer wraps the mcp-go library server with additional functionality.
// It provides HTTP serving, middleware chaining, and management endpoints.
type MCPServer struct {
	mcpServer       *server.MCPServer            // Core MCP server implementation
	streamServer    *server.StreamableHTTPServer // Streamable HTTP handler
	sseServer       *server.SSEServer            // Legacy SSE server
	httpServer      *http.Server                 // Go HTTP server instance
	addr            string                       // Listen address (host:port)
	serverName      string                       // Service name for logging/health
	version         string                       // Semantic version
	tools           []config.ToolConfig          // Tool configurations for docs
	rateLimiter     *RateLimiter                 // Rate limiting middleware (nil if disabled)
	tracer          *tracing.Tracer              // Distributed tracing
	allowedOrigins  []string                     // CORS allowed origins (empty = all)
	uploadConfig    config.UploadConfig          // Upload endpoint configuration
	filesDir        string                       // Directory for serving generated files via /files/
	healthChecker   *health.Checker              // Health checker for dependency status
	resourceManager *resources.ResourceManager   // Resource resolver/token manager
	adminKey        string                       // ADMIN_API_KEY for admin endpoints (empty = disabled)
	adminKeyHash    [32]byte                     // SHA-256 of adminKey, pre-computed for time-constant compare
	db              *sql.DB                      // Database connection for admin endpoints
	stopCh          chan struct{}
	stopCleanupOnce sync.Once
	maxMCPBodySize  int64
}

// SetResourceManager sets the resource manager used by the MCP server.
func (s *MCPServer) SetResourceManager(m *resources.ResourceManager) {
	s.resourceManager = m
}

// authMiddleware wraps a handler with API key authentication for the upload endpoint.
// Requires header: Authorization: Bearer <api_key>. If MCP_UPLOAD_API_KEY is not set,
// authentication is skipped (for backward compatibility).
func (s *MCPServer) authMiddleware(next http.HandlerFunc) http.Handler {
	return auth.BearerAuth(uploadKeyHash, auth.OnEmptySkip, next)
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
	// Upload is the file upload configuration
	Upload config.UploadConfig
	// FilesDir is the absolute path to serve generated files from (e.g., /data/reports)
	FilesDir string
	// HealthChecker performs health checks against dependencies (nil = no-op)
	HealthChecker *health.Checker
	// AdminKey is the ADMIN_API_KEY for admin endpoints (empty = disabled)
	AdminKey string
	// DB is the database connection for admin endpoints (nil = admin endpoints disabled)
	DB *sql.DB
	// MaxMCPBodySizeMB is max request body size for MCP endpoints (0 = default 10MB)
	MaxMCPBodySizeMB int64
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

	var adminKeyHash [32]byte
	if cfg.AdminKey != "" {
		adminKeyHash = sha256.Sum256([]byte(cfg.AdminKey))
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
		uploadConfig:   cfg.Upload,
		filesDir:       cfg.FilesDir,
		healthChecker:  cfg.HealthChecker,
		adminKey:       cfg.AdminKey,
		adminKeyHash:   adminKeyHash,
		db:             cfg.DB,
		maxMCPBodySize: maxBodySize(cfg.MaxMCPBodySizeMB),
		stopCh:         make(chan struct{}),
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

	// Upload endpoint (POST /upload) - protected with API key auth
	uploadHandler := auth.BearerAuth(uploadKeyHash, auth.OnEmptySkip, http.HandlerFunc(s.handleUpload))
	mux.Handle("/upload", uploadHandler)

	// Files endpoint (GET /files/{tool}/{filename}) - serve generated files
	mux.HandleFunc("/files/", s.handleFiles)

	// Internal resource streaming endpoint (GET /internal/resource/{token})
	// Not protected by auth middleware; relies on Docker network isolation.
	mux.HandleFunc("/internal/resource/", s.handleInternalResource)

	// Admin endpoints (protected with ADMIN_API_KEY)
	s.registerAdminRoutes(mux)

	// Start background TTL cleanup goroutine for uploaded files
	go s.startUploadCleanup()

	// Prepare middleware chain: MaxBody -> CORS -> Rate Limiter -> Path Sanitizer -> Mux Handler
	bodyMiddleware := MaxBodyMiddleware(s.maxMCPBodySize)

	var streamHandler http.Handler = s.streamServer
	if s.rateLimiter != nil {
		streamHandler = s.rateLimiter.Middleware(streamHandler)
	}
	streamHandler = CORSMiddleware(s.allowedOrigins)(streamHandler)
	streamHandler = bodyMiddleware(streamHandler)

	// Build multi-handler chain: first the custom mux, then the SSE/stream handlers
	// The mux handles health, metrics, upload, files; streamHandler handles MCP SSE/streamable HTTP
	// Wrap the entire mux with sanitizePathMiddleware to prevent path normalization attacks
	sanitizedMux := sanitizePathMiddleware(mux)

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
	sseHandler = bodyMiddleware(sseHandler)

	messageHandler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		messageServerHandler.ServeHTTP(w, r)
	}))
	if s.rateLimiter != nil {
		messageHandler = s.rateLimiter.Middleware(messageHandler)
	}
	messageHandler = CORSMiddleware(s.allowedOrigins)(messageHandler)
	messageHandler = bodyMiddleware(messageHandler)

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

	maxBodyMB := s.maxMCPBodySize / (1024 * 1024)
	if s.maxMCPBodySize%(1024*1024) != 0 {
		maxBodyMB = s.maxMCPBodySize / (1024 * 1024)
	}
	log.Info().
		Int64("max_body_bytes", s.maxMCPBodySize).
		Int64("max_body_mb", maxBodyMB).
		Msg("SSE transport active on /sse (GET) and /message (POST)")

	// Wrap entire mux with sanitize middleware, tracing and logging middleware
	var handler http.Handler = sanitizedMux
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

// registerAdminRoutes sets up admin endpoints with auth, rate limiting, and CORS.
// Only registers if admin key and DB are configured.
func (s *MCPServer) registerAdminRoutes(mux *http.ServeMux) {
	if s.adminKey == "" || s.db == nil {
		log.Warn().Msg("Admin endpoints disabled: ADMIN_API_KEY or DB not configured")
		return
	}

	adminHandler := admin.NewHandler(s.db)

	if err := adminHandler.SetupMigrations(context.Background()); err != nil {
		log.Error().Err(err).Msg("Failed to apply admin migrations")
	}

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/admin/kb/users", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/admin/kb/users")
		if r.Method == http.MethodGet {
			if path == "" || path == "/" {
				metrics.RecordAdminOperation("list_users", r.Method, "success")
				adminHandler.ListUsers(w, r)
			} else if strings.HasSuffix(path, "/export") {
				metrics.RecordAdminOperation("export_user", r.Method, "success")
				adminHandler.ExportUser(w, r)
			} else {
				metrics.RecordAdminOperation("get_user", r.Method, "success")
				adminHandler.GetUser(w, r)
			}
		} else if r.Method == http.MethodDelete {
			metrics.RecordAdminOperation("delete_user_data", r.Method, "success")
			adminHandler.DeleteUserData(w, r)
		} else {
			metrics.RecordAdminOperation("users", r.Method, "method_not_allowed")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/admin/kb/users/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/admin/kb/users/")
		if strings.Contains(path, "/collections/") {
			if r.Method == http.MethodDelete {
				metrics.RecordAdminOperation("delete_user_collection", r.Method, "success")
				adminHandler.DeleteUserCollection(w, r)
			} else {
				metrics.RecordAdminOperation("delete_user_collection", r.Method, "method_not_allowed")
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		} else if strings.HasSuffix(path, "/export") {
			metrics.RecordAdminOperation("export_user", r.Method, "success")
			adminHandler.ExportUser(w, r)
		} else if r.Method == http.MethodGet {
			metrics.RecordAdminOperation("get_user", r.Method, "success")
			adminHandler.GetUser(w, r)
		} else if r.Method == http.MethodDelete {
			metrics.RecordAdminOperation("delete_user_data", r.Method, "success")
			adminHandler.DeleteUserData(w, r)
		} else {
			metrics.RecordAdminOperation("users", r.Method, "method_not_allowed")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/admin/kb/collections/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			metrics.RecordAdminOperation("delete_global_collection", r.Method, "success")
			adminHandler.DeleteGlobalCollection(w, r)
		} else {
			metrics.RecordAdminOperation("delete_global_collection", r.Method, "method_not_allowed")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/admin/audit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			metrics.RecordAdminOperation("audit_log", r.Method, "success")
			adminHandler.AuditLog(w, r)
		} else {
			metrics.RecordAdminOperation("audit_log", r.Method, "method_not_allowed")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/admin/audit/", func(w http.ResponseWriter, r *http.Request) {
		metrics.RecordAdminOperation("audit_log", r.Method, "success")
		adminHandler.AuditLog(w, r)
	})

	adminGetLimiter := NewRateLimiter(1.0, 60)
	adminDeleteLimiter := NewRateLimiter(0.17, 10)
	rateLimitedAdminMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			adminGetLimiter.Middleware(adminMux).ServeHTTP(w, r)
		case http.MethodDelete:
			adminDeleteLimiter.Middleware(adminMux).ServeHTTP(w, r)
		default:
			adminMux.ServeHTTP(w, r)
		}
	})

	handler := auth.BearerAuth(s.adminKeyHash, auth.OnEmpty503, rateLimitedAdminMux)
	handler = AdminCORSMiddleware(s.allowedOrigins)(handler)

	if s.adminKey != "" && len(s.allowedOrigins) == 0 {
		log.Warn().Msg("ADMIN_API_KEY is set but CORS allowed_origins is empty — admin endpoints accessible from any origin")
	}

	mux.Handle("/admin/", handler)
	log.Info().Msg("Admin endpoints enabled on /admin/")
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

// handleHealth returns basic server health status with dependency checks.
//
// This endpoint runs all health checks (redis, postgres, config, memory,
// and configured external dependencies). If any critical dependency is
// unreachable, returns HTTP 503 Service Unavailable.
//
// Response format:
//
//	{
//	  "status": "healthy|degraded|unhealthy",
//	  "service": "mcp-orchestrator",
//	  "version": "1.0.0",
//	  "protocol": "mcp",
//	  "transport": "streamable-http + sse",
//	  "dependencies": {"redis": "healthy", ...},
//	  "endpoints": {...}
//	}
func (s *MCPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := "healthy"
	httpStatus := http.StatusOK
	deps := make(map[string]string)

	if s.healthChecker != nil {
		results := s.healthChecker.RunAllChecks(r.Context())
		overall := s.healthChecker.GetOverallStatus(results)

		for _, r := range results {
			deps[r.Name] = string(r.Status)
		}

		if overall == health.StatusUnhealthy {
			status = "unhealthy"
			httpStatus = http.StatusServiceUnavailable
		} else if overall == health.StatusDegraded {
			status = "degraded"
			// Degraded still returns OK but signals partial issues
		}
	}

	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":       status,
		"service":      s.serverName,
		"version":      s.version,
		"protocol":     "mcp",
		"transport":    "streamable-http + sse",
		"dependencies": deps,
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

	deps := make(map[string]interface{})
	overallStatus := "healthy"

	if s.healthChecker != nil {
		results := s.healthChecker.RunAllChecks(r.Context())
		for _, r := range results {
			deps[r.Name] = map[string]interface{}{
				"status":      string(r.Status),
				"message":     r.Message,
				"duration_ms": r.Duration.Milliseconds(),
			}
		}
		overall := s.healthChecker.GetOverallStatus(results)
		overallStatus = string(overall)
	} else {
		// Fallback when no health checker is configured
		deps["redis"] = map[string]string{"status": "unknown"}
		deps["postgres"] = map[string]string{"status": "unknown"}
	}

	components["dependencies"] = deps

	response := map[string]interface{}{
		"status":     overallStatus,
		"timestamp":  time.Now().Format(time.RFC3339),
		"service":    s.serverName,
		"version":    s.version,
		"components": components,
	}

	httpStatus := http.StatusOK
	if overallStatus == "unhealthy" {
		httpStatus = http.StatusServiceUnavailable
	}

	w.WriteHeader(httpStatus)
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

// handleFiles serves generated files via GET /files/{tool}/{filename}.
// Resolves to {filesDir}/{tool}/{filename} and serves with Content-Type detection.
// Path traversal is blocked — only paths containing ".." are rejected.
func (s *MCPServer) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.filesDir == "" {
		http.Error(w, "Files serving not configured", http.StatusNotFound)
		return
	}

	// Extract path after /files/ prefix
	// r.URL.Path is "/files/reports/filename.pdf" → path = "reports/filename.pdf"
	path := strings.TrimPrefix(r.URL.Path, "/files/")

	// SECURITY: Block path traversal attempts
	if strings.Contains(path, "..") {
		log.Warn().Str("path", path).Str("remote", r.RemoteAddr).Msg("Path traversal blocked in /files/")
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// SECURITY: Block paths starting with / to prevent absolute path access
	if strings.HasPrefix(path, "/") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Resolve to absolute path
	filePath := filepath.Join(s.filesDir, path)

	// Verify the resolved path is within filesDir (double-check against symlink traversal)
	absFilesDir, _ := filepath.Abs(s.filesDir)
	absFilePath, _ := filepath.Abs(filePath)
	if !strings.HasPrefix(absFilePath, absFilesDir) {
		log.Warn().Str("resolved", absFilePath).Str("allowed", absFilesDir).Msg("Path escape detected in /files/")
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Check file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// Serve the file
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(filePath)))
	http.ServeFile(w, r, filePath)

	log.Info().Str("path", filePath).Str("remote", r.RemoteAddr).Msg("File served via /files/")
}

// sanitizePathMiddleware intercepts requests with suspicious path patterns
// before Go's ServeMux normalizes them (which would cause 301 redirects).
// Go's ServeMux cleans paths like "/foo//bar" → "/foo/bar" and issues a redirect.
// This middleware prevents the redirect by rejecting the malformed request early.
//
// Security: Without this, an attacker could probe for path normalization
// behavior or force redirects that reveal server internals.
func sanitizePathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block double slashes — Go's ServeMux would normalize them,
		// potentially bypassing path traversal checks in handlers
		if strings.Contains(r.URL.Path, "//") {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
