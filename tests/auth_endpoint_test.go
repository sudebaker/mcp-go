package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sudebaker/mcp-go/internal/auth"
	"github.com/sudebaker/mcp-go/internal/session"
	"github.com/sudebaker/mcp-go/internal/transport"
)

// These tests run the real HTTP stack: real mux, real middleware chain, real
// mcp-go server and the real session store. They are the end-to-end counterpart
// of the unit tests in internal/auth and internal/transport.

// freePort reserves a loopback port and releases it so the server can bind it.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("failed to release the reserved port: %v", err)
	}
	return port
}

// buildKeyring builds a required keyring whose tokens come from an in-memory
// environment map, so no real environment variable is touched.
func buildKeyring(t *testing.T, tokens map[string]string) *auth.Keyring {
	t.Helper()
	env := map[string]string{"MCP_AUTH_MODE": auth.ModeRequired}
	entries := make([]auth.Entry, 0, len(tokens))
	for userID, token := range tokens {
		envVar := "KEY_" + strings.ToUpper(userID)
		env[envVar] = token
		entries = append(entries, auth.Entry{UserID: userID, KeyEnv: envVar})
	}

	keyring, err := auth.BuildKeyring(func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}, entries)
	if err != nil {
		t.Fatalf("failed to build keyring: %v", err)
	}
	return keyring
}

// startAuthServer boots the server on a loopback port and returns its base URL
// together with the session store the hook writes to.
func startAuthServer(t *testing.T, keyring *auth.Keyring) (*session.Store, string) {
	t.Helper()

	sessionStore := session.New()

	hooks := &server.Hooks{}
	hooks.AddAfterInitialize(func(ctx context.Context, id any, message *mcp.InitializeRequest, result *mcp.InitializeResult) {
		sess := server.ClientSessionFromContext(ctx)
		if sess == nil {
			return
		}
		asserted := ""
		if message != nil && message.Params.Capabilities.Experimental != nil {
			if value, ok := message.Params.Capabilities.Experimental["user_id"].(string); ok {
				asserted = value
			}
		}
		auth.BindSessionUserID(ctx, asserted, sessionStore, sess.SessionID())
	})

	mcpServer := server.NewMCPServer(
		"test-auth",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithHooks(hooks),
	)

	port := freePort(t)
	srv := transport.NewMCPServer(mcpServer, transport.MCPConfig{
		Host:           "127.0.0.1",
		Port:           port,
		ServerName:     "test-auth",
		Version:        "1.0.0",
		RateLimitRPS:   100,
		RateLimitBurst: 50,
		Auth:           keyring,
	})

	go func() {
		_ = srv.Start()
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealth(t, baseURL)

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			t.Logf("shutdown returned an error: %v", err)
		}
	})

	return sessionStore, baseURL
}

func waitForHealth(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server at %s never became healthy", baseURL)
}

// initializeBody builds an MCP initialize request. assertedUser, when set, is
// sent in capabilities.experimental.user_id — the client-asserted identity.
func initializeBody(assertedUser string) []byte {
	capabilities := map[string]interface{}{}
	if assertedUser != "" {
		capabilities["experimental"] = map[string]string{"user_id": assertedUser}
	}

	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    capabilities,
			"clientInfo":      map[string]string{"name": "test-client", "version": "1.0.0"},
		},
	})
	return body
}

func postInitialize(t *testing.T, baseURL, token, assertedUser string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", bytes.NewReader(initializeBody(assertedUser)))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func TestMCPEndpoint_RejectsRequestsWithoutToken(t *testing.T) {
	_, baseURL := startAuthServer(t, buildKeyring(t, map[string]string{
		"amphora": "amphora-token-abcdef",
	}))

	// /health must stay open: the container healthcheck calls it unauthenticated.
	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health: expected 200, got %d", resp.StatusCode)
	}

	for name, token := range map[string]string{
		"no token":    "",
		"wrong token": "totally-wrong-token",
	} {
		t.Run(name, func(t *testing.T) {
			resp := postInitialize(t, baseURL, token, "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("/mcp initialize: expected 401, got %d", resp.StatusCode)
			}
			if resp.Header.Get("Mcp-Session-Id") != "" {
				t.Error("a rejected request must not create a session")
			}
		})
	}
}

// The user_id bound to a session comes from the verified token, never from the
// value the client asserts in capabilities.experimental — and isolation is
// asserted against the real session store.
func TestMCPEndpoint_BindsSessionToTokenIdentity(t *testing.T) {
	sessionStore, baseURL := startAuthServer(t, buildKeyring(t, map[string]string{
		"amphora": "amphora-token-abcdef",
		"bob":     "bob-token-1234567890",
	}))

	tests := []struct {
		name         string
		token        string
		assertedUser string
		wantUser     string
	}{
		{name: "token identity is used", token: "amphora-token-abcdef", wantUser: "amphora"},
		{name: "client-asserted identity is ignored", token: "amphora-token-abcdef", assertedUser: "mallory", wantUser: "amphora"},
		{name: "second user stays isolated", token: "bob-token-1234567890", assertedUser: "amphora", wantUser: "bob"},
	}

	sessions := make([]string, 0, len(tests))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := postInitialize(t, baseURL, tc.token, tc.assertedUser)
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
				t.Fatalf("initialize: expected 200/202, got %d", resp.StatusCode)
			}

			sessionID := resp.Header.Get("Mcp-Session-Id")
			if sessionID == "" {
				t.Fatal("initialize response carried no Mcp-Session-Id")
			}
			for _, seen := range sessions {
				if seen == sessionID {
					t.Fatalf("session id %q was reused across users", sessionID)
				}
			}
			sessions = append(sessions, sessionID)

			got, ok := sessionStore.Get(sessionID)
			if !ok {
				t.Fatalf("session %q was not bound to any user", sessionID)
			}
			if got != tc.wantUser {
				t.Errorf("expected user_id %q, got %q", tc.wantUser, got)
			}
		})
	}
}
