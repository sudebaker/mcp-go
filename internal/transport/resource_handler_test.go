package transport

import (
	"bytes"
	"context"
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

func TestHandleInternalResource(t *testing.T) {
	store := session.New()
	store.Set("sess1", "user-abc")
	storage := newFakeStorage()
	storage.Put(t.Context(), "users", "user-abc/file.txt", strings.NewReader("hello"), 5, "text/plain")
	mgr := resources.NewResourceManager(storage, store)
	res, err := mgr.PutForUser(t.Context(), "sess1", "file.txt", strings.NewReader("hello"), 5, "text/plain")
	if err != nil {
		t.Fatalf("PutForUser failed: %v", err)
	}

	s := &MCPServer{resourceManager: mgr}
	req := httptest.NewRequest(http.MethodGet, "/internal/resource/"+strings.TrimPrefix(res.URI, "res://"), nil)
	rec := httptest.NewRecorder()
	s.handleInternalResource(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if string(body) != "hello" {
		t.Fatalf("expected hello, got %s", body)
	}
	if got := rec.Result().Header.Get("X-Resource-Name"); got != "file.txt" {
		t.Fatalf("expected X-Resource-Name file.txt, got %s", got)
	}
}
