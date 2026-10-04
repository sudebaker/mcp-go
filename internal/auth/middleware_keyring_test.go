package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testKeyring(t *testing.T, env map[string]string, entries []Entry) *Keyring {
	t.Helper()
	k, err := BuildKeyring(envFrom(env), entries)
	if err != nil {
		t.Fatalf("failed to build keyring: %v", err)
	}
	return k
}

func TestKeyringAuth_MissingHeaderIs401(t *testing.T) {
	k := testKeyring(t,
		map[string]string{"MCP_AUTH_MODE": ModeRequired, "KEY_AMPHORA": "amphora-token-abcdef"},
		[]Entry{{UserID: "amphora", KeyEnv: "KEY_AMPHORA"}},
	)

	nextCalled := false
	h := KeyringAuth(k, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if nextCalled {
		t.Error("handler behind auth must not run")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected an error message in the body")
	}
}

func TestKeyringAuth_RejectsBadTokens(t *testing.T) {
	k := testKeyring(t,
		map[string]string{"MCP_AUTH_MODE": ModeRequired, "KEY_AMPHORA": "amphora-token-abcdef"},
		[]Entry{{UserID: "amphora", KeyEnv: "KEY_AMPHORA"}},
	)
	h := KeyringAuth(k, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler behind auth must not run")
	}))

	for name, header := range map[string]string{
		"wrong token":       "Bearer not-the-real-token",
		"wrong scheme":      "Basic dXNlcjpwYXNz",
		"empty bearer":      "Bearer ",
		"token too short":   "Bearer short",
		"token with spaces": "Bearer amphora token abcdef",
		"no scheme":         "amphora-token-abcdef",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", header)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", rec.Code)
			}
		})
	}
}

// A client must never be able to claim an identity with a plain header.
func TestKeyringAuth_CannotSpoofIdentityWithHeaders(t *testing.T) {
	k := testKeyring(t,
		map[string]string{"MCP_AUTH_MODE": ModeRequired, "KEY_AMPHORA": "amphora-token-abcdef"},
		[]Entry{{UserID: "amphora", KeyEnv: "KEY_AMPHORA"}},
	)
	h := KeyringAuth(k, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler behind auth must not run")
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-User-Id", "amphora")
	req.Header.Set("X-User-ID", "amphora")
	req.Header.Set("X-Authenticated-User", "amphora")
	req.Header.Set("Mcp-Session-Id", "amphora")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestKeyringAuth_ValidTokenInjectsUserID(t *testing.T) {
	k := testKeyring(t,
		map[string]string{"MCP_AUTH_MODE": ModeRequired, "KEY_AMPHORA": "amphora-token-abcdef"},
		[]Entry{{UserID: "amphora", KeyEnv: "KEY_AMPHORA"}},
	)

	var gotUser, gotRequestID string
	h := KeyringAuth(k, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = UserIDFromContext(r.Context())
		gotRequestID = GetRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer amphora-token-abcdef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUser != "amphora" {
		t.Errorf("expected user_id amphora in context, got %q", gotUser)
	}
	if gotRequestID == "" {
		t.Error("expected a request id in context")
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID response header")
	}
}

// Fail-closed: a required keyring with no usable keys must refuse to serve
// instead of falling through unauthenticated.
func TestKeyringAuth_EmptyRequiredKeyringIs503(t *testing.T) {
	k := NewKeyring(ModeRequired, nil)
	nextCalled := false
	h := KeyringAuth(k, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	for name, header := range map[string]string{"no header": "", "valid-looking": "Bearer amphora-token-abcdef"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("expected 503, got %d", rec.Code)
			}
		})
	}
	if nextCalled {
		t.Error("handler behind a fail-closed auth must not run")
	}
}

func TestKeyringAuth_NilKeyringIs503(t *testing.T) {
	nextCalled := false
	h := KeyringAuth(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
	if nextCalled {
		t.Error("handler behind a fail-closed auth must not run")
	}
}

// MCP_AUTH_MODE=off is the only supported way to disable auth; when it is
// chosen no identity is injected, so no user_id can be derived from a header.
func TestKeyringAuth_ModeOffPassesThroughWithoutIdentity(t *testing.T) {
	k := NewKeyring(ModeOff, nil)
	var gotUser string
	called := false
	h := KeyringAuth(k, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotUser = UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-User-Id", "mallory")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("off mode must pass through, got code %d (called=%v)", rec.Code, called)
	}
	if gotUser != "" {
		t.Errorf("off mode must not inject an identity, got %q", gotUser)
	}
}
