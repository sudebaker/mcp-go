package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sudebaker/mcp-go/internal/session"
)

// authenticatedContext runs a request through KeyringAuth and returns the
// request context a downstream handler would see. Using the real middleware
// (instead of hand-building a context) keeps the test honest.
func authenticatedContext(t *testing.T, k *Keyring, token string) context.Context {
	t.Helper()
	var got context.Context
	h := KeyringAuth(k, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Context()
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got == nil {
		t.Fatalf("request with token %q never reached the handler", token)
	}
	return got
}

// Isolation is asserted against the real session store, not a mock: two tokens
// must produce two independent user_ids.
func TestBindSessionUserID_IsolatesUsersThroughRealStore(t *testing.T) {
	aliceToken := "token-alice-1234567890"
	bobToken := "token-bob-0987654321"

	k := testKeyring(t, map[string]string{
		"MCP_AUTH_MODE": ModeRequired,
		"KEY_ALICE":     aliceToken,
		"KEY_BOB":       bobToken,
	}, []Entry{
		{UserID: "alice", KeyEnv: "KEY_ALICE"},
		{UserID: "bob", KeyEnv: "KEY_BOB"},
	})

	store := session.New()

	userID, ok := BindSessionUserID(authenticatedContext(t, k, aliceToken), "", store, "session-alice")
	if !ok || userID != "alice" {
		t.Fatalf("expected (alice, true), got (%q, %v)", userID, ok)
	}
	userID, ok = BindSessionUserID(authenticatedContext(t, k, bobToken), "", store, "session-bob")
	if !ok || userID != "bob" {
		t.Fatalf("expected (bob, true), got (%q, %v)", userID, ok)
	}

	// session.Store is the same implementation the executor consults when it
	// injects context.user_id into KB tools.
	if got, found := store.Get("session-alice"); !found || got != "alice" {
		t.Errorf("session-alice: expected (alice, true), got (%q, %v)", got, found)
	}
	if got, found := store.Get("session-bob"); !found || got != "bob" {
		t.Errorf("session-bob: expected (bob, true), got (%q, %v)", got, found)
	}
	if got, found := store.Get("session-unknown"); found {
		t.Errorf("unbound session must not resolve, got %q", got)
	}
}

// Regression: capabilities.experimental.user_id is client-asserted and must be
// ignored. Without an authenticated token no identity may be recorded at all.
func TestBindSessionUserID_IgnoresClientAssertedUserID(t *testing.T) {
	store := session.New()

	userID, ok := BindSessionUserID(context.Background(), "mallory", store, "session-x")
	if ok {
		t.Errorf("unauthenticated request must not bind an identity, got %q", userID)
	}
	if userID != "" {
		t.Errorf("expected empty user_id, got %q", userID)
	}
	if got, found := store.Get("session-x"); found {
		t.Errorf("client-asserted user_id leaked into the session store: %q", got)
	}

	// Even with a valid token, the asserted value must never win.
	k := testKeyring(t,
		map[string]string{"MCP_AUTH_MODE": ModeRequired, "KEY_ALICE": "token-alice-1234567890"},
		[]Entry{{UserID: "alice", KeyEnv: "KEY_ALICE"}},
	)
	userID, ok = BindSessionUserID(authenticatedContext(t, k, "token-alice-1234567890"), "mallory", store, "session-y")
	if !ok || userID != "alice" {
		t.Fatalf("expected (alice, true), got (%q, %v)", userID, ok)
	}
	if got, _ := store.Get("session-y"); got != "alice" {
		t.Errorf("expected the token identity to win, got %q", got)
	}
}

// A session that already carries an identity is rewritten only by an
// authenticated request; a client cannot overwrite it by asserting a user_id.
func TestBindSessionUserID_AssertionCannotOverwriteExistingIdentity(t *testing.T) {
	store := session.New()
	store.Set("session-z", "alice")

	if _, ok := BindSessionUserID(context.Background(), "bob", store, "session-z"); ok {
		t.Error("unauthenticated request must not report a bound identity")
	}
	if got, _ := store.Get("session-z"); got != "alice" {
		t.Errorf("stored identity was overwritten: %q", got)
	}
}
