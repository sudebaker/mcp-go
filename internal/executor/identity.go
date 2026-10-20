package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/server"

	"github.com/sudebaker/mcp-go/internal/auth"
)

// ErrIdentityRequired reports that a user-scoped tool was invoked on a request
// that carried no authenticated identity while the server enforces auth.
//
// It is returned instead of running the tool: an empty user_id reaches the
// Python side as "anonymous", which is a namespace shared by every caller.
var ErrIdentityRequired = errors.New("tool requires an authenticated identity")

// identityScopedTools lists the tools whose data is partitioned by user_id.
// Their subprocess MUST receive a non-empty user_id; see
// tools/knowledge_base, which falls back to a shared "anonymous" namespace
// when the field is missing.
var identityScopedTools = map[string]struct{}{
	"kb_ingest": {},
	"kb_search": {},
}

// requiresIdentity reports whether a tool call is scoped to a single user.
func requiresIdentity(toolName string) bool {
	_, ok := identityScopedTools[toolName]
	return ok
}

// ResolveUserID returns the user_id a tool call must run as.
//
// The identity verified by the transport middleware (auth.UserIDFromContext)
// is authoritative. It is present on every authenticated request, including
// modern stateless requests (protocol 2026-07-28), which never run initialize
// and therefore have no MCP session to look up.
//
// The session store is only a fallback for requests whose context carries no
// identity. It must never override the token: a session id minted for one user
// could otherwise be replayed with another user's token, and a tool would run
// against the first user's data.
//
// An empty return means "no authenticated identity" — callers must not
// substitute a client-supplied value.
func ResolveUserID(ctx context.Context, store interface {
	Get(sessionID string) (string, bool)
}) string {
	if userID := auth.UserIDFromContext(ctx); userID != "" {
		return userID
	}

	if store != nil {
		if session := server.ClientSessionFromContext(ctx); session != nil {
			if userID, ok := store.Get(session.SessionID()); ok {
				return userID
			}
		}
	}

	return ""
}

// resolveToolIdentity resolves the identity for a tool call and enforces the
// fail-closed rule for user-scoped tools.
//
// When required is true (MCP_AUTH_MODE=required) a user-scoped tool invoked
// without a resolved identity is rejected with ErrIdentityRequired and never
// spawned. Tools that are not user-scoped resolve to "" and are unaffected.
func resolveToolIdentity(ctx context.Context, store interface {
	Get(sessionID string) (string, bool)
}, toolName string, required bool) (string, error) {
	if !requiresIdentity(toolName) {
		return "", nil
	}

	userID := ResolveUserID(ctx, store)
	if userID == "" && required {
		return "", fmt.Errorf("%w: tool %q", ErrIdentityRequired, toolName)
	}

	return userID, nil
}
