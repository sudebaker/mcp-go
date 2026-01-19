package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/amphora/mcp-go/internal/transport"

	mark3 "github.com/mark3labs/mcp-go/server"

	"github.com/stretchr/testify/assert"
)

func TestServer(t *testing.T) {
	// Arrange
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "localhost",
			Port: 8080,
			Name: "test-server",
		},
	}

	mcpServer := mark3.NewMCPServer(
		cfg.Server.Name,
		"0.1.0",
		mark3.WithToolCapabilities(true),
		mark3.WithLogging(),
		mark3.WithRecovery(),
	)

	sseServer := transport.NewSSEServer(mcpServer, transport.SSEConfig{
		Host: cfg.Server.Host,
		Port: cfg.Server.Port,
	})

	// Do not call Start() (which binds to a port). Use the handler directly
	// to avoid port conflicts in test environments.

	// Create a test client
	req := httptest.NewRequest("GET", "http://localhost:8080/healthz", nil)
	w := httptest.NewRecorder()

	// Act - use the handler directly
	sseServer.Handler().ServeHTTP(w, req)

	// Assert: handler should not return 500. Accept 200 or 404 depending on routes.
	assert.NotEqual(t, http.StatusInternalServerError, w.Code)
	t.Log("Server responded with status code:", w.Code)

	// Clean up
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(cancelCtx, 10*time.Second)
	defer shutdownCancel()

	if err := sseServer.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}
}
