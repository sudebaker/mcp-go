package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/transport"

	mark3 "github.com/mark3labs/mcp-go/server"

	"github.com/stretchr/testify/assert"
)

func TestServer(t *testing.T) {
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

	pingReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	}
	body, _ := json.Marshal(pingReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	sseServer.Handler().ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusInternalServerError, w.Code)
	t.Log("Server responded with status code:", w.Code)

	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(cancelCtx, 10*time.Second)
	defer shutdownCancel()

	if err := sseServer.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}
}
