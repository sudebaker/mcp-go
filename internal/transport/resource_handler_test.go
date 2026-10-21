package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sudebaker/mcp-go/internal/resources"
	"github.com/sudebaker/mcp-go/internal/session"
)

type fakeStorage struct {
	objects map[string]resources.ObjectInfo
	data    map[string][]byte
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{
		objects: make(map[string]resources.ObjectInfo),
		data:    make(map[string][]byte),
	}
}

func objectKey(bucket, key string) string {
	return bucket + "/" + key
}

func (s *fakeStorage) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) (resources.ObjectInfo, error) {
	if bucket == "" || key == "" {
		return resources.ObjectInfo{}, errors.New("bucket and key are required")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return resources.ObjectInfo{}, err
	}
	info := resources.ObjectInfo{
		Key:          key,
		Size:         int64(len(data)),
		ContentType:  contentType,
		ETag:         "etag-" + key,
		LastModified: time.Now(),
	}
	fullKey := objectKey(bucket, key)
	s.objects[fullKey] = info
	s.data[fullKey] = data
	return info, nil
}

func (s *fakeStorage) Open(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	fullKey := objectKey(bucket, key)
	data, ok := s.data[fullKey]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeStorage) Stat(ctx context.Context, bucket, key string) (resources.ObjectInfo, error) {
	fullKey := objectKey(bucket, key)
	info, ok := s.objects[fullKey]
	if !ok {
		return resources.ObjectInfo{}, errors.New("not found")
	}
	return info, nil
}

func (s *fakeStorage) List(ctx context.Context, bucket, prefix string) ([]resources.ObjectInfo, error) {
	var out []resources.ObjectInfo
	for fullKey, info := range s.objects {
		if strings.HasPrefix(fullKey, bucket+"/") && strings.HasPrefix(info.Key, prefix) {
			out = append(out, info)
		}
	}
	return out, nil
}

func (s *fakeStorage) Delete(ctx context.Context, bucket, key string) error {
	fullKey := objectKey(bucket, key)
	if _, ok := s.objects[fullKey]; !ok {
		return errors.New("not found")
	}
	delete(s.objects, fullKey)
	delete(s.data, fullKey)
	return nil
}

func TestHandleInternalResourceWithBearer(t *testing.T) {
	store := session.New()
	store.Set("sess1", "user-abc")
	storage := newFakeStorage()
	storage.Put(t.Context(), "users", "user-abc/file.txt", strings.NewReader("hello"), 5, "text/plain")
	mgr := resources.NewResourceManager(storage, store)
	res, err := mgr.PutForUser(t.Context(), "sess1", "file.txt", strings.NewReader("hello"), 5, "text/plain")
	if err != nil {
		t.Fatalf("PutForUser failed: %v", err)
	}

	token := "test-only-sandbox-token-0123456789abcdef"
	requestURI := "/internal/resource/" + strings.TrimPrefix(res.URI, "res://")

	// Test cases (boss acceptance criteria for /internal/resource):
	//   1. no Authorization header        -> 401
	//   2. valid Bearer SANDBOX_API_TOKEN -> 200
	//   3. invalid bearer token           -> 401
	//   (fail-closed: unset token config -> 401, covered by case 4)
	cases := []struct {
		name   string
		hash   [32]byte
		bearer string
		want   int
	}{
		{name: "no token", hash: sha256.Sum256([]byte(token)), bearer: "", want: http.StatusUnauthorized},
		{name: "valid token", hash: sha256.Sum256([]byte(token)), bearer: "Bearer " + token, want: http.StatusOK},
		{name: "invalid token", hash: sha256.Sum256([]byte(token)), bearer: "Bearer wrong-value-token-99", want: http.StatusUnauthorized},
		{name: "unconfigured token (fail closed)", hash: [32]byte{}, bearer: "Bearer anything-123", want: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &MCPServer{resourceManager: mgr, internalResourceKeyHash: tc.hash}
			req := httptest.NewRequest(http.MethodGet, requestURI, nil)
			if tc.bearer != "" {
				req.Header.Set("Authorization", tc.bearer)
			}
			rec := httptest.NewRecorder()
			s.internalResourceHandler().ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
			if tc.want == http.StatusOK {
				body, _ := io.ReadAll(rec.Result().Body)
				if string(body) != "hello" {
					t.Fatalf("expected hello, got %s", body)
				}
				if got := rec.Result().Header.Get("X-Resource-Name"); got != "file.txt" {
					t.Fatalf("expected X-Resource-Name file.txt, got %s", got)
				}
			}
		})
	}
}

// TestHandleInternalResourceMultiUseAndExpiry pins the phase-1 token contract
// at the HTTP boundary (docs/API.md, Resource resolution flow): the same
// res:// token answers 200 on repeated GETs while it lives, and 401 once the
// TTL has passed. Re-running the flow (upload once, ask several times about
// the same file) must not re-upload.
func TestHandleInternalResourceMultiUseAndExpiry(t *testing.T) {
	store := session.New()
	store.Set("sess1", "user-abc")
	storage := newFakeStorage()
	mgr := resources.NewResourceManager(storage, store)
	res, err := mgr.PutForUser(t.Context(), "sess1", "file.txt", strings.NewReader("hello"), 5, "text/plain")
	if err != nil {
		t.Fatalf("PutForUser failed: %v", err)
	}

	token := "test-only-sandbox-token-0123456789abcdef"
	hash := sha256.Sum256([]byte(token))
	s := &MCPServer{resourceManager: mgr, internalResourceKeyHash: hash}
	requestURI := "/internal/resource/" + strings.TrimPrefix(res.URI, "res://")

	for i := 1; i <= 3; i++ {
		req := httptest.NewRequest(http.MethodGet, requestURI, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.internalResourceHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET #%d of the same token: expected 200, got %d: %s", i, rec.Code, rec.Body.String())
		}
		body, _ := io.ReadAll(rec.Result().Body)
		if string(body) != "hello" {
			t.Fatalf("GET #%d of the same token: expected hello, got %q", i, body)
		}
	}

	// Past the TTL the GET answers 401 ("Invalid or expired token").
	expiredToken := mgr.Tokens().Issue("users", "user-abc/expired.txt", "user-abc", "sess1", -time.Second)
	expiredReq := httptest.NewRequest(http.MethodGet, "/internal/resource/"+expiredToken, nil)
	expiredReq.Header.Set("Authorization", "Bearer "+token)
	expiredRec := httptest.NewRecorder()
	s.internalResourceHandler().ServeHTTP(expiredRec, expiredReq)
	if expiredRec.Code != http.StatusUnauthorized {
		t.Fatalf("GET of an expired token: expected 401, got %d: %s", expiredRec.Code, expiredRec.Body.String())
	}
}

func TestInternalResourceKeyHashRejectsBadTokens(t *testing.T) {
	var zero [32]byte
	for name, token := range map[string]string{
		"empty":       "   ",
		"badcharset":  "con espacios no",
		"placeholder": "change_me",
	} {
		t.Setenv("SANDBOX_API_TOKEN", token)
		hash, err := internalResourceKeyHash()
		if err == nil {
			t.Errorf("SANDBOX_API_TOKEN %q (%s): expected error, got hash %v", token, name, hash)
		}
		if hash != zero {
			t.Errorf("SANDBOX_API_TOKEN %q (%s): zero hash expected, got %v", token, name, hash)
		}
	}

	valid := "generated-openssl-rand-hex-32-value"
	t.Setenv("SANDBOX_API_TOKEN", valid)
	hash, err := internalResourceKeyHash()
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if hash != sha256.Sum256([]byte(valid)) {
		t.Fatal("hash mismatch for valid token")
	}
}
