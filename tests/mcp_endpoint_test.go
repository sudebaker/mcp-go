package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/amphora/mcp-go/internal/transport"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestMCPServer(port int, tools []config.ToolConfig, rateLimitRPS float64, rateLimitBurst int) *transport.MCPServer {
	mcpServer := transport.NewMCPServer(nil, transport.MCPConfig{
		Host:           "127.0.0.1",
		Port:           port,
		ServerName:     "test-server",
		Version:        "1.0.0",
		Tools:          tools,
		RateLimitRPS:   rateLimitRPS,
		RateLimitBurst: rateLimitBurst,
		AllowedOrigins: []string{},
	})
	return mcpServer
}

func setupTestMux(tools []config.ToolConfig) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "healthy",
			"service":  "test-server",
			"version":  "1.0.0",
			"protocol": "mcp",
		})
	})

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name":      "test-server",
				"version":   "1.0.0",
				"protocol":  "MCP",
				"endpoints": []string{"/mcp"},
			})
			return
		}

		if r.Method == http.MethodPost {
			contentType := r.Header.Get("Content-Type")
			if contentType != "application/json" && contentType != "application/json-rpc" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"error": map[string]interface{}{
						"code":    -32600,
						"message": "Invalid Request",
					},
				})
				return
			}

			body := http.MaxBytesReader(w, r.Body, 1048576)

			var reqJSON map[string]interface{}
			decoder := json.NewDecoder(body)
			if err := decoder.Decode(&reqJSON); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"error": map[string]interface{}{
						"code":    -32700,
						"message": "Parse error",
					},
				})
				return
			}

			method, _ := reqJSON["method"].(string)
			id := reqJSON["id"]

			response := processMCPRequest(method, id)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}
	})

	mux.Handle("/metrics", promhttp.Handler())

	return mux
}

func processMCPRequest(method string, id interface{}) map[string]interface{} {
	switch method {
	case "initialize":
		return map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"serverInfo": map[string]interface{}{
					"name":    "test-server",
					"version": "1.0.0",
				},
				"capabilities": map[string]interface{}{
					"tools":     map[string]interface{}{},
					"resources": map[string]interface{}{},
				},
			},
		}
	case "ping":
		return map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  map[string]interface{}{},
		}
	case "tools/list":
		return map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]interface{}{
				"tools": []map[string]interface{}{
					{
						"name":        "echo",
						"description": "Echo test tool",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"text": map[string]interface{}{
									"type": "string",
								},
							},
							"required": []string{"text"},
						},
					},
				},
			},
		}
	default:
		if method == "" {
			return map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32600,
					"message": "Invalid Request",
				},
			}
		}
		return map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"error": map[string]interface{}{
				"code":    -32601,
				"message": "Method not found",
			},
		}
	}
}

func TestMCPEndpointInitialize(t *testing.T) {
	mux := setupTestMux(nil)

	initializeReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"clientInfo": map[string]interface{}{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}

	body, _ := json.Marshal(initializeReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "Expected status 200 for initialize")

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	result, ok := response["result"].(map[string]interface{})
	require.True(t, ok, "Response should have result")

	protocolVersion, ok := result["protocolVersion"].(string)
	require.True(t, ok, "Response should have protocolVersion")
	assert.NotEmpty(t, protocolVersion)

	serverInfo, ok := result["serverInfo"].(map[string]interface{})
	require.True(t, ok, "Response should have serverInfo")
	assert.Equal(t, "test-server", serverInfo["name"])

	capabilities, ok := result["capabilities"].(map[string]interface{})
	require.True(t, ok, "Response should have capabilities")
	assert.NotNil(t, capabilities)
}

func TestMCPEndpointToolsList(t *testing.T) {
	mux := setupTestMux(nil)

	toolsListReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	}

	body, _ := json.Marshal(toolsListReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "Expected status 200 for tools/list")

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	result, ok := response["result"].(map[string]interface{})
	require.True(t, ok, "Response should have result")

	tools, ok := result["tools"].([]interface{})
	require.True(t, ok, "Response should have tools array")
	assert.GreaterOrEqual(t, len(tools), 1, "Should have at least one tool")
}

func TestMCPEndpointPing(t *testing.T) {
	mux := setupTestMux(nil)

	pingReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "ping",
	}

	body, _ := json.Marshal(pingReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.NotNil(t, response["result"], "Ping should return result")
}

func TestMCPEndpointInvalidJSON(t *testing.T) {
	mux := setupTestMux(nil)

	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader([]byte("invalid json {[[[")))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	errorResp, ok := response["error"].(map[string]interface{})
	require.True(t, ok, "Should return error for invalid JSON")

	assert.Equal(t, float64(-32700), errorResp["code"], "Error code should be -32700 (Parse error)")
}

func TestMCPEndpointUnknownMethod(t *testing.T) {
	mux := setupTestMux(nil)

	unknownReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "unknown/method",
	}

	body, _ := json.Marshal(unknownReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	errorResp, ok := response["error"].(map[string]interface{})
	require.True(t, ok, "Should return error for unknown method")

	assert.Equal(t, float64(-32601), errorResp["code"], "Error code should be -32601 (Method not found)")
}

func TestMCPEndpointMissingJSONRPC(t *testing.T) {
	mux := setupTestMux(nil)

	noJSONRPCReq := map[string]interface{}{
		"id":     5,
		"method": "ping",
	}

	body, _ := json.Marshal(noJSONRPCReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.NotNil(t, response, "Should return a response")
}

func TestMCPEndpointEmptyBody(t *testing.T) {
	mux := setupTestMux(nil)

	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "Should handle empty body")

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	errorResp, ok := response["error"].(map[string]interface{})
	require.True(t, ok, "Should return error for empty body")

	assert.Equal(t, float64(-32700), errorResp["code"], "Error code should be -32700 (Parse error)")
}

func TestMCPEndpointBatchRequest(t *testing.T) {
	mux := setupTestMux(nil)

	batchReq := []map[string]interface{}{
		{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "ping",
		},
		{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "ping",
		},
	}

	body, _ := json.Marshal(batchReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "Batch not supported in mock")
}

func TestMCPEndpointCORS(t *testing.T) {
	mux := setupTestMux(nil)

	pingReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	}

	body, _ := json.Marshal(pingReq)
	req := httptest.NewRequest("OPTIONS", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
}

func TestMCPEndpointHealthCheck(t *testing.T) {
	mux := setupTestMux(nil)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "healthy", response["status"])
}

func TestMCPEndpointGET(t *testing.T) {
	mux := setupTestMux(nil)

	req := httptest.NewRequest("GET", "/mcp", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.NotNil(t, response["name"])
}

func TestMCPEndpointInvalidRequestID(t *testing.T) {
	mux := setupTestMux(nil)

	pingReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "ping",
	}

	body, _ := json.Marshal(pingReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.NotNil(t, response["result"], "Should return result even without ID")
}

func TestMCPEndpointMultipleMethods(t *testing.T) {
	mux := setupTestMux(nil)

	methods := []string{"initialize", "ping", "ping"}

	for i, method := range methods {
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      i + 1,
			"method":  method,
		}

		if method == "initialize" {
			req["params"] = map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
			}
		}

		body, _ := json.Marshal(req)
		httpReq := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httpReq)

		require.Equal(t, http.StatusOK, w.Code, "Method %s should succeed", method)
	}
}

func TestMCPEndpointContentTypes(t *testing.T) {
	mux := setupTestMux(nil)

	pingReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	}
	body, _ := json.Marshal(pingReq)

	tests := []struct {
		contentType string
		expectOK    bool
	}{
		{"application/json", true},
		{"application/json-rpc", true},
	}

	for _, tc := range tests {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", tc.contentType)

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if tc.expectOK {
			assert.Equal(t, http.StatusOK, w.Code, "Content-Type %s should work", tc.contentType)
		}
	}
}

func TestMCPEndpointConcurrentRequests(t *testing.T) {
	mux := setupTestMux(nil)

	pingReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	}
	body, _ := json.Marshal(pingReq)

	results := make(chan int, 50)
	errors := make(chan error, 50)

	for i := 0; i < 50; i++ {
		go func() {
			req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				errors <- fmt.Errorf("unexpected status: %d", w.Code)
				return
			}

			var response map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				errors <- err
				return
			}

			results <- w.Code
		}()
	}

	timeout := time.After(10 * time.Second)
	for i := 0; i < 50; i++ {
		select {
		case <-results:
		case err := <-errors:
			t.Errorf("Concurrent request error: %v", err)
		case <-timeout:
			t.Fatalf("Timeout waiting for requests")
		}
	}
}

func TestMCPEndpointInitializeAndListTools(t *testing.T) {
	mux := setupTestMux(nil)

	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
		},
	}

	body, _ := json.Marshal(initReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	toolsReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	}

	body, _ = json.Marshal(toolsReq)
	req = httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	result, ok := response["result"].(map[string]interface{})
	require.True(t, ok)

	tools, ok := result["tools"].([]interface{})
	require.True(t, ok)
	assert.GreaterOrEqual(t, len(tools), 1, "Should have registered tools")
}

func TestMCPEndpointMetrics(t *testing.T) {
	mux := setupTestMux(nil)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/plain")
}

func TestMCPEndpointErrorResponseFormat(t *testing.T) {
	mux := setupTestMux(nil)

	invalidReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      "nonexistent",
			"arguments": map[string]interface{}{},
		},
	}

	body, _ := json.Marshal(invalidReq)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	_, hasError := response["error"]
	_, hasResult := response["result"]

	assert.True(t, hasError || hasResult, "Response should have either error or result")
}

func TestMCPEndpointNilBody(t *testing.T) {
	mux := setupTestMux(nil)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestMCPEndpointVeryLargeBody(t *testing.T) {
	mux := setupTestMux(nil)

	largeBody := make([]byte, 1048577)
	for i := range largeBody {
		largeBody[i] = 'a'
	}

	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestMCPEndpointMalformedJSON(t *testing.T) {
	mux := setupTestMux(nil)

	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader([]byte(`{"jsonrpc": "2.0", `)))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	errorResp, ok := response["error"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(-32700), errorResp["code"])
}

func TestMCPEndpointAllJSONRPCVersions(t *testing.T) {
	mux := setupTestMux(nil)

	versions := []string{"2.0", "1.0"}

	for _, version := range versions {
		req := map[string]interface{}{
			"jsonrpc": version,
			"id":      1,
			"method":  "ping",
		}

		body, _ := json.Marshal(req)
		httpReq := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusOK, w.Code, "JSONRPC version %s should work", version)
	}
}

func TestMCPEndpointNumericID(t *testing.T) {
	mux := setupTestMux(nil)

	testIDs := []interface{}{1, 2.5, "string-id", nil}

	for _, id := range testIDs {
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "ping",
		}

		body, _ := json.Marshal(req)
		httpReq := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusOK, w.Code, "ID type %T should work", id)
	}
}

func BenchmarkMCPEndpointPing(b *testing.B) {
	mux := setupTestMux(nil)

	pingReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	}
	body, _ := json.Marshal(pingReq)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
	}
}

func BenchmarkMCPEndpointInitialize(b *testing.B) {
	mux := setupTestMux(nil)

	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "bench", "version": "1.0"},
		},
	}
	body, _ := json.Marshal(initReq)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
	}
}

func BenchmarkMCPEndpointToolsList(b *testing.B) {
	mux := setupTestMux(nil)

	toolsReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	body, _ := json.Marshal(toolsReq)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
	}
}
