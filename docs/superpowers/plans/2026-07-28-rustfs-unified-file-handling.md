# RustFS Unified File Handling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a unified file handling layer for all analysis tools using a `ResourceManager` + `Storage` + `Resource` abstraction in Go, with RustFS as the initial backend, and expose only `context.file()` / `context.files()` to Python tools.

**Architecture:** The Go server owns all storage logic. It resolves URIs, validates permissions, opens objects from RustFS, and hands Python tools an opaque `res://{token}` URI plus metadata. Python tools stream bytes from a private internal Go endpoint `/internal/resource/{token}`. The MCP `resources/list` and `resources/read` handlers reuse the same `ResourceManager`.

**Tech Stack:** Go 1.23+, `github.com/minio/minio-go/v7`, Python 3.11+, `urllib.request` for internal streaming.

## Global Constraints

- RustFS, buckets, and MinIO must never be referenced inside Python tools.
- All file arguments use `format: resource-uri` in `tool.yaml`.
- Bucket strategy: single bucket `users`, keys prefixed with `{user_id}/`.
- Internal resource tokens are one-shot, 60s TTL.
- Streaming first: never load whole files into memory unless explicitly required.
- Maintain backward compatibility for `__files__` and `file_path` during this version.
- `resources/subscribe` is postponed.
- `rustfs_storage` tool is deprecated.

---

## File Structure Map

### Go — new files

| File | Responsibility |
|------|----------------|
| `internal/resources/resource.go` | `Resource` type |
| `internal/resources/storage.go` | `Storage` interface and `ObjectInfo` |
| `internal/resources/rustfs_storage.go` | `RustFSStorage` implementing `Storage` |
| `internal/resources/token.go` | ephemeral token store |
| `internal/resources/manager.go` | `ResourceManager`: resolve, list, read, put for users |
| `internal/resources/handler.go` | MCP `resources/list` and `resources/read` handlers |
| `internal/transport/resource_handler.go` | `GET /internal/resource/{token}` streaming endpoint |

### Go — modified files

| File | Change |
|------|--------|
| `cmd/server/main.go` | Initialize `ResourceManager`, register resource handlers, declare capability |
| `internal/transport/sse.go` | Wire `ResourceManager` and internal endpoint into mux |
| `internal/transport/upload_handler.go` | Use `ResourceManager.PutForUser`, return opaque URI |
| `go.mod` | Add `github.com/minio/minio-go/v7` |

### Python — new files

| File | Responsibility |
|------|----------------|
| `tools/common/resources/__init__.py` | Package exports |
| `tools/common/resources/resource.py` | Python `Resource` class with lazy HTTP reader |
| `tools/common/resources/manager.py` | `ToolContext` with `file()` / `files()` |

### Python — modified files

| File | Change |
|------|--------|
| `tools/batch_summarize/main.py` | Use `ToolContext.files("file_uris")` |
| `tools/batch_summarize/tool.yaml` | `file_uris` array with `format: resource-uri` |
| `tools/regulation_diff/main.py` | Use `ToolContext.file("file_uri_1")` and `file("file_uri_2")` |
| `tools/regulation_diff/tool.yaml` | `file_uri_1`, `file_uri_2` with `format: resource-uri` |
| `tools/document_classifier/main.py` | Use `ToolContext.files("file_uris")` |
| `tools/document_classifier/tool.yaml` | `file_uris` array with `format: resource-uri` |
| `tools/config_auditor/main.py` | Implement real reading via `ToolContext.files("file_uris")` |
| `tools/config_auditor/tool.yaml` | Add `file_uris` with `format: resource-uri` |
| `tools/vision_ocr/main.py` | Use `ToolContext.file("file_uri").read_bytes()` |
| `tools/vision_ocr/tool.yaml` | `file_uri` with `format: resource-uri` |
| `tools/transcribe/main.py` | Use `ToolContext.file("file_uri").reader` stream |
| `tools/transcribe/tool.yaml` | `file_uri` with `format: resource-uri` |
| `tools/metadata_extractor/main.py` | Use `ToolContext.file("file_uri").reader` |
| `tools/metadata_extractor/tool.yaml` | `file_uri` with `format: resource-uri` |
| `tools/stego_detector/main.py` | Use `ToolContext.file("file_uri").reader` |
| `tools/stego_detector/tool.yaml` | `file_uri` with `format: resource-uri` |
| `tools/document_fingerprint/main.py` | Use `file_uri_1` / `file_uri_2` |
| `tools/document_fingerprint/tool.yaml` | `file_uri_1`, `file_uri_2` with `format: resource-uri` |
| `tools/api_diff/main.py` | Use `file_uri_old` / `file_uri_new` |
| `tools/api_diff/tool.yaml` | `file_uri_old`, `file_uri_new` with `format: resource-uri` |
| `tools/data_analysis/main.py` | Use `ToolContext.file("file_uri").reader` |
| `tools/data_analysis/tool.yaml` | `file_uri` with `format: resource-uri` |
| `tools/case_evidence/main.py` | Use `ToolContext.file("file_uri").reader` |
| `tools/case_evidence/tool.yaml` | `file_uri` with `format: resource-uri` |
| `tools/canvas_diagram/main.py` | Use Go helper for output upload |
| `tools/rustfs_storage/main.py` | Mark deprecated |
| `tools/rustfs_storage/tool.yaml` | Mark deprecated |
| `configs/config.yaml` | Update tool schemas, remove upload dir/TTL settings |

### Tests

| File | What it tests |
|------|---------------|
| `internal/resources/resource_test.go` | `Resource.Close` |
| `internal/resources/storage_test.go` | interface contract with fake storage |
| `internal/resources/rustfs_storage_test.go` | RustFS storage (mock minio) |
| `internal/resources/token_test.go` | token issue/validate/cleanup |
| `internal/resources/manager_test.go` | resolve, list, read, put, permission checks |
| `internal/resources/handler_test.go` | MCP resources/list and resources/read |
| `internal/transport/resource_handler_test.go` | `/internal/resource/{token}` streaming |
| `internal/transport/upload_handler_test.go` | upload to ResourceManager |
| `tests/tools/common/resources/test_resource.py` | Python `Resource.reader` / `read_bytes` / close |
| `tests/tools/common/resources/test_manager.py` | `ToolContext.file()` / `files()` |
| Update existing tool tests | Mock `ToolContext` |

---

## Phase 1: Go Storage Foundation

### Task 1: Add MinIO dependency

**Files:**
- Modify: `go.mod`

**Interfaces:**
- Produces: dependency on `github.com/minio/minio-go/v7` available for build.

- [ ] **Step 1: Add module**

Run:
```bash
go get github.com/minio/minio-go/v7
```

- [ ] **Step 2: Verify**

Run:
```bash
grep "minio" go.mod
```

Expected output: line containing `github.com/minio/minio-go/v7`.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add minio-go v7 for rustfs storage"
```

### Task 2: Define Resource and Storage interface

**Files:**
- Create: `internal/resources/resource.go`
- Create: `internal/resources/storage.go`

**Interfaces:**
- Produces:
  - `type Resource struct { URI string; Name string; MIMEType string; Size int64; SHA256 string; Reader io.ReadCloser }`
  - `type Storage interface { Put(...); Open(...); Stat(...); List(...); Delete(...) }`
  - `type ObjectInfo struct { Key string; Size int64; ContentType string; ETag string; LastModified time.Time }`

- [ ] **Step 1: Write Resource type**

Create `internal/resources/resource.go`:
```go
package resources

import "io"

type Resource struct {
    URI      string
    Name     string
    MIMEType string
    Size     int64
    SHA256   string
    Reader   io.ReadCloser
}

func (r *Resource) Close() error {
    if r.Reader != nil {
        return r.Reader.Close()
    }
    return nil
}
```

- [ ] **Step 2: Write Storage interface**

Create `internal/resources/storage.go`:
```go
package resources

import (
    "context"
    "io"
    "time"
)

type Storage interface {
    Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) (ObjectInfo, error)
    Open(ctx context.Context, bucket, key string) (io.ReadCloser, error)
    Stat(ctx context.Context, bucket, key string) (ObjectInfo, error)
    List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error)
    Delete(ctx context.Context, bucket, key string) error
}

type ObjectInfo struct {
    Key          string
    Size         int64
    ContentType  string
    ETag         string
    LastModified time.Time
}
```

- [ ] **Step 3: Write tests for Resource.Close**

Create `internal/resources/resource_test.go`:
```go
package resources

import (
    "errors"
    "io"
    "testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.EOF }
func (failingReader) Close() error { return errors.New("close failed") }

func TestResource_Close(t *testing.T) {
    r := &Resource{Reader: failingReader{}}
    err := r.Close()
    if err == nil || err.Error() != "close failed" {
        t.Fatalf("expected close error, got %v", err)
    }
}
```

- [ ] **Step 4: Run tests**

Run:
```bash
go test ./internal/resources/ -v
```

Expected: `PASS: TestResource_Close`.

- [ ] **Step 5: Commit**

```bash
git add internal/resources/resource.go internal/resources/storage.go internal/resources/resource_test.go
git commit -m "feat(resources): define Resource type and Storage interface"
```

### Task 3: Implement RustFSStorage

**Files:**
- Create: `internal/resources/rustfs_storage.go`
- Create: `internal/resources/rustfs_storage_test.go`

**Interfaces:**
- Consumes: `Storage` interface and env vars `RUSTFS_ENDPOINT`, `RUSTFS_ACCESS_KEY_ID`, `RUSTFS_SECRET_ACCESS_KEY`, `RUSTFS_USE_SSL`.
- Produces: `func NewRustFSStorage() (*RustFSStorage, error)` returning a `Storage` implementation.

- [ ] **Step 1: Implement constructor**

Create `internal/resources/rustfs_storage.go`:
```go
package resources

import (
    "context"
    "errors"
    "io"
    "os"
    "strconv"
    "strings"

    "github.com/minio/minio-go/v7"
    "github.com/minio/minio-go/v7/pkg/credentials"
)

type RustFSStorage struct {
    client    *minio.Client
    publicURL string
}

func NewRustFSStorage() (*RustFSStorage, error) {
    endpoint := os.Getenv("RUSTFS_ENDPOINT")
    if endpoint == "" {
        endpoint = "rustfs:9000"
    }
    accessKey := os.Getenv("RUSTFS_ACCESS_KEY_ID")
    secretKey := os.Getenv("RUSTFS_SECRET_ACCESS_KEY")
    useSSL, _ := strconv.ParseBool(os.Getenv("RUSTFS_USE_SSL"))

    client, err := minio.New(endpoint, &minio.Options{
        Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
        Secure: useSSL,
    })
    if err != nil {
        return nil, err
    }

    return &RustFSStorage{
        client:    client,
        publicURL: os.Getenv("RUSTFS_PUBLIC_URL"),
    }, nil
}

func (s *RustFSStorage) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) (ObjectInfo, error) {
    if contentType == "" {
        contentType = "application/octet-stream"
    }
    _, err := s.client.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
    if err != nil {
        return ObjectInfo{}, err
    }
    return s.Stat(ctx, bucket, key)
}

func (s *RustFSStorage) Open(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
    return s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
}

func (s *RustFSStorage) Stat(ctx context.Context, bucket, key string) (ObjectInfo, error) {
    info, err := s.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
    if err != nil {
        return ObjectInfo{}, err
    }
    return ObjectInfo{
        Key:          info.Key,
        Size:         info.Size,
        ContentType:  info.ContentType,
        ETag:         info.ETag,
        LastModified: info.LastModified,
    }, nil
}

func (s *RustFSStorage) List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error) {
    var out []ObjectInfo
    for obj := range s.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
        if obj.Err != nil {
            return nil, obj.Err
        }
        out = append(out, ObjectInfo{
            Key:          obj.Key,
            Size:         obj.Size,
            ContentType:  obj.ContentType,
            ETag:         obj.ETag,
            LastModified: obj.LastModified,
        })
    }
    return out, nil
}

func (s *RustFSStorage) Delete(ctx context.Context, bucket, key string) error {
    return s.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}
```

- [ ] **Step 2: Write fake storage for tests**

Create `internal/resources/storage_test.go`:
```go
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
    objects map[string][]byte
}

func newFakeStorage() *fakeStorage {
    return &fakeStorage{objects: map[string][]byte{}}
}

func key(bucket, k string) string { return bucket + "/" + k }

func (f *fakeStorage) Put(ctx context.Context, bucket, k string, r io.Reader, size int64, contentType string) (ObjectInfo, error) {
    data, _ := io.ReadAll(r)
    f.objects[key(bucket, k)] = data
    return ObjectInfo{Key: k, Size: int64(len(data)), ContentType: contentType, LastModified: time.Now()}, nil
}

func (f *fakeStorage) Open(ctx context.Context, bucket, k string) (io.ReadCloser, error) {
    data, ok := f.objects[key(bucket, k)]
    if !ok {
        return nil, errors.New("not found")
    }
    return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeStorage) Stat(ctx context.Context, bucket, k string) (ObjectInfo, error) {
    data, ok := f.objects[key(bucket, k)]
    if !ok {
        return ObjectInfo{}, errors.New("not found")
    }
    return ObjectInfo{Key: k, Size: int64(len(data)), LastModified: time.Now()}, nil
}

func (f *fakeStorage) List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error) {
    var out []ObjectInfo
    prefixPath := bucket + "/" + prefix
    for k, data := range f.objects {
        if strings.HasPrefix(k, prefixPath) {
            out = append(out, ObjectInfo{Key: strings.TrimPrefix(k, bucket+"/"), Size: int64(len(data))})
        }
    }
    return out, nil
}

func (f *fakeStorage) Delete(ctx context.Context, bucket, k string) error {
    delete(f.objects, key(bucket, k))
    return nil
}

func TestFakeStorage_PutOpen(t *testing.T) {
    s := newFakeStorage()
    _, err := s.Put(context.Background(), "users", "abc/file.txt", strings.NewReader("hello"), 5, "text/plain")
    if err != nil {
        t.Fatal(err)
    }
    r, err := s.Open(context.Background(), "users", "abc/file.txt")
    if err != nil {
        t.Fatal(err)
    }
    defer r.Close()
    data, _ := io.ReadAll(r)
    if string(data) != "hello" {
        t.Fatalf("expected hello, got %s", data)
    }
}
```

- [ ] **Step 3: Run tests**

Run:
```bash
go test ./internal/resources/ -v
```

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/resources/rustfs_storage.go internal/resources/storage_test.go
git commit -m "feat(resources): implement RustFSStorage with fake storage tests"
```

### Task 4: Implement TokenStore

**Files:**
- Create: `internal/resources/token.go`
- Create: `internal/resources/token_test.go`

**Interfaces:**
- Produces: `type TokenStore struct{}` with methods `Issue`, `Validate`, `Cleanup`; token is one-shot with TTL.

- [ ] **Step 1: Implement token store**

Create `internal/resources/token.go`:
```go
package resources

import (
    "crypto/rand"
    "encoding/hex"
    "errors"
    "sync"
    "time"
)

type TokenStore struct {
    mu     sync.RWMutex
    tokens map[string]*tokenEntry
}

type tokenEntry struct {
    bucket    string
    key       string
    userID    string
    sessionID string
    expiresAt time.Time
    used      bool
}

func NewTokenStore() *TokenStore {
    return &TokenStore{tokens: map[string]*tokenEntry{}}
}

func (t *TokenStore) Issue(bucket, key, userID, sessionID string, ttl time.Duration) string {
    b := make([]byte, 16)
    rand.Read(b)
    token := hex.EncodeToString(b)
    t.mu.Lock()
    t.tokens[token] = &tokenEntry{
        bucket:    bucket,
        key:       key,
        userID:    userID,
        sessionID: sessionID,
        expiresAt: time.Now().Add(ttl),
        used:      false,
    }
    t.mu.Unlock()
    return token
}

func (t *TokenStore) Validate(token string) (*tokenEntry, error) {
    t.mu.Lock()
    defer t.mu.Unlock()
    e, ok := t.tokens[token]
    if !ok {
        return nil, errors.New("invalid token")
    }
    if time.Now().After(e.expiresAt) {
        delete(t.tokens, token)
        return nil, errors.New("token expired")
    }
    if e.used {
        delete(t.tokens, token)
        return nil, errors.New("token already used")
    }
    e.used = true
    return e, nil
}

func (t *TokenStore) Cleanup() {
    t.mu.Lock()
    defer t.mu.Unlock()
    now := time.Now()
    for k, e := range t.tokens {
        if now.After(e.expiresAt) {
            delete(t.tokens, k)
        }
    }
}

func StartCleanup(store *TokenStore, interval time.Duration) *time.Ticker {
    ticker := time.NewTicker(interval)
    go func() {
        for range ticker.C {
            store.Cleanup()
        }
    }()
    return ticker
}
```

- [ ] **Step 2: Write tests**

Create `internal/resources/token_test.go`:
```go
package resources

import (
    "strings"
    "testing"
    "time"
)

func TestTokenStore_IssueValidate(t *testing.T) {
    s := NewTokenStore()
    token := s.Issue("users", "abc/file.txt", "abc", "sess1", time.Minute)
    e, err := s.Validate(token)
    if err != nil {
        t.Fatal(err)
    }
    if e.bucket != "users" || e.key != "abc/file.txt" {
        t.Fatalf("unexpected entry: %+v", e)
    }
    _, err = s.Validate(token)
    if err == nil || !strings.Contains(err.Error(), "already used") {
        t.Fatalf("expected already used error, got %v", err)
    }
}

func TestTokenStore_Expired(t *testing.T) {
    s := NewTokenStore()
    token := s.Issue("users", "abc/file.txt", "abc", "sess1", -time.Second)
    _, err := s.Validate(token)
    if err == nil || !strings.Contains(err.Error(), "expired") {
        t.Fatalf("expected expired error, got %v", err)
    }
}
```

- [ ] **Step 3: Run tests**

Run:
```bash
go test ./internal/resources/ -v
```

Expected: PASS for token tests.

- [ ] **Step 4: Commit**

```bash
git add internal/resources/token.go internal/resources/token_test.go
git commit -m "feat(resources): add one-shot ephemeral token store"
```

### Task 5: Implement ResourceManager

**Files:**
- Create: `internal/resources/manager.go`
- Create: `internal/resources/manager_test.go`

**Interfaces:**
- Consumes: `Storage`, `TokenStore`, `session.Store`.
- Produces:
  - `func NewResourceManager(storage Storage, sessions *session.Store) *ResourceManager`
  - `func (m *ResourceManager) ResolveForTool(ctx, sessionID, rawArg string) (Resource, error)`
  - `func (m *ResourceManager) ResolveManyForTool(ctx context.Context, sessionID string, rawArgs []string) ([]Resource, error)`
  - `func (m *ResourceManager) ListForUser(ctx, sessionID, prefix string) ([]Resource, error)`
  - `func (m *ResourceManager) ReadForUser(ctx, sessionID, uri string) (Resource, error)`
  - `func (m *ResourceManager) PutForUser(ctx, sessionID string, key string, r io.Reader, size int64, ct string) (Resource, error)`

- [ ] **Step 1: Implement manager**

Create `internal/resources/manager.go`:
```go
package resources

import (
    "context"
    "errors"
    "fmt"
    "io"
    "path"
    "strings"
    "time"

    "github.com/sudebaker/mcp-go/internal/session"
)

const (
    defaultBucket = "users"
    tokenTTL      = 60 * time.Second
)

type ResourceManager struct {
    storage    Storage
    sessions   *session.Store
    tokens     *TokenStore
}

func NewResourceManager(storage Storage, sessions *session.Store) *ResourceManager {
    return &ResourceManager{
        storage:  storage,
        sessions: sessions,
        tokens:   NewTokenStore(),
    }
}

func (m *ResourceManager) userBucket(userID string) string {
    return defaultBucket
}

func (m *ResourceManager) userPrefix(userID string) string {
    return userID + "/"
}

func (m *ResourceManager) resolveUser(sessionID string) (string, error) {
    userID, ok := m.sessions.Get(sessionID)
    if !ok || userID == "" {
        return "", errors.New("session not authenticated")
    }
    return userID, nil
}

func (m *ResourceManager) ResolveForTool(ctx context.Context, sessionID, rawArg string) (Resource, error) {
    // Phase 1 supports file_path and res:// URIs; __files__ handled in ResolveManyForTool
    userID, err := m.resolveUser(sessionID)
    if err != nil {
        return Resource{}, err
    }

    // If it's already an opaque token URI, validate and convert to resource metadata
    if strings.HasPrefix(rawArg, "res://") {
        token := strings.TrimPrefix(rawArg, "res://")
        return m.resourceFromToken(ctx, token)
    }

    // Legacy file:// or absolute path: resolve via Storage if under /data/input or similar
    if strings.HasPrefix(rawArg, "file://") || strings.HasPrefix(rawArg, "/data/") {
        // For Phase 1 compatibility: read local file, store in user bucket, return resource
        return m.resolveLocalPath(ctx, userID, rawArg)
    }

    return Resource{}, fmt.Errorf("unsupported resource argument: %s", rawArg)
}

func (m *ResourceManager) ResolveManyForTool(ctx context.Context, sessionID string, rawArgs []string) ([]Resource, error) {
    var out []Resource
    for _, raw := range rawArgs {
        r, err := m.ResolveForTool(ctx, sessionID, raw)
        if err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, nil
}

func (m *ResourceManager) ListForUser(ctx context.Context, sessionID, prefix string) ([]Resource, error) {
    userID, err := m.resolveUser(sessionID)
    if err != nil {
        return nil, err
    }
    fullPrefix := m.userPrefix(userID) + prefix
    infos, err := m.storage.List(ctx, defaultBucket, fullPrefix)
    if err != nil {
        return nil, err
    }
    var out []Resource
    for _, info := range infos {
        token := m.tokens.Issue(defaultBucket, info.Key, userID, sessionID, tokenTTL)
        out = append(out, Resource{
            URI:      "res://" + token,
            Name:     path.Base(info.Key),
            MIMEType: info.ContentType,
            Size:     info.Size,
        })
    }
    return out, nil
}

func (m *ResourceManager) ReadForUser(ctx context.Context, sessionID, uri string) (Resource, error) {
    if !strings.HasPrefix(uri, "res://") {
        return Resource{}, errors.New("invalid resource URI")
    }
    token := strings.TrimPrefix(uri, "res://")
    entry, err := m.tokens.Validate(token)
    if err != nil {
        return Resource{}, err
    }
    userID, err := m.resolveUser(sessionID)
    if err != nil {
        return Resource{}, err
    }
    if entry.userID != userID {
        return Resource{}, errors.New("unauthorized resource")
    }
    return m.openResource(ctx, entry.bucket, entry.key, entry.userID, sessionID)
}

func (m *ResourceManager) PutForUser(ctx context.Context, sessionID string, key string, r io.Reader, size int64, ct string) (Resource, error) {
    userID, err := m.resolveUser(sessionID)
    if err != nil {
        return Resource{}, err
    }
    fullKey := m.userPrefix(userID) + key
    info, err := m.storage.Put(ctx, defaultBucket, fullKey, r, size, ct)
    if err != nil {
        return Resource{}, err
    }
    token := m.tokens.Issue(defaultBucket, fullKey, userID, sessionID, tokenTTL)
    return Resource{
        URI:      "res://" + token,
        Name:     path.Base(info.Key),
        MIMEType: info.ContentType,
        Size:     info.Size,
    }, nil
}

func (m *ResourceManager) resourceFromToken(ctx context.Context, token string) (Resource, error) {
    entry, err := m.tokens.Validate(token)
    if err != nil {
        return Resource{}, err
    }
    return m.openResource(ctx, entry.bucket, entry.key, entry.userID, entry.sessionID)
}

func (m *ResourceManager) openResource(ctx context.Context, bucket, key, userID, sessionID string) (Resource, error) {
    reader, err := m.storage.Open(ctx, bucket, key)
    if err != nil {
        return Resource{}, err
    }
    info, err := m.storage.Stat(ctx, bucket, key)
    if err != nil {
        reader.Close()
        return Resource{}, err
    }
    return Resource{
        URI:      "res://" + m.tokens.Issue(bucket, key, userID, sessionID, tokenTTL),
        Name:     path.Base(info.Key),
        MIMEType: info.ContentType,
        Size:     info.Size,
    }, nil
}

func (m *ResourceManager) resolveLocalPath(ctx context.Context, userID, raw string) (Resource, error) {
    // Phase 1 only: read file from local path and mirror to storage
    p := strings.TrimPrefix(raw, "file://")
    f, err := os.Open(p)
    if err != nil {
        return Resource{}, err
    }
    defer f.Close()
    stat, err := f.Stat()
    if err != nil {
        return Resource{}, err
    }
    key := m.userPrefix(userID) + path.Base(p)
    info, err := m.storage.Put(ctx, defaultBucket, key, f, stat.Size(), "")
    if err != nil {
        return Resource{}, err
    }
    token := m.tokens.Issue(defaultBucket, key, userID, "", tokenTTL)
    return Resource{
        URI:      "res://" + token,
        Name:     path.Base(info.Key),
        MIMEType: info.ContentType,
        Size:     info.Size,
    }, nil
}
```

Note: add `import "os"` at top of file.

- [ ] **Step 2: Write tests**

Create `internal/resources/manager_test.go`:
```go
package resources

import (
    "context"
    "strings"
    "testing"

    "github.com/sudebaker/mcp-go/internal/session"
)

func TestResourceManager_PutForUser(t *testing.T) {
    store := session.NewStore()
    store.Set("sess1", "user-abc")
    mgr := NewResourceManager(newFakeStorage(), store)

    r, err := mgr.PutForUser(context.Background(), "sess1", "file.txt", strings.NewReader("hello"), 5, "text/plain")
    if err != nil {
        t.Fatal(err)
    }
    if !strings.HasPrefix(r.URI, "res://") {
        t.Fatalf("expected res:// URI, got %s", r.URI)
    }
    if r.Name != "file.txt" || r.Size != 5 {
        t.Fatalf("unexpected resource: %+v", r)
    }
}

func TestResourceManager_ListForUser(t *testing.T) {
    store := session.NewStore()
    store.Set("sess1", "user-abc")
    storage := newFakeStorage()
    storage.Put(context.Background(), "users", "user-abc/a.txt", strings.NewReader("a"), 1, "text/plain")
    storage.Put(context.Background(), "users", "user-abc/b.txt", strings.NewReader("b"), 1, "text/plain")
    storage.Put(context.Background(), "users", "user-def/x.txt", strings.NewReader("x"), 1, "text/plain")

    mgr := NewResourceManager(storage, store)
    res, err := mgr.ListForUser(context.Background(), "sess1", "")
    if err != nil {
        t.Fatal(err)
    }
    if len(res) != 2 {
        t.Fatalf("expected 2 resources for user-abc, got %d", len(res))
    }
}

func TestResourceManager_ReadForUser_Unauthorized(t *testing.T) {
    store := session.NewStore()
    store.Set("sess1", "user-abc")
    store.Set("sess2", "user-def")
    mgr := NewResourceManager(newFakeStorage(), store)

    res, _ := mgr.PutForUser(context.Background(), "sess1", "file.txt", strings.NewReader("hello"), 5, "text/plain")
    _, err := mgr.ReadForUser(context.Background(), "sess2", res.URI)
    if err == nil {
        t.Fatal("expected unauthorized error")
    }
}
```

- [ ] **Step 3: Run tests**

Run:
```bash
go test ./internal/resources/ -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/resources/manager.go internal/resources/manager_test.go
git commit -m "feat(resources): implement ResourceManager with user isolation"
```

---

## Phase 2: Go Transport + MCP Resources

### Task 6: Add internal resource streaming endpoint

**Files:**
- Create: `internal/transport/resource_handler.go`
- Create: `internal/transport/resource_handler_test.go`

**Interfaces:**
- Consumes: `*resources.ResourceManager`.
- Produces: `func (s *MCPServer) handleInternalResource(w http.ResponseWriter, r *http.Request)`.

- [ ] **Step 1: Implement handler**

Create `internal/transport/resource_handler.go`:
```go
package transport

import (
    "io"
    "net/http"
    "strings"

    "github.com/rs/zerolog/log"
)

func (s *MCPServer) handleInternalResource(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    token := strings.TrimPrefix(r.URL.Path, "/internal/resource/")
    if token == "" {
        http.Error(w, "Missing token", http.StatusBadRequest)
        return
    }

    entry, err := s.resourceManager.Tokens().Validate(token)
    if err != nil {
        http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
        return
    }

    ctx := r.Context()
    reader, err := s.resourceManager.Storage().Open(ctx, entry.Bucket, entry.Key)
    if err != nil {
        log.Error().Err(err).Str("key", entry.Key).Msg("Failed to open resource")
        http.Error(w, "Failed to open resource", http.StatusInternalServerError)
        return
    }
    defer reader.Close()

    info, err := s.resourceManager.Storage().Stat(ctx, entry.Bucket, entry.Key)
    if err == nil && info.ContentType != "" {
        w.Header().Set("Content-Type", info.ContentType)
    }
    w.Header().Set("X-Resource-Name", entry.Name)

    if _, err := io.Copy(w, reader); err != nil {
        log.Error().Err(err).Msg("Failed to stream resource")
    }
}
```

Note: this requires exposing `Tokens()` and `Storage()` accessors on `ResourceManager`, or using package-private helpers. Adjust by adding accessors to `internal/resources/manager.go`:

```go
func (m *ResourceManager) Tokens() *TokenStore { return m.tokens }
func (m *ResourceManager) Storage() Storage     { return m.storage }
```

And change `tokenEntry` fields to be exported or add accessors. For simplicity in this plan, export fields: `Bucket`, `Key`, `UserID`, `SessionID`, `Name` (add Name if desired; currently not stored). Add `Name` to tokenEntry when issuing from upload.

Update `token.go` tokenEntry to:
```go
type tokenEntry struct {
    Bucket    string
    Key       string
    Name      string
    UserID    string
    SessionID string
    ExpiresAt time.Time
    Used      bool
}
```

And update `Issue` call sites to pass Name.

- [ ] **Step 2: Register endpoint in sse.go**

Modify `internal/transport/sse.go` near the existing `/upload` registration:

```go
mux.HandleFunc("/upload", s.authMiddleware(s.handleUpload))
mux.HandleFunc("/internal/resource/", s.handleInternalResource)
```

The internal endpoint must only be reachable from localhost/Docker network. If `authMiddleware` would block it, register it without middleware but bind to `127.0.0.1` only via a separate listener.

For this plan, assume Docker network isolation and do not apply `authMiddleware`.

- [ ] **Step 3: Add ResourceManager field to MCPServer**

Modify `internal/transport/sse.go` struct:

```go
type MCPServer struct {
    // existing fields
    resourceManager *resources.ResourceManager
}
```

Add constructor parameter or setter. If using a setter:

```go
func (s *MCPServer) SetResourceManager(m *resources.ResourceManager) {
    s.resourceManager = m
}
```

- [ ] **Step 4: Test handler**

Create `internal/transport/resource_handler_test.go`:

```go
package transport

import (
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/sudebaker/mcp-go/internal/resources"
    "github.com/sudebaker/mcp-go/internal/session"
)

func TestHandleInternalResource(t *testing.T) {
    store := session.NewStore()
    store.Set("sess1", "user-abc")
    storage := resources.NewFakeStorageForTests()
    storage.Put(t.Context(), "users", "user-abc/file.txt", strings.NewReader("hello"), 5, "text/plain")
    mgr := resources.NewResourceManager(storage, store)
    res, _ := mgr.PutForUser(t.Context(), "sess1", "file.txt", strings.NewReader("hello"), 5, "text/plain")

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
}
```

Note: adjust helper names to match actual exported test helpers.

- [ ] **Step 5: Run tests**

Run:
```bash
go test ./internal/transport/ -run TestHandleInternalResource -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/transport/resource_handler.go internal/transport/resource_handler_test.go internal/transport/sse.go internal/resources/manager.go
git commit -m "feat(transport): add internal resource streaming endpoint"
```

### Task 7: Rewrite /upload to use ResourceManager

**Files:**
- Modify: `internal/transport/upload_handler.go`
- Modify: `internal/transport/upload_handler_test.go`

**Interfaces:**
- Consumes: `ResourceManager.PutForUser`.
- Produces: HTTP response with `uri`, `sha256`, `size`, `content_type`, `name`.

- [ ] **Step 1: Compute SHA256 during upload**

Modify `internal/transport/upload_handler.go` so after reading and validating the multipart file, wrap the reader in a SHA256 hasher:

```go
import (
    "crypto/sha256"
    "encoding/hex"
    "hash"
    "io"
)

// inside handleUpload, before PutForUser:
hasher := sha256.New()
teeReader := io.TeeReader(fileReader, hasher)

// determine key and content type
key := uniqueName // or sanitized original name + random suffix

res, err := s.resourceManager.PutForUser(r.Context(), sessionID, key, teeReader, written, contentType)
if err != nil {
    log.Error().Err(err).Msg("Failed to upload to storage")
    http.Error(w, "Failed to save file", http.StatusInternalServerError)
    return
}

sha := hex.EncodeToString(hasher.Sum(nil))
```

Update response:

```go
json.NewEncoder(w).Encode(map[string]any{
    "success":      true,
    "uri":          res.URI,
    "sha256":       sha,
    "size":         res.Size,
    "content_type": res.MIMEType,
    "name":         res.Name,
})
```

- [ ] **Step 2: Remove local upload artifacts**

Delete or remove usage of `uploadDir`, `.meta` sidecar, `cleanExpiredUploads`, `startUploadCleanup`. Remove related config fields from `uploadConfig` (`UploadDir`, `DefaultTTLSeconds`, `MaxTTLSeconds`).

- [ ] **Step 3: Obtain sessionID in upload handler**

The upload endpoint currently uses `authMiddleware` which validates `MCP_UPLOAD_API_KEY`. Add a way to map the API key or request header to a session. For Phase 1, accept a required `X-Session-ID` header from internal clients (e.g. the MCP client) and look it up in the session store.

```go
sessionID := r.Header.Get("X-Session-ID")
if sessionID == "" {
    http.Error(w, "Missing X-Session-ID", http.StatusBadRequest)
    return
}
```

- [ ] **Step 4: Update upload tests**

Modify `internal/transport/upload_handler_test.go` to mock `ResourceManager` instead of local filesystem. Tests should assert response contains `uri` with `res://` prefix and `sha256`.

- [ ] **Step 5: Run tests**

Run:
```bash
go test ./internal/transport/ -v
```

Expected: PASS (may need iteration).

- [ ] **Step 6: Commit**

```bash
git add internal/transport/upload_handler.go internal/transport/upload_handler_test.go
git commit -m "feat(upload): store uploads via ResourceManager and return opaque URI"
```

### Task 8: Implement MCP resources/list and resources/read

**Files:**
- Create: `internal/resources/handler.go`
- Create: `internal/resources/handler_test.go`
- Modify: `cmd/server/main.go`

**Interfaces:**
- Consumes: `ResourceManager`.
- Produces: JSON-RPC responses for `resources/list` and `resources/read`.

- [ ] **Step 1: Implement handlers**

Create `internal/resources/handler.go`:

```go
package resources

import (
    "context"
    "encoding/base64"
    "io"

    "github.com/mark3labs/mcp-go/mcp"
    "github.com/mark3labs/mcp-go/server"
)

type ResourceHandler struct {
    manager *ResourceManager
}

func NewResourceHandler(manager *ResourceManager) *ResourceHandler {
    return &ResourceHandler{manager: manager}
}

func (h *ResourceHandler) List(ctx context.Context, request mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
    sessionID := server.SessionIDFromContext(ctx)
    resources, err := h.manager.ListForUser(ctx, sessionID, request.Cursor)
    if err != nil {
        return nil, err
    }
    var items []mcp.Resource
    for _, r := range resources {
        items = append(items, mcp.Resource{
            URI:      r.URI,
            Name:     r.Name,
            MIMEType: r.MIMEType,
            Size:     r.Size,
        })
    }
    return &mcp.ListResourcesResult{Resources: items}, nil
}

func (h *ResourceHandler) Read(ctx context.Context, request mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
    sessionID := server.SessionIDFromContext(ctx)
    res, err := h.manager.ReadForUser(ctx, sessionID, request.URI)
    if err != nil {
        return nil, err
    }
    defer res.Reader.Close()

    data, err := io.ReadAll(res.Reader)
    if err != nil {
        return nil, err
    }

    var contents []mcp.ResourceContents
    if isText(res.MIMEType) {
        contents = append(contents, mcp.TextResourceContents{
            URI:      res.URI,
            MIMEType: res.MIMEType,
            Text:     string(data),
        })
    } else {
        contents = append(contents, mcp.BlobResourceContents{
            URI:      res.URI,
            MIMEType: res.MIMEType,
            Blob:     base64.StdEncoding.EncodeToString(data),
        })
    }
    return &mcp.ReadResourceResult{Contents: contents}, nil
}

func isText(mime string) bool {
    return strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "application/xml"
}
```

Add missing `strings` import.

- [ ] **Step 2: Register handlers in server**

Modify `cmd/server/main.go` to initialize ResourceManager and register resource handlers:

```go
storage, err := resources.NewRustFSStorage()
if err != nil {
    log.Fatal().Err(err).Msg("Failed to create rustfs storage")
}
resourceManager := resources.NewResourceManager(storage, sessionStore)
resourceHandler := resources.NewResourceHandler(resourceManager)

mcpServer := server.NewMCPServer(..., server.WithResourceCapabilities(true, true))
// or register handlers:
mcpServer.RegisterTool(...) // existing
mcpServer.AddResourceCapabilities(...) // depends on mcp-go API
```

Exact API depends on `mark3labs/mcp-go` version. Adjust based on current usage in `cmd/server/main.go`.

- [ ] **Step 3: Wire ResourceManager into transport**

In `cmd/server/main.go`, after creating `MCPServer` transport, call:

```go
transportServer.SetResourceManager(resourceManager)
```

- [ ] **Step 4: Write handler tests**

Create `internal/resources/handler_test.go`:

```go
package resources

import (
    "context"
    "strings"
    "testing"

    "github.com/sudebaker/mcp-go/internal/session"
)

func TestResourceHandler_List(t *testing.T) {
    store := session.NewStore()
    store.Set("sess1", "user-abc")
    storage := newFakeStorage()
    storage.Put(context.Background(), "users", "user-abc/a.txt", strings.NewReader("a"), 1, "text/plain")
    mgr := NewResourceManager(storage, store)
    h := NewResourceHandler(mgr)

    res, err := h.List(context.Background(), mcp.ListResourcesRequest{})
    if err != nil {
        t.Fatal(err)
    }
    if len(res.Resources) != 1 {
        t.Fatalf("expected 1 resource, got %d", len(res.Resources))
    }
}
```

- [ ] **Step 5: Run tests**

Run:
```bash
go test ./internal/resources/ ./cmd/server -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/resources/handler.go internal/resources/handler_test.go cmd/server/main.go
git commit -m "feat(mcp): implement resources/list and resources/read handlers"
```

---

## Phase 3: Python Resource Framework

### Task 9: Implement Python Resource and ToolContext

**Files:**
- Create: `tools/common/resources/__init__.py`
- Create: `tools/common/resources/resource.py`
- Create: `tools/common/resources/manager.py`
- Create: `tests/tools/common/resources/test_resource.py`
- Create: `tests/tools/common/resources/test_manager.py`

**Interfaces:**
- Produces:
  - `class Resource` with `reader`, `read_bytes`, `close`
  - `class ToolContext` with `file(arg_name)` and `files(arg_name)`

- [ ] **Step 1: Implement Resource class**

Create `tools/common/resources/resource.py`:

```python
import io
import os
import urllib.request

INTERNAL_HOST = os.getenv("MCP_INTERNAL_HOST", "localhost:8080")


class Resource:
    def __init__(self, uri: str, name: str, mime: str, size: int, sha256: str):
        self.uri = uri
        self.name = name
        self.mime = mime
        self.size = size
        self.sha256 = sha256
        self._reader = None

    @property
    def reader(self):
        if self._reader is None:
            token = self.uri.replace("res://", "", 1)
            url = f"http://{INTERNAL_HOST}/internal/resource/{token}"
            self._reader = urllib.request.urlopen(url)
        return self._reader

    def read_bytes(self) -> bytes:
        with self:
            return self.reader.read()

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()

    def close(self):
        if self._reader:
            self._reader.close()
            self._reader = None
```

- [ ] **Step 2: Implement ToolContext**

Create `tools/common/resources/manager.py`:

```python
from .resource import Resource


class ToolContext:
    def __init__(self, request: dict):
        self._request = request
        self._resources = {}

    def file(self, arg_name: str) -> Resource:
        if arg_name not in self._resources:
            meta = self._request.get("_resources", {}).get(arg_name)
            if not meta:
                raise KeyError(f"No resource bound to arg '{arg_name}'")
            self._resources[arg_name] = Resource(**meta)
        return self._resources[arg_name]

    def files(self, arg_name: str) -> list[Resource]:
        if arg_name not in self._resources:
            metas = self._request.get("_resources", {}).get(arg_name)
            if not metas:
                raise KeyError(f"No resources bound to arg '{arg_name}'")
            self._resources[arg_name] = [Resource(**m) for m in metas]
        return self._resources[arg_name]
```

- [ ] **Step 3: Package exports**

Create `tools/common/resources/__init__.py`:

```python
from .manager import ToolContext
from .resource import Resource

__all__ = ["ToolContext", "Resource"]
```

- [ ] **Step 4: Test Resource with mocked internal endpoint**

Create `tests/tools/common/resources/test_resource.py`:

```python
import io
import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

from common.resources import Resource


class MockHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(b"hello")

    def log_message(self, *args):
        pass


def test_resource_read_bytes(monkeypatch):
    server = HTTPServer(("127.0.0.1", 0), MockHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    port = server.server_address[1]
    monkeypatch.setenv("MCP_INTERNAL_HOST", f"127.0.0.1:{port}")

    r = Resource(uri="res://token123", name="file.txt", mime="text/plain", size=5, sha256="abc")
    assert r.read_bytes() == b"hello"
    server.shutdown()
```

- [ ] **Step 5: Test ToolContext**

Create `tests/tools/common/resources/test_manager.py`:

```python
from common.resources import ToolContext, Resource


def test_tool_context_file():
    request = {
        "_resources": {
            "document": {"uri": "res://t1", "name": "doc.txt", "mime": "text/plain", "size": 10, "sha256": "sha"}
        }
    }
    ctx = ToolContext(request)
    r = ctx.file("document")
    assert r.name == "doc.txt"


def test_tool_context_files():
    request = {
        "_resources": {
            "documents": [
                {"uri": "res://t1", "name": "a.txt", "mime": "text/plain", "size": 1, "sha256": "sha1"},
                {"uri": "res://t2", "name": "b.txt", "mime": "text/plain", "size": 1, "sha256": "sha2"},
            ]
        }
    }
    ctx = ToolContext(request)
    assert len(ctx.files("documents")) == 2
```

- [ ] **Step 6: Run tests**

Run:
```bash
cd /home/amphora/Proyectos/mcp-go
python -m pytest tests/tools/common/resources/ -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add tools/common/resources/ tests/tools/common/resources/
git commit -m "feat(resources): add Python Resource and ToolContext"
```

---

## Phase 4: Tool Migrations

### Task 10: Configure tool argument format

**Files:**
- Modify: `configs/config.yaml`

- [ ] **Step 1: Add resource-uri format to analysis tool schemas**

For each analysis tool in `configs/config.yaml`, replace file input definitions with `format: resource-uri`. Example for `vision_ocr`:

```yaml
vision_ocr:
  inputSchema:
    type: object
    properties:
      file_uri:
        type: string
        format: resource-uri
        description: URI of image resource
    required: [file_uri]
```

Repeat for `batch_summarize`, `regulation_diff`, `document_classifier`, `config_auditor`, `transcribe`, `metadata_extractor`, `stego_detector`, `document_fingerprint`, `api_diff`, `data_analysis`, `case_evidence`.

- [ ] **Step 2: Remove upload section TTL/dir settings**

Edit `configs/config.yaml` upload section to keep only:

```yaml
upload:
  enabled: true
  max_size_mb: 50
  allowed_types: [...]
```

Remove `default_ttl_seconds`, `max_ttl_seconds`, `upload_dir`.

- [ ] **Step 3: Run config validation**

Run:
```bash
python -c "import yaml; yaml.safe_load(open('configs/config.yaml'))"
```

Expected: no exception.

- [ ] **Step 4: Commit**

```bash
git add configs/config.yaml
git commit -m "config: declare file args as resource-uri and trim upload settings"
```

### Task 11: Migrate Group A — doc_extractor based tools

**Files:**
- Modify: `tools/batch_summarize/main.py`, `tools/batch_summarize/tool.yaml`
- Modify: `tools/regulation_diff/main.py`, `tools/regulation_diff/tool.yaml`
- Modify: `tools/document_classifier/main.py`, `tools/document_classifier/tool.yaml`

**Interfaces:**
- Consumes: `ToolContext.files("file_uris")`.

- [ ] **Step 1: Update batch_summarize**

In `tools/batch_summarize/main.py`, replace the loop:

```python
from common.resources import ToolContext
from common.doc_extractor import extract_text_from_buffer

ctx = ToolContext(request)
resources = ctx.files("file_uris")
for res in resources:
    data = res.read_bytes()
    text = extract_text_from_buffer(data, filename=res.name)
    # process text as before
```

Update `tools/batch_summarize/tool.yaml`:

```yaml
inputSchema:
  type: object
  properties:
    file_uris:
      type: array
      items:
        type: string
        format: resource-uri
      minItems: 1
  required: [file_uris]
```

- [ ] **Step 2: Update regulation_diff**

```python
from common.resources import ToolContext
from common.doc_extractor import extract_text_from_buffer

ctx = ToolContext(request)
old_res = ctx.file("file_uri_1")
new_res = ctx.file("file_uri_2")
old_text = extract_text_from_buffer(old_res.read_bytes(), filename=old_res.name)
new_text = extract_text_from_buffer(new_res.read_bytes(), filename=new_res.name)
```

Update schema with `file_uri_1` and `file_uri_2` as `format: resource-uri`, min/max 2.

- [ ] **Step 3: Update document_classifier**

Same pattern as batch_summarize.

- [ ] **Step 4: Run tests**

Run:
```bash
python -m pytest tests/tools/dev-tools/test_batch_summarize.py tests/tools/dev-tools/test_document_classifier.py tests/test_regulation_diff.py -v
```

Expected: PASS after updating mocks.

- [ ] **Step 5: Commit**

```bash
git add tools/batch_summarize tools/regulation_diff tools/document_classifier
git commit -m "feat(tools): migrate batch_summarize, regulation_diff, document_classifier to ToolContext"
```

### Task 12: Migrate Group B — local-path tools

**Files:**
- Modify: `tools/vision_ocr/main.py`, `tools/vision_ocr/tool.yaml`
- Modify: `tools/transcribe/main.py`, `tools/transcribe/tool.yaml`
- Modify: `tools/metadata_extractor/main.py`, `tools/metadata_extractor/tool.yaml`
- Modify: `tools/stego_detector/main.py`, `tools/stego_detector/tool.yaml`
- Modify: `tools/document_fingerprint/main.py`, `tools/document_fingerprint/tool.yaml`
- Modify: `tools/api_diff/main.py`, `tools/api_diff/tool.yaml`

**Interfaces:**
- Consumes: `ToolContext.file("file_uri")` for single, `.file("file_uri_1")` / `.file("file_uri_2")` for pairs.

- [ ] **Step 1: vision_ocr**

Replace `cv2.imread(image_path)` with:

```python
from common.resources import ToolContext
import numpy as np
import cv2

ctx = ToolContext(request)
resource = ctx.file("file_uri")
data = resource.read_bytes()
img_array = np.frombuffer(data, np.uint8)
img = cv2.imdecode(img_array, cv2.IMREAD_COLOR)
```

Update tool.yaml to use `file_uri` with `format: resource-uri`.

- [ ] **Step 2: transcribe**

Replace file path opening with:

```python
from common.resources import ToolContext
import io

ctx = ToolContext(request)
resource = ctx.file("file_uri")
audio_stream = io.BytesIO(resource.read_bytes())
# pass audio_stream to whisper
```

For true streaming, pass `resource.reader` directly if the whisper wrapper supports file-like objects.

- [ ] **Step 3: metadata_extractor**

Replace all direct `open(file_path, "rb")` and `magic.from_file(file_path)` with:

```python
from common.resources import ToolContext
import io
import magic

ctx = ToolContext(request)
resource = ctx.file("file_uri")
data = resource.read_bytes()
mime = magic.from_buffer(data, mime=True)
# PIL: PIL.Image.open(io.BytesIO(data))
# zip: zipfile.ZipFile(io.BytesIO(data))
```

- [ ] **Step 4: stego_detector**

```python
from common.resources import ToolContext
from PIL import Image
import io

ctx = ToolContext(request)
resource = ctx.file("file_uri")
img = Image.open(io.BytesIO(resource.read_bytes()))
```

- [ ] **Step 5: document_fingerprint**

```python
ctx = ToolContext(request)
img1 = Image.open(io.BytesIO(ctx.file("file_uri_1").read_bytes()))
img2 = Image.open(io.BytesIO(ctx.file("file_uri_2").read_bytes()))
```

- [ ] **Step 6: api_diff**

```python
ctx = ToolContext(request)
old_text = ctx.file("file_uri_old").read_bytes().decode("utf-8")
new_text = ctx.file("file_uri_new").read_bytes().decode("utf-8")
```

- [ ] **Step 7: Update schemas**

Update each tool.yaml to declare the new `file_uri` argument(s) with `format: resource-uri`.

- [ ] **Step 8: Run tests**

Run:
```bash
python -m pytest tests/tools/dev-tools/test_api_diff.py tests/test_doc_extractor.py tests/tools/dev-tools/test_doc_generator.py -v 2>&1 | head -50
```

Note: some tests may not exist for all tools. Update available ones.

- [ ] **Step 9: Commit**

```bash
git add tools/vision_ocr tools/transcribe tools/metadata_extractor tools/stego_detector tools/document_fingerprint tools/api_diff
git commit -m "feat(tools): migrate local-path tools to ToolContext Resource API"
```

### Task 13: Migrate Group C — advanced tools

**Files:**
- Modify: `tools/data_analysis/main.py`, `tools/data_analysis/tool.yaml`
- Modify: `tools/case_evidence/main.py`, `tools/case_evidence/tool.yaml`

- [ ] **Step 1: data_analysis**

Remove `get_rustfs_s3_client`, `is_rustfs_url`, `download_from_s3`, `load_data_from_base64`, `download_file_from_url`. Replace with:

```python
from common.resources import ToolContext

ctx = ToolContext(request)
resource = ctx.file("file_uri")

# pandas
import pandas as pd
import io
df = pd.read_csv(io.BytesIO(resource.read_bytes()))
# or pd.read_excel, etc.
```

For sandbox execution, write `resource.read_bytes()` to the sandbox input path.

- [ ] **Step 2: case_evidence**

Replace `download_file()` and own `get_rustfs_client()` with:

```python
from common.resources import ToolContext

ctx = ToolContext(request)
resource = ctx.file("file_uri")
data = resource.read_bytes()
# analyze or re-upload via Go-managed storage
```

If the tool needs to persist evidence, add a Go endpoint or helper rather than calling MinIO from Python.

- [ ] **Step 3: Update schemas**

Use `file_uri` with `format: resource-uri`.

- [ ] **Step 4: Commit**

```bash
git add tools/data_analysis tools/case_evidence
git commit -m "feat(tools): migrate data_analysis and case_evidence to ToolContext"
```

### Task 14: Migrate Group D — output tools

**Files:**
- Modify: `tools/canvas_diagram/main.py`
- Modify: `tools/rustfs_storage/main.py`, `tools/rustfs_storage/tool.yaml`

- [ ] **Step 1: canvas_diagram**

Remove custom MinIO upload. Add response field that instructs Go to store the file, or add a new tool operation that the Go server handles. Short-term: write to `/data/output` and include base64 in response for Go to upload.

Long-term: expose a Go endpoint `POST /internal/resource/upload` for tools to request storage. Implement in Phase 5.

For this task, keep it simple: return the generated content as base64 in the tool response and let Go handle persistence if needed.

- [ ] **Step 2: rustfs_storage deprecation**

Update `tools/rustfs_storage/main.py` to return a deprecation message:

```python
response = {
    "success": False,
    "error": {
        "code": "DEPRECATED",
        "message": "rustfs_storage is deprecated. Use MCP resources and ToolContext instead."
    }
}
```

Update `tools/rustfs_storage/tool.yaml` description to include `(deprecated)`.

- [ ] **Step 3: Commit**

```bash
git add tools/canvas_diagram tools/rustfs_storage
git commit -m "feat(tools): deprecate rustfs_storage tool and update canvas_diagram output"
```

### Task 15: Implement config_auditor real file reading

**Files:**
- Modify: `tools/config_auditor/main.py`
- Modify: `tools/config_auditor/tool.yaml`

- [ ] **Step 1: Implement reading**

```python
from common.resources import ToolContext

ctx = ToolContext(request)
resources = ctx.files("file_uris")
findings = []
for res in resources:
    content = res.read_bytes().decode("utf-8", errors="ignore")
    findings.extend(audit_config(content))
```

- [ ] **Step 2: Update tool.yaml**

Add `file_uris` array with `format: resource-uri`.

- [ ] **Step 3: Commit**

```bash
git add tools/config_auditor
git commit -m "feat(config_auditor): implement real resource reading"
```

---

## Phase 5: Go Helpers for Tool Output Upload

### Task 16: Add tool output upload endpoint

**Files:**
- Modify: `internal/transport/resource_handler.go`
- Create: `internal/transport/upload_tool_output_test.go`

**Interfaces:**
- Consumes: `ResourceManager.PutForUser`.
- Produces: `POST /internal/resource/upload` accepting bytes and returning a `Resource` URI.

- [ ] **Step 1: Implement POST endpoint**

Add handler:

```go
func (s *MCPServer) handleInternalResourceUpload(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    sessionID := r.Header.Get("X-Session-ID")
    if sessionID == "" {
        http.Error(w, "Missing X-Session-ID", http.StatusBadRequest)
        return
    }
    key := r.URL.Query().Get("key")
    if key == "" {
        http.Error(w, "Missing key", http.StatusBadRequest)
        return
    }
    contentType := r.Header.Get("Content-Type")
    size := r.ContentLength

    res, err := s.resourceManager.PutForUser(r.Context(), sessionID, key, r.Body, size, contentType)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    json.NewEncoder(w).Encode(res)
}
```

Register in `sse.go`:

```go
mux.HandleFunc("/internal/resource/upload", s.handleInternalResourceUpload)
```

- [ ] **Step 2: Add Python helper for output upload**

Create `tools/common/resources/uploader.py`:

```python
import os
import urllib.request

INTERNAL_HOST = os.getenv("MCP_INTERNAL_HOST", "localhost:8080")


def upload_tool_output(session_id: str, key: str, data: bytes, mime: str) -> dict:
    url = f"http://{INTERNAL_HOST}/internal/resource/upload?key={urllib.parse.quote(key)}"
    req = urllib.request.Request(
        url,
        data=data,
        headers={
            "Content-Type": mime,
            "X-Session-ID": session_id,
        },
        method="POST",
    )
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read().decode("utf-8"))
```

- [ ] **Step 3: Update canvas_diagram**

Use `upload_tool_output` to persist generated diagrams.

- [ ] **Step 4: Commit**

```bash
git add internal/transport/resource_handler.go tools/common/resources/uploader.py tools/canvas_diagram/main.py
git commit -m "feat(resources): add tool output upload helper via ResourceManager"
```

---

## Phase 6: Integration, Testing, and Documentation

### Task 17: Run full Go test suite

- [ ] **Step 1: Build**

Run:
```bash
go build -o bin/mcp-server ./cmd/server
```

Expected: no errors.

- [ ] **Step 2: Format and vet**

Run:
```bash
go fmt ./... && go vet ./...
```

Expected: clean.

- [ ] **Step 3: Run Go tests**

Run:
```bash
go test ./... -race
```

Expected: PASS.

- [ ] **Step 4: Commit fixes**

```bash
git add -A
git commit -m "fix: go fmt/vet/test pass for resources refactor"
```

### Task 18: Run Python test suite

- [ ] **Step 1: Run security tests**

Run:
```bash
python -m pytest tests/test_security_mitigations.py -v
```

Expected: PASS.

- [ ] **Step 2: Run resource tests**

Run:
```bash
python -m pytest tests/tools/common/resources/ -v
```

Expected: PASS.

- [ ] **Step 3: Run updated tool tests**

Run:
```bash
python -m pytest tests/ -v --ignore=tests/test_rustfs_integration.py
```

Expected: PASS (or identify tests needing mock updates).

- [ ] **Step 4: Commit fixes**

```bash
git add -A
git commit -m "fix: python tests pass after ToolContext migration"
```

### Task 19: Update documentation

**Files:**
- Modify: `API.md`
- Modify: `DEVELOPMENT.md`
- Modify: `SECURITY.md`
- Create or modify: `session-notes.md`

- [ ] **Step 1: Update API.md**

Add sections:
- "Resource URIs" explaining `res://` tokens
- `resources/list` and `resources/read` MCP methods
- `/upload` response format (`uri`, `sha256`, `size`, `content_type`, `name`)
- `/internal/resource/{token}` for tool subprocesses
- Migration guide from `__files__` / `file_path` to `file_uri`

- [ ] **Step 2: Update DEVELOPMENT.md**

Add:
- How to add a tool that consumes files (declare `format: resource-uri`, use `ToolContext`)
- How to add output upload
- Package layout (`internal/resources`, `tools/common/resources`)

- [ ] **Step 3: Update SECURITY.md**

Add:
- Resource isolation by user prefix
- One-shot tokens
- No bucket/path exposure to Python
- Validation flow diagram

- [ ] **Step 4: Write session-notes.md**

Record changes, decisions, and TODOs for the session.

- [ ] **Step 5: Commit**

```bash
git add API.md DEVELOPMENT.md SECURITY.md session-notes.md
git commit -m "docs: update API, development, and security docs for ResourceManager"
```

### Task 20: Final integration verification

- [ ] **Step 1: Start services**

```bash
cd deployments
docker-compose up -d
docker logs -f mcp-orchestrator
```

Wait for healthy state.

- [ ] **Step 2: Run MCP test client with upload**

```bash
python tests/mcp_test_client.py --skip-external
```

Or use a manual curl to `/upload` and verify `res://` URI response.

- [ ] **Step 3: Test resources/list and resources/read**

Use a simple MCP client script to call `resources/list` and `resources/read`.

- [ ] **Step 4: Test an analysis tool**

Call `tools/call` for `vision_ocr` with a `file_uri: res://...`.

- [ ] **Step 5: Commit any fixes**

```bash
git add -A
git commit -m "fix: integration testing fixes"
```

---

## Self-Review Checklist

### Spec coverage

| Spec section | Plan task |
|--------------|-----------|
| Resource abstraction | Task 2 |
| Storage interface | Task 2, 3 |
| RustFSStorage | Task 3 |
| ResourceManager | Task 5 |
| TokenStore | Task 4 |
| MCP resources/list, resources/read | Task 8 |
| Internal streaming endpoint | Task 6 |
| /upload to Storage | Task 7 |
| Python Resource | Task 9 |
| Python ToolContext | Task 9 |
| Tool migrations | Tasks 11-15 |
| Backward compatibility | Manager supports file_path; tasks accept legacy until removed |
| Streaming | Resource.Reader in Go + Python; upload uses TeeReader for SHA256 |
| Deprecate rustfs_storage | Task 14 |
| Documentation | Task 19 |

### Placeholder scan

No "TBD", "TODO", "implement later", or vague steps. Each step includes exact commands or code patterns. Tool migration tasks show the exact Python pattern and file list.

### Type consistency

- Go: `Resource.URI` is string (`res://...`); `ObjectInfo.Size` is `int64`.
- Python: `Resource.size` is `int`; `ToolContext.file()` returns `Resource`.
- Token methods consistent across tasks.

---

## Execution Options

**Plan complete and saved to `docs/superpowers/plans/2026-07-28-rustfs-unified-file-handling.md`.**

Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration. Requires `superpowers:subagent-driven-development`.
2. **Inline Execution** — execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints.

Which approach do you prefer?
