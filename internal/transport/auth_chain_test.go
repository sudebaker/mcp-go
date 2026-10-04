package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sudebaker/mcp-go/internal/auth"
)

func chainTestKeyring(t *testing.T, mode string, token string) *auth.Keyring {
	t.Helper()
	env := map[string]string{"MCP_AUTH_MODE": mode, "KEY_AMPHORA": token}
	k, err := auth.BuildKeyring(func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}, []auth.Entry{{UserID: "amphora", KeyEnv: "KEY_AMPHORA"}})
	if err != nil {
		t.Fatalf("failed to build keyring: %v", err)
	}
	return k
}

func chainTestServer(t *testing.T, k *auth.Keyring, origins []string) *MCPServer {
	t.Helper()
	return NewMCPServer(nil, MCPConfig{
		Host:           "127.0.0.1",
		Port:           0,
		ServerName:     "test-server",
		Version:        "1.0.0",
		AllowedOrigins: origins,
		Auth:           k,
	})
}

// CORS must wrap auth: an origin outside the allow-list is rejected before the
// bearer token is even looked at.
func TestMCPEndpointMiddleware_CORSRejectsOriginBeforeAuth(t *testing.T) {
	s := chainTestServer(t, chainTestKeyring(t, auth.ModeRequired, "amphora-token-abcdef"), []string{"https://allowed.example"})
	h := s.mcpEndpointMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not run for a disallowed origin")
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 from CORS, got %d", rec.Code)
	}
}

func TestMCPEndpointMiddleware_AllowedOriginStillNeedsToken(t *testing.T) {
	s := chainTestServer(t, chainTestKeyring(t, auth.ModeRequired, "amphora-token-abcdef"), []string{"https://allowed.example"})
	h := s.mcpEndpointMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not run without a token")
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://allowed.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 from auth, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://allowed.example" {
		t.Errorf("expected CORS headers on the 401 response, got %q", got)
	}
}

func TestMCPEndpointMiddleware_AuthenticatedRequestReachesHandler(t *testing.T) {
	s := chainTestServer(t, chainTestKeyring(t, auth.ModeRequired, "amphora-token-abcdef"), []string{"https://allowed.example"})

	var gotUser string
	h := s.mcpEndpointMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = auth.UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://allowed.example")
	req.Header.Set("Authorization", "Bearer amphora-token-abcdef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUser != "amphora" {
		t.Errorf("expected user_id amphora behind the chain, got %q", gotUser)
	}
}

// Preflight is answered by CORS before auth runs, so browsers can negotiate.
func TestMCPEndpointMiddleware_PreflightNeedsNoToken(t *testing.T) {
	s := chainTestServer(t, chainTestKeyring(t, auth.ModeRequired, "amphora-token-abcdef"), []string{"https://allowed.example"})
	h := s.mcpEndpointMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("preflight must not reach the MCP handler")
	}))

	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	req.Header.Set("Origin", "https://allowed.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
}

// A required keyring with no keys refuses to serve the MCP endpoints.
func TestMCPEndpointMiddleware_EmptyRequiredKeyringIs503(t *testing.T) {
	s := chainTestServer(t, auth.NewKeyring(auth.ModeRequired, nil), nil)
	h := s.mcpEndpointMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not run without a usable keyring")
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer amphora-token-abcdef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// MCP_AUTH_MODE=off is the only bypass, and it must be explicit.
func TestMCPEndpointMiddleware_ModeOffPassesThrough(t *testing.T) {
	s := chainTestServer(t, auth.NewKeyring(auth.ModeOff, nil), nil)
	called := false
	h := s.mcpEndpointMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if !called || rec.Code != http.StatusOK {
		t.Errorf("expected passthrough in off mode, got code %d (called=%v)", rec.Code, called)
	}
}

// /upload is guarded by the same keyring as the MCP endpoints: MCP_UPLOAD_API_KEY
// is one of its entries, so the upload endpoint no longer has an on/off rule of
// its own.
func TestUploadEndpoint_UsesTheSameKeyring(t *testing.T) {
	s := chainTestServer(t, chainTestKeyring(t, auth.ModeRequired, "amphora-token-abcdef"), nil)

	reached := false
	h := s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/upload", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("upload without a token: expected 401, got %d", rec.Code)
	}
	if reached {
		t.Error("upload handler must not run without a token")
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", nil)
	req.Header.Set("Authorization", "Bearer amphora-token-abcdef")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !reached {
		t.Errorf("upload with a valid token: expected 200 and handler reached, got %d (reached=%v)", rec.Code, reached)
	}
}
