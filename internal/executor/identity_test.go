package executor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sudebaker/mcp-go/internal/auth"
	"github.com/sudebaker/mcp-go/internal/config"
	mcptypes "github.com/sudebaker/mcp-go/internal/mcp"
	"github.com/sudebaker/mcp-go/internal/tracing"
)

const testToken = "token-alice-1234567890"

// authenticatedContext runs a request through the real keyring middleware and
// returns the context a tool handler would see.
func authenticatedContext(t *testing.T, token string) context.Context {
	t.Helper()

	env := map[string]string{"MCP_AUTH_MODE": auth.ModeRequired, "KEY_ALICE": testToken}
	k, err := auth.BuildKeyring(
		func(key string) (string, bool) { v, ok := env[key]; return v, ok },
		[]auth.Entry{{UserID: "alice", KeyEnv: "KEY_ALICE"}},
	)
	if err != nil {
		t.Fatalf("failed to build keyring: %v", err)
	}

	var got context.Context
	h := auth.KeyringAuth(k, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Context()
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got == nil {
		t.Fatal("authenticated request never reached the handler")
	}
	return got
}

// testSession is a minimal server.ClientSession so a session can be planted in
// a context without starting a transport.
type testSession struct{ id string }

func (s testSession) Initialize()       {}
func (s testSession) Initialized() bool { return true }
func (s testSession) SessionID() string { return s.id }
func (s testSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	return make(chan mcp.JSONRPCNotification)
}

type fakeStore map[string]string

func (s fakeStore) Get(sessionID string) (string, bool) {
	userID, ok := s[sessionID]
	return userID, ok
}

// sessionContext plants a client session in ctx the same way the transports do
// (server.WithContext is the mcp-go helper they call).
func sessionContext(ctx context.Context, sessionID string) context.Context {
	srv := server.NewMCPServer("test-server", "1.0.0")
	return srv.WithContext(ctx, testSession{id: sessionID})
}

// The verified token wins over the session binding: a session id minted for
// one user must not decide which account another user's token runs as.
func TestResolveUserID_PrefersTokenOverSessionStore(t *testing.T) {
	ctx := sessionContext(authenticatedContext(t, testToken), "sess-alice")

	if got := ResolveUserID(ctx, fakeStore{"sess-alice": "mallory"}); got != "alice" {
		t.Errorf("expected token identity alice, got %q", got)
	}
}

// The session store is still the fallback when the context carries no
// identity (legacy transports, MCP_AUTH_MODE=off).
func TestResolveUserID_FallsBackToSessionStore(t *testing.T) {
	ctx := sessionContext(context.Background(), "sess-alice")

	if got := ResolveUserID(ctx, fakeStore{"sess-alice": "alice"}); got != "alice" {
		t.Errorf("expected session fallback alice, got %q", got)
	}
}

// A modern stateless request never initializes, so it has no session at all.
// Without a token identity there is nothing to resolve and the caller must not
// invent one.
func TestResolveUserID_StatelessRequestWithoutTokenIsEmpty(t *testing.T) {
	if got := ResolveUserID(context.Background(), fakeStore{"sess-alice": "alice"}); got != "" {
		t.Errorf("expected empty identity for a sessionless unauthenticated request, got %q", got)
	}
}

// Tools that are not user-scoped never resolve an identity and never fail
// closed, whatever the auth mode.
func TestResolveToolIdentity_NonScopedToolIsUnaffected(t *testing.T) {
	userID, err := resolveToolIdentity(context.Background(), nil, "web_search", true)
	if err != nil {
		t.Fatalf("non-scoped tool must not fail closed: %v", err)
	}
	if userID != "" {
		t.Errorf("expected no identity for a non-scoped tool, got %q", userID)
	}
}

// Reproduces review #78 at the executor boundary: a user-scoped tool invoked
// without a resolved identity must not run when auth is enforced. This is the
// stateless path — no session, no initialize.
func TestKBExecute_FailsClosedWithoutIdentity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 1
	cfg.Execution.DefaultTimeout = 5 * time.Second
	cfg.Execution.WorkingDir = t.TempDir()
	cfg.Tools = []config.ToolConfig{{
		Name:    "kb_search",
		Command: "/bin/sh",
		Args:    []string{"-c", "cat > " + filepath.Join(t.TempDir(), "spawned")},
	}}

	exec := NewWithTracer(cfg, tracing.NoOpTracer())
	exec.SetRequireUserIdentity(true)

	_, err := exec.Execute(context.Background(), "kb_search", map[string]interface{}{"query": "x"})
	if !errors.Is(err, ErrIdentityRequired) {
		t.Fatalf("expected ErrIdentityRequired, got %v", err)
	}
}

// The pooled KB path (the one used in production for kb_ingest/kb_search) is
// guarded too, and refuses before a process is acquired or spawned.
func TestPooledKBExecute_FailsClosedWithoutIdentity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 1
	cfg.Tools = []config.ToolConfig{{Name: "kb_search", Command: "python3"}}

	exec := NewWithTracerSessionStoreAndPool(cfg, tracing.NoOpTracer(), nil, 1)
	defer exec.Close()
	exec.SetRequireUserIdentity(true)

	_, err := exec.Execute(context.Background(), "kb_search", map[string]interface{}{"query": "x"})
	if !errors.Is(err, ErrIdentityRequired) {
		t.Fatalf("expected ErrIdentityRequired, got %v", err)
	}

	exec.pool.mu.Lock()
	defer exec.pool.mu.Unlock()
	if n := len(exec.pool.pool["kb_search"]); n != 0 {
		t.Errorf("expected no process to be spawned, found %d slots", n)
	}
}

// Causal test for review #78: a stateless tool call that carries a valid
// bearer token — and no MCP session whatsoever — must reach the subprocess with
// the token's user_id.
func TestKBExecute_StatelessRequestPassesTokenIdentity(t *testing.T) {
	dir := t.TempDir()
	captured := filepath.Join(dir, "request.json")

	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 1
	cfg.Execution.DefaultTimeout = 5 * time.Second
	cfg.Execution.WorkingDir = dir
	cfg.Tools = []config.ToolConfig{{
		Name:    "kb_search",
		Command: "/bin/sh",
		Args:    []string{"-c", "cat > " + captured},
	}}

	exec := NewWithTracer(cfg, tracing.NoOpTracer())
	exec.SetRequireUserIdentity(true)

	// No session in the context: this is exactly the modern stateless shape.
	ctx := authenticatedContext(t, testToken)
	if _, err := exec.Execute(ctx, "kb_search", map[string]interface{}{"query": "x"}); err != nil {
		t.Fatalf("execute: %v", err)
	}

	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("subprocess did not receive a request: %v", err)
	}

	var req mcptypes.SubprocessRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("subprocess request is not valid JSON: %v (%s)", err, raw)
	}
	if req.ToolName != "kb_search" {
		t.Errorf("expected tool kb_search, got %q", req.ToolName)
	}
	if req.Context.UserID != "alice" {
		t.Errorf("expected user_id alice from the bearer token, got %q", req.Context.UserID)
	}
}
