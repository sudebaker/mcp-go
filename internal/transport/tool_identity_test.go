package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sudebaker/mcp-go/internal/auth"
)

// postMCP sends one JSON-RPC message through the same middleware chain the
// server uses for /mcp.
func postMCP(t *testing.T, h http.Handler, token, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// postMCPModern sends a stateless 2026-07-28 request: no initialize, no
// session id, the modern routing headers instead.
func postMCPModern(t *testing.T, h http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "probe")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Reproduces review #78 as a regression gate: a tools/call in the stateless
// 2026-07-28 era — no initialize, therefore no session binding — must either be
// refused by the transport or run as the token's user. What it must never do is
// execute with an empty user_id, which the KB tools turn into a shared
// "anonymous" namespace.
//
// Against mcp-go 0.43.2 the request is refused; after the 1.2.0 bump the same
// test asserts the identity still comes from the token.
func TestModernStatelessToolCallNeverRunsWithoutIdentity(t *testing.T) {
	const token = "test-token-abcdef"

	var gotUser string
	var ran bool
	mcpServer := server.NewMCPServer("probe-server", "1.0.0", server.WithToolCapabilities(true))
	mcpServer.AddTool(mcp.NewTool("probe"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ran = true
		gotUser = auth.UserIDFromContext(ctx)
		return mcp.NewToolResultText("ok"), nil
	})

	s := NewMCPServer(mcpServer, MCPConfig{
		Host:       "127.0.0.1",
		Port:       0,
		ServerName: "probe-server",
		Version:    "1.0.0",
		Auth:       chainTestKeyring(t, auth.ModeRequired, token),
	})
	h := s.mcpEndpointMiddleware(s.streamServer)

	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"probe","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`

	rec := postMCPModern(t, h, token, body)
	t.Logf("stateless tools/call: status=%d executed=%v user_id=%q", rec.Code, ran, gotUser)
	if rec.Code == http.StatusOK {
		if !ran {
			t.Fatalf("200 without the tool running is an unexpected shape: %s", rec.Body.String())
		}
		if gotUser != "admin" {
			t.Fatalf("stateless call executed with user_id %q, want admin (review #78)", gotUser)
		}
	} else {
		t.Logf("transport refused the stateless request with %d (no identity leak): %s", rec.Code, strings.TrimSpace(rec.Body.String()))
		if ran {
			t.Fatalf("tool ran despite status %d", rec.Code)
		}
	}

	// Same request without a token: 401, and the tool never runs.
	ran = false
	rec = postMCPModern(t, h, "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stateless call without token: expected 401, got %d", rec.Code)
	}
	if ran {
		t.Error("stateless call ran without a token")
	}
}

// The tool handler must see the identity the bearer token resolved to, with no
// help from the session store. This is the property the stateless protocol era
// (2026-07-28) depends on: those requests never initialize, so no session
// binding exists, but the request context still carries the identity.
func TestToolCallCarriesTokenIdentityIntoHandlerContext(t *testing.T) {
	const token = "test-token-abcdef"

	var gotUser string
	mcpServer := server.NewMCPServer("probe-server", "1.0.0", server.WithToolCapabilities(true))
	mcpServer.AddTool(mcp.NewTool("probe"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		gotUser = auth.UserIDFromContext(ctx)
		return mcp.NewToolResultText("ok"), nil
	})

	s := NewMCPServer(mcpServer, MCPConfig{
		Host:       "127.0.0.1",
		Port:       0,
		ServerName: "probe-server",
		Version:    "1.0.0",
		Auth:       chainTestKeyring(t, auth.ModeRequired, token),
	})
	h := s.mcpEndpointMiddleware(s.streamServer)

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	rec := postMCP(t, h, token, "", initBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	sessionID := rec.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize did not return an Mcp-Session-Id")
	}

	postMCP(t, h, token, sessionID, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	callBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"probe","arguments":{}}}`
	rec = postMCP(t, h, token, sessionID, callBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotUser != "admin" {
		t.Errorf("tool handler context carried user_id %q, want %q", gotUser, "admin")
	}

	// Without a token nothing reaches the tool at all.
	gotUser = ""
	rec = postMCP(t, h, "", sessionID, callBody)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tools/call without token: expected 401, got %d", rec.Code)
	}
	if gotUser != "" {
		t.Errorf("tool handler ran without a token: user_id %q", gotUser)
	}
}
