package resources

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type fakeStorage struct {
	objects map[string]ObjectInfo
	data    map[string][]byte
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{
		objects: make(map[string]ObjectInfo),
		data:    make(map[string][]byte),
	}
}

func objectKey(bucket, key string) string {
	return bucket + "/" + key
}

func (s *fakeStorage) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) (ObjectInfo, error) {
	if bucket == "" || key == "" {
		return ObjectInfo{}, errors.New("bucket and key are required")
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return ObjectInfo{}, err
	}

	info := ObjectInfo{
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

func (s *fakeStorage) Stat(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	fullKey := objectKey(bucket, key)
	info, ok := s.objects[fullKey]
	if !ok {
		return ObjectInfo{}, errors.New("not found")
	}
	return info, nil
}

func (s *fakeStorage) List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
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

func TestFakeStorage_PutAndOpen(t *testing.T) {
	ctx := context.Background()
	store := newFakeStorage()

	content := "hello, fake storage"
	info, err := store.Put(ctx, "default", "test.txt", strings.NewReader(content), int64(len(content)), "text/plain")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if info.Key != "test.txt" {
		t.Errorf("Key = %q, want %q", info.Key, "test.txt")
	}
	if info.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", info.Size, len(content))
	}
	if info.ContentType != "text/plain" {
		t.Errorf("ContentType = %q, want %q", info.ContentType, "text/plain")
	}

	reader, err := store.Open(ctx, "default", "test.txt")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(got) != content {
		t.Errorf("content = %q, want %q", string(got), content)
	}
}

func TestFakeStorage_OpenNotFound(t *testing.T) {
	ctx := context.Background()
	store := newFakeStorage()

	_, err := store.Open(ctx, "default", "missing.txt")
	if err == nil {
		t.Fatal("expected error opening missing object, got nil")
	}
}

var _ Storage = (*fakeStorage)(nil)
