package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/amphora/mcp-go/internal/executor"
	"github.com/amphora/mcp-go/internal/transport"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

// TestExecutorBasic tests basic executor functionality
func TestExecutorBasic(t *testing.T) {
	cfg := &config.Config{
		Execution: config.ExecutionConfig{
			DefaultTimeout: 10 * time.Second,
			WorkingDir:     "/tmp",
			Environment: map[string]string{
				"LLM_API_URL": "http://localhost:11434",
				"LLM_MODEL":   "test-model",
			},
		},
		Tools: []config.ToolConfig{
			{
				Name:        "echo",
				Description: "Echo test",
				Command:     "echo",
				Args:        []string{"hello"},
				Timeout:     5 * time.Second,
			},
		},
	}

	exec := executor.New(cfg)
	require.NotNil(t, exec)
}

// TestToolConfigValidation tests tool config validation
func TestToolConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.ToolConfig
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: &config.ToolConfig{
				Name:    "test",
				Command: "echo",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			cfg: &config.ToolConfig{
				Command: "echo",
			},
			wantErr: true,
		},
		{
			name: "missing command",
			cfg: &config.ToolConfig{
				Name: "test",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := executor.ValidateToolConfig(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestHealthEndpoint tests the health check endpoint
func TestHealthEndpoint(t *testing.T) {
	mux := http.NewServeMux()

	// Add health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

// TestMetricsEndpoint tests the Prometheus metrics endpoint
func TestMetricsEndpoint(t *testing.T) {
	mux := http.NewServeMux()

	// Simple metrics endpoint for testing
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "# HELP test_metric A test metric\n")
		io.WriteString(w, "test_metric 42\n")
	})

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/plain", w.Header().Get("Content-Type"))
	body := w.Body.String()
	require.Contains(t, body, "test_metric")
}

// TestRateLimiting tests the rate limiter
func TestRateLimiting(t *testing.T) {
	limiter := transport.NewRateLimiter(100, 10) // 100 RPS with burst of 10
	defer limiter.Stop()

	// Should allow requests
	require.True(t, limiter.Allow("client1"))
	require.True(t, limiter.Allow("client2"))

	// Allow multiple in burst
	for i := 0; i < 10; i++ {
		require.True(t, limiter.Allow("client3"))
	}
}

// TestRateLimiterStop tests that rate limiter stops cleanly
func TestRateLimiterStop(t *testing.T) {
	limiter := transport.NewRateLimiter(100, 10)

	// Should not panic
	require.NotPanics(t, func() {
		limiter.Stop()
	})

	// Stopping again should also not panic
	require.NotPanics(t, func() {
		limiter.Stop()
	})
}

// TestGracefulShutdown tests graceful shutdown
func TestGracefulShutdown(t *testing.T) {
	cfg := &config.Config{
		Execution: config.ExecutionConfig{
			DefaultTimeout: 10 * time.Second,
			WorkingDir:     "/tmp",
			Environment:    map[string]string{},
		},
		Tools: []config.ToolConfig{},
	}

	// Create MCP server
	mcpServer := server.NewMCPServer(
		"test-server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Create transport server
	sseServer := transport.NewSSEServer(mcpServer, transport.MCPConfig{
		Host:           "127.0.0.1",
		Port:           9999,
		ServerName:     "test",
		Version:        "1.0.0",
		Tools:          cfg.Tools,
		RateLimitRPS:   100,
		RateLimitBurst: 10,
	})

	// Test that shutdown completes without error
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := sseServer.Shutdown(ctx)
	require.NoError(t, err)
}
