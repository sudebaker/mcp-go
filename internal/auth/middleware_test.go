package auth

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuth_ValidToken(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	called := false
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if GetRequestID(r.Context()) == "" {
			t.Error("expected request_id in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-key-1234")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler was not called")
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header")
	}
}

func TestBearerAuth_InvalidToken(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-key-xxxx")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_TokenTooShort(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer short")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_TokenTooLong(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	token := make([]byte, 300)
	for i := range token {
		token[i] = 'a'
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+string(token))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_TokenNullByte(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-key\x001234")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_NoAuthHeader(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_WrongScheme(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic dGVzdDp0ZXN0")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_OnEmptySkip(t *testing.T) {
	called := false
	handler := BearerAuth([32]byte{}, OnEmptySkip, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestBearerAuth_OnEmpty503(t *testing.T) {
	handler := BearerAuth([32]byte{}, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["error"] != "endpoints disabled: API key not set" {
		t.Errorf("unexpected error message: %s", body["error"])
	}
}

func TestBearerAuth_OnEmpty401(t *testing.T) {
	handler := BearerAuth([32]byte{}, OnEmpty401, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuth_RespectsClientRequestID(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-key-1234")
	req.Header.Set("X-Request-ID", "client-provided-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") != "client-provided-id" {
		t.Errorf("expected client-provided-id, got %s", rec.Header().Get("X-Request-ID"))
	}
}

func TestBearerAuth_GenerateRequestID(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-key-1234")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	rid := rec.Header().Get("X-Request-ID")
	if rid == "" {
		t.Error("expected X-Request-ID to be generated")
	}
}

func TestBearerAuth_InvalidRequestID(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-key-1234")
	req.Header.Set("X-Request-ID", "invalid/id/with/slashes")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	rid := rec.Header().Get("X-Request-ID")
	if rid == "invalid/id/with/slashes" {
		t.Error("expected invalid request_id to be replaced")
	}
	if rid == "" {
		t.Error("expected a valid generated request_id")
	}
}

func TestGetRequestID_Empty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if id := GetRequestID(req.Context()); id != "" {
		t.Errorf("expected empty, got %s", id)
	}
}

func TestGetRequestID_Present(t *testing.T) {
	keyHash := sha256.Sum256([]byte("test-key-1234"))
	var capturedID string
	handler := BearerAuth(keyHash, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = GetRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-key-1234")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if capturedID == "" {
		t.Error("expected request_id to be captured")
	}
}

func TestBearerAuth_ResponseJSONContentType(t *testing.T) {
	handler := BearerAuth([32]byte{}, OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected application/json, got %s", rec.Header().Get("Content-Type"))
	}
}
