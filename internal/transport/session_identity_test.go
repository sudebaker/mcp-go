package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sudebaker/mcp-go/internal/auth"
	"github.com/sudebaker/mcp-go/internal/session"
)

// These tests are the KB-isolation invariant end to end: the identity bound to
// a session at initialize must still be resolvable from a later tools/call on
// the same session, and a second user's session must never resolve to the first
// user's id. They exist because the mcp-go bump (0.43.2 → 1.2.0) changes how
// sessions are generated and reused, and the unit tests only assert the binding
// written by the after-initialize hook — not that the session id survives the
// following request.
//
// The executor resolves the KB namespace with exactly this lookup
// (internal/executor/subprocess.go): ClientSessionFromContext(ctx).SessionID()
// → sessionStore.Get. The "whoami" tool below does the same thing, so the
// assertion covers the real rule and not a copy of it.

// identityClient bounds every probe: http.DefaultClient has no timeout, so a
// wedged server would hang the whole package instead of failing one test.
var identityClient = &http.Client{Timeout: 10 * time.Second}

type identityHarness struct {
	store *session.Store
	url   string
	ts    *httptest.Server
}

func newIdentityHarness(t *testing.T, tokens map[string]string) *identityHarness {
	t.Helper()

	env := map[string]string{"MCP_AUTH_MODE": auth.ModeRequired}
	entries := make([]auth.Entry, 0, len(tokens))
	for user, token := range tokens {
		name := "KEY_" + strings.ToUpper(user)
		env[name] = token
		entries = append(entries, auth.Entry{UserID: user, KeyEnv: name})
	}
	keyring, err := auth.BuildKeyring(func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}, entries)
	if err != nil {
		t.Fatalf("failed to build keyring: %v", err)
	}

	store := session.New()

	hooks := &server.Hooks{}
	hooks.AddAfterInitialize(func(ctx context.Context, id any, _ *mcp.InitializeRequest, _ *mcp.InitializeResult) {
		sess := server.ClientSessionFromContext(ctx)
		if sess == nil {
			return
		}
		auth.BindSessionUserID(ctx, "", store, sess.SessionID())
	})

	mcpServer := server.NewMCPServer(
		"test-identity",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithHooks(hooks),
	)

	whoami := mcp.NewTool("whoami",
		mcp.WithDescription("returns the user_id bound to the calling session"),
		mcp.WithString("probe", mcp.Description("ignored")),
	)
	mcpServer.AddTool(whoami, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userID := ""
		if sess := server.ClientSessionFromContext(ctx); sess != nil {
			if uid, ok := store.Get(sess.SessionID()); ok {
				userID = uid
			}
		}
		return mcp.NewToolResultText("user_id=" + userID), nil
	})

	srv := NewMCPServer(mcpServer, MCPConfig{
		Host:       "127.0.0.1",
		Port:       0,
		ServerName: "test-identity",
		Version:    "1.0.0",
		Auth:       keyring,
	})

	// Handler() returns the bare Streamable HTTP handler; the production mux
	// wraps it with body limit → CORS → auth → rate limit, so the test must go
	// through the same chain to exercise the identity bound by the keyring.
	ts := httptest.NewServer(srv.mcpEndpointMiddleware(srv.streamServer))
	t.Cleanup(ts.Close)

	return &identityHarness{store: store, url: ts.URL, ts: ts}
}

// rpc posts one JSON-RPC message and returns the status code, the response
// headers and the payload of the last data frame carrying an id (the endpoint
// answers either plain JSON or an SSE stream).
//
// It returns values instead of the *http.Response on purpose: the body is read
// and closed here, so handing the response back would invite a second Close at
// the call site — a pattern that silences bodyclose while hiding who owns the
// resource.
func (h *identityHarness) rpc(t *testing.T, token, sessionID string, payload map[string]interface{}) (int, http.Header, map[string]interface{}) {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.url+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := identityClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	var parsed map[string]interface{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var frame map[string]interface{}
		if err := json.Unmarshal([]byte(candidate), &frame); err == nil {
			if _, ok := frame["id"]; ok {
				parsed = frame
			}
		}
	}
	if parsed == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &parsed)
	}
	return resp.StatusCode, resp.Header, parsed
}

func (h *identityHarness) initialize(t *testing.T, token string) string {
	t.Helper()
	status, header, _ := h.rpc(t, token, "", map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]string{"name": "identity-test", "version": "1.0.0"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("initialize returned %d", status)
	}
	sessionID := header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatalf("initialize response carried no Mcp-Session-Id header (headers: %v)", header)
	}
	return sessionID
}

func (h *identityHarness) whoami(t *testing.T, token, sessionID string) string {
	t.Helper()
	status, _, payload := h.rpc(t, token, sessionID, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      "whoami",
			"arguments": map[string]interface{}{"probe": "x"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("tools/call returned %d", status)
	}
	if payload == nil {
		t.Fatalf("tools/call returned no JSON-RPC frame")
	}
	if rpcErr, ok := payload["error"]; ok {
		t.Fatalf("tools/call returned a JSON-RPC error: %v", rpcErr)
	}
	result, ok := payload["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("tools/call result is not an object: %v", payload["result"])
	}
	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("tools/call result carried no content: %v", result)
	}
	first, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatalf("content entry is not an object: %v", content[0])
	}
	text, _ := first["text"].(string)
	return strings.TrimPrefix(text, "user_id=")
}

// TestSessionIdentity_SurvivesAcrossRequests is the regression guard for the
// mcp-go 1.2.0 bump: the id minted at initialize must still resolve to the
// same authenticated user on a following request in the same session.
func TestSessionIdentity_SurvivesAcrossRequests(t *testing.T) {
	h := newIdentityHarness(t, map[string]string{
		"alice": "alice-token-123456",
		"bob":   "bob-token-654321",
	})

	aliceSession := h.initialize(t, "alice-token-123456")
	if got := h.whoami(t, "alice-token-123456", aliceSession); got != "alice" {
		t.Fatalf("first call on alice's session resolved user_id %q, want %q", got, "alice")
	}
	if got := h.whoami(t, "alice-token-123456", aliceSession); got != "alice" {
		t.Fatalf("second call on alice's session resolved user_id %q, want %q", got, "alice")
	}

	bobSession := h.initialize(t, "bob-token-654321")
	if bobSession == aliceSession {
		t.Fatalf("two initializes minted the same session id %q", bobSession)
	}
	if got := h.whoami(t, "bob-token-654321", bobSession); got != "bob" {
		t.Fatalf("call on bob's session resolved user_id %q, want %q", got, "bob")
	}
}

// TestSessionIdentity_ModernProtocolStillCarriesSession pins the reason KB
// isolation survives a client that asks for a post-2025 protocol version: the
// server negotiates 2025-11-25 and still mints a session id, so the identity
// binding keeps its carrier. A future bump that starts negotiating the
// session-less "modern" era fails here — and must fix the isolation path before
// merging, because tools would then run with an empty user_id.
func TestSessionIdentity_ModernProtocolStillCarriesSession(t *testing.T) {
	h := newIdentityHarness(t, map[string]string{"alice": "alice-token-123456"})

	status, header, payload := h.rpc(t, "alice-token-123456", "", map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2026-07-28",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]string{"name": "identity-test", "version": "1.0.0"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("initialize returned %d", status)
	}
	if negotiated, _ := payload["result"].(map[string]interface{}); negotiated != nil {
		version, _ := negotiated["protocolVersion"].(string)
		if version == "2026-07-28" {
			t.Fatalf("server negotiated the session-less modern era (%q): the session id that carries the KB identity is gone", version)
		}
	}
	sessionID := header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatalf("initialize for protocol 2026-07-28 minted no session id: the KB identity has no carrier")
	}
	if got := h.whoami(t, "alice-token-123456", sessionID); got != "alice" {
		t.Fatalf("tools/call on the negotiated session resolved user_id %q, want %q", got, "alice")
	}
}

// TestSessionIdentity_NoIdentityWithoutToken documents the closed default:
// with auth required, an unauthenticated call never reaches a tool, so no
// namespace can be selected without a token.
func TestSessionIdentity_NoIdentityWithoutToken(t *testing.T) {
	h := newIdentityHarness(t, map[string]string{"alice": "alice-token-123456"})

	status, header, _ := h.rpc(t, "", "", map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]string{"name": "identity-test", "version": "1.0.0"},
		},
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("initialize without a token returned %d, want 401", status)
	}
	if got := header.Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("unauthenticated initialize minted a session id %q", got)
	}
	if n := len(h.store.GetAll()); n != 0 {
		t.Fatalf("unauthenticated initialize wrote %d session bindings", n)
	}
}
