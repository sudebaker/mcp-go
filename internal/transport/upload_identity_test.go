package transport

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/sudebaker/mcp-go/internal/auth"
	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/resources"
	"github.com/sudebaker/mcp-go/internal/session"
)

// The namespace a file lands in is decided by the session store lookup of
// X-Session-ID, so the handler must verify that the session belongs to the
// identity that authenticated the request. Otherwise any keyring holder can
// write into another user's namespace by naming their session id.

func newUploadServerWithKeyring(t *testing.T, env map[string]string, entries []auth.Entry, store *session.Store) (*MCPServer, *fakeStorage) {
	t.Helper()

	keyring, err := auth.BuildKeyring(func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}, entries)
	if err != nil {
		t.Fatalf("failed to build keyring: %v", err)
	}

	storage := newFakeStorage()
	manager := resources.NewResourceManager(storage, store)

	srv := NewMCPServer(server.NewMCPServer("test", "1.0.0"), MCPConfig{
		Host: "127.0.0.1",
		Port: 0,
		Auth: keyring,
		Upload: config.UploadConfig{
			Enabled:      true,
			MaxSizeMB:    50,
			AllowedTypes: []string{"image/png", "text/plain"},
		},
	})
	srv.SetResourceManager(manager)

	return srv, storage
}

func makeAuthedUploadRequest(t *testing.T, srv *MCPServer, token, sessionID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="file"; filename="report.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if sessionID != "" {
		req.Header.Set("X-Session-ID", sessionID)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	srv.authMiddleware(srv.handleUpload).ServeHTTP(rec, req)
	return rec
}

func storedKeys(storage *fakeStorage) []string {
	keys := make([]string, 0, len(storage.objects))
	for key := range storage.objects {
		keys = append(keys, key)
	}
	return keys
}

func TestUpload_RejectsCrossTenantSessionID(t *testing.T) {
	env := map[string]string{
		"MCP_AUTH_MODE": auth.ModeRequired,
		"KEY_ALICE":     "alice-token-1234567890",
		"KEY_BOB":       "bob-token-0987654321",
	}
	store := session.New()
	store.Set("session-bob", "bob") // bob's initialize

	srv, storage := newUploadServerWithKeyring(t, env, []auth.Entry{
		{UserID: "alice", KeyEnv: "KEY_ALICE"},
		{UserID: "bob", KeyEnv: "KEY_BOB"},
	}, store)

	// alice names bob's session: rejected, and nothing is written.
	rec := makeAuthedUploadRequest(t, srv, "alice-token-1234567890", "session-bob", validPNG())
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant upload: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(storage.objects) != 0 {
		t.Errorf("rejected upload wrote objects: %v", storedKeys(storage))
	}

	// bob names his own session: accepted, into his own namespace.
	rec = makeAuthedUploadRequest(t, srv, "bob-token-0987654321", "session-bob", validPNG())
	if rec.Code != http.StatusOK {
		t.Fatalf("owner upload: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(storage.objects) != 1 {
		t.Errorf("expected exactly 1 object, got %v", storedKeys(storage))
	}
	for key := range storage.objects {
		if !strings.Contains(key, "users/bob/") {
			t.Errorf("object landed outside the owner namespace: %s", key)
		}
	}
}

// An authenticated caller naming a session that does not exist gets 403, not a
// lookup that silently creates a namespace.
func TestUpload_RejectsUnknownSessionForAuthenticatedCaller(t *testing.T) {
	env := map[string]string{"MCP_AUTH_MODE": auth.ModeRequired, "KEY_BOB": "bob-token-0987654321"}
	srv, storage := newUploadServerWithKeyring(t, env, []auth.Entry{{UserID: "bob", KeyEnv: "KEY_BOB"}}, session.New())

	rec := makeAuthedUploadRequest(t, srv, "bob-token-0987654321", "session-nobody", validPNG())
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(storage.objects) != 0 {
		t.Errorf("rejected upload wrote objects: %v", storedKeys(storage))
	}
}

// Tokens are still required before any of this: no token, no upload.
func TestUpload_StillRequiresAToken(t *testing.T) {
	env := map[string]string{"MCP_AUTH_MODE": auth.ModeRequired, "KEY_BOB": "bob-token-0987654321"}
	store := session.New()
	store.Set("session-bob", "bob")
	srv, storage := newUploadServerWithKeyring(t, env, []auth.Entry{{UserID: "bob", KeyEnv: "KEY_BOB"}}, store)

	rec := makeAuthedUploadRequest(t, srv, "", "session-bob", validPNG())
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if len(storage.objects) != 0 {
		t.Errorf("unauthenticated upload wrote objects: %v", storedKeys(storage))
	}
}

// MCP_AUTH_MODE=off has no identity to compare against: the header is trusted,
// exactly as before this change. Documented as local-development only.
func TestUpload_ModeOffTrustsSessionHeader(t *testing.T) {
	store := session.New()
	store.Set("session-bob", "bob")

	storage := newFakeStorage()
	manager := resources.NewResourceManager(storage, store)
	srv := NewMCPServer(server.NewMCPServer("test", "1.0.0"), MCPConfig{
		Host: "127.0.0.1",
		Port: 0,
		Auth: auth.NewKeyring(auth.ModeOff, nil),
		Upload: config.UploadConfig{
			Enabled:      true,
			MaxSizeMB:    50,
			AllowedTypes: []string{"image/png"},
		},
	})
	srv.SetResourceManager(manager)

	rec := makeAuthedUploadRequest(t, srv, "", "session-bob", validPNG())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 in off mode, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(storage.objects) != 1 {
		t.Errorf("expected 1 object, got %v", storedKeys(storage))
	}
}
