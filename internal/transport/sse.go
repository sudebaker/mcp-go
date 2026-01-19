package transport

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog/log"
)

// SSEServer wraps the mcp-go SSE server with additional functionality
type SSEServer struct {
	mcpServer *server.MCPServer
	sseServer *server.SSEServer
	addr      string
}

// SSEConfig holds SSE server configuration
type SSEConfig struct {
	Host              string
	Port              int
	BaseURL           string
	KeepAliveInterval time.Duration
}

// NewSSEServer creates a new SSE server
func NewSSEServer(mcpServer *server.MCPServer, cfg SSEConfig) *SSEServer {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://%s", addr)
	}

	opts := []server.SSEOption{
		server.WithBaseURL(baseURL),
		server.WithSSEEndpoint("/sse"),
		server.WithMessageEndpoint("/messages"),
	}

	if cfg.KeepAliveInterval > 0 {
		opts = append(opts, server.WithKeepAliveInterval(cfg.KeepAliveInterval))
	}

	sseServer := server.NewSSEServer(mcpServer, opts...)

	return &SSEServer{
		mcpServer: mcpServer,
		sseServer: sseServer,
		addr:      addr,
	}
}

// Start begins serving the SSE server
func (s *SSEServer) Start() error {
	log.Info().
		Str("addr", s.addr).
		Msg("Starting SSE server")
	return s.sseServer.Start(s.addr)
}

// Shutdown gracefully shuts down the server
func (s *SSEServer) Shutdown(ctx context.Context) error {
	log.Info().Msg("Shutting down SSE server")
	return s.sseServer.Shutdown(ctx)
}

// Handler returns the HTTP handler for the SSE server
func (s *SSEServer) Handler() http.Handler {
	return s.sseServer
}
