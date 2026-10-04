package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSMiddleware_AllowedOrigin(t *testing.T) {
	allowedOrigins := []string{"http://localhost:3000", "https://example.com"}
	middleware := CORSMiddleware(allowedOrigins)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("Expected CORS header, got %s", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Vary") != "Origin" {
		t.Errorf("Expected Vary: Origin header, got %s", w.Header().Get("Vary"))
	}
	if w.Header().Get("Access-Control-Allow-Methods") != "GET, POST, DELETE, OPTIONS" {
		t.Errorf("Expected Allow-Methods header, got %s", w.Header().Get("Access-Control-Allow-Methods"))
	}
	if w.Header().Get("Access-Control-Allow-Headers") != "Content-Type, Accept, Authorization, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID" {
		t.Errorf("Expected Allow-Headers header with all fields, got %s", w.Header().Get("Access-Control-Allow-Headers"))
	}
	if w.Header().Get("Access-Control-Expose-Headers") != "Mcp-Session-Id, X-Request-ID" {
		t.Errorf("Expected Expose-Headers header, got %s", w.Header().Get("Access-Control-Expose-Headers"))
	}
}

// A browser only gets to send Authorization if the preflight allows it;
// otherwise the request dies in CORS and the client never sees the 401/503 that
// the auth middleware produces.
func TestCORSMiddleware_PreflightAllowsAuthorization(t *testing.T) {
	middleware := CORSMiddleware([]string{"https://lobe-chat.example.com"})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("preflight must not reach the handler")
	}))

	req := httptest.NewRequest("OPTIONS", "/mcp", nil)
	req.Header.Set("Origin", "https://lobe-chat.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("Expected 204, got %d", w.Code)
	}
	allowed := make(map[string]bool)
	for _, h := range strings.Split(w.Header().Get("Access-Control-Allow-Headers"), ",") {
		allowed[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for _, header := range []string{"authorization", "content-type"} {
		if !allowed[header] {
			t.Errorf("Access-Control-Allow-Headers %q does not allow %q",
				w.Header().Get("Access-Control-Allow-Headers"), header)
		}
	}
}

// A JavaScript client cannot read the MCP session id unless the server exposes
// it: it is not a CORS-safelisted response header.
func TestCORSMiddleware_ExposesSessionIDHeader(t *testing.T) {
	middleware := CORSMiddleware([]string{"https://lobe-chat.example.com"})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Mcp-Session-Id", "3f2b7c1e-0000-4000-8000-000000000000")
		w.Header().Set("X-Request-ID", "req-1")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Origin", "https://lobe-chat.example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	exposed := make(map[string]bool)
	for _, h := range strings.Split(w.Header().Get("Access-Control-Expose-Headers"), ",") {
		exposed[strings.ToLower(strings.TrimSpace(h))] = true
	}
	if !exposed["mcp-session-id"] {
		t.Errorf("Mcp-Session-Id is not readable by JS clients: Access-Control-Expose-Headers=%q",
			w.Header().Get("Access-Control-Expose-Headers"))
	}
}

func TestCORSMiddleware_DisallowedOrigin(t *testing.T) {
	allowedOrigins := []string{"http://localhost:3000"}
	middleware := CORSMiddleware(allowedOrigins)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Origin", "http://evil.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 403, got %d", w.Code)
	}
	// Disallowed origins should NOT have CORS headers
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("Expected NO CORS header for disallowed origin, got %s", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSMiddleware_DisallowedOriginPreflight(t *testing.T) {
	allowedOrigins := []string{"http://localhost:3000"}
	middleware := CORSMiddleware(allowedOrigins)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("OPTIONS", "/mcp", nil)
	req.Header.Set("Origin", "http://evil.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 403, got %d", w.Code)
	}
}

func TestCORSMiddleware_EmptyOriginsList(t *testing.T) {
	middleware := CORSMiddleware([]string{})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Origin", "http://any-origin.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "http://any-origin.com" {
		t.Errorf("Expected CORS header for any origin, got %s", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Vary") != "Origin" {
		t.Errorf("Expected Vary: Origin header, got %s", w.Header().Get("Vary"))
	}
}

func TestCORSMiddleware_Preflight(t *testing.T) {
	allowedOrigins := []string{"http://localhost:3000"}
	middleware := CORSMiddleware(allowedOrigins)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("OPTIONS", "/mcp", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected 204, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Methods") != "GET, POST, DELETE, OPTIONS" {
		t.Errorf("Expected Allow-Methods header, got %s", w.Header().Get("Access-Control-Allow-Methods"))
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("Expected CORS header in preflight, got %s", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSMiddleware_NoOriginHeader(t *testing.T) {
	middleware := CORSMiddleware([]string{})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/mcp", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("Expected * when no origin header and empty list, got %s", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSMiddleware_NoOriginHeaderWithRestrictedList(t *testing.T) {
	// When allowed_origins is restricted and no Origin header is sent,
	// no CORS headers should be added (strict mode)
	allowedOrigins := []string{"http://localhost:3000"}
	middleware := CORSMiddleware(allowedOrigins)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/mcp", nil)
	// No Origin header
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	// Should NOT add CORS headers without Origin header in restricted mode
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("Expected NO CORS header when no Origin header in restricted mode, got %s", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSMiddleware_OriginWithWhitespace(t *testing.T) {
	// Test that origins with whitespace are trimmed correctly
	allowedOrigins := []string{"http://localhost:3000"}
	middleware := CORSMiddleware(allowedOrigins)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Origin", "  http://localhost:3000  ") // with whitespace
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("Expected trimmed origin, got %s", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSMiddleware_LastEventIDHeader(t *testing.T) {
	// Test that Last-Event-ID is included in Access-Control-Allow-Headers (for SSE support)
	allowedOrigins := []string{"http://localhost:3000"}
	middleware := CORSMiddleware(allowedOrigins)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("OPTIONS", "/sse", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected 204, got %d", w.Code)
	}

	allowHeaders := w.Header().Get("Access-Control-Allow-Headers")
	if allowHeaders == "" {
		t.Errorf("Expected Access-Control-Allow-Headers, got empty")
	}

	// Check that Last-Event-ID is in the allowed headers
	if !strings.Contains(allowHeaders, "Last-Event-ID") {
		t.Errorf("Expected Last-Event-ID in Allow-Headers for SSE support, got %s", allowHeaders)
	}
}
