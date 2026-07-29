package resources

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sudebaker/mcp-go/internal/session"
)

func TestResourceManager_PutForUser(t *testing.T) {
	store := session.New()
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
	store := session.New()
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
	store := session.New()
	store.Set("sess1", "user-abc")
	store.Set("sess2", "user-def")
	mgr := NewResourceManager(newFakeStorage(), store)

	res, _ := mgr.PutForUser(context.Background(), "sess1", "file.txt", strings.NewReader("hello"), 5, "text/plain")
	_, err := mgr.ReadForUser(context.Background(), "sess2", res.URI)
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
}

func setupTestLocalRoots(t *testing.T) string {
	root := t.TempDir()
	t.Cleanup(func() { resetAllowedLocalRootsForTest(allowedLocalRootsDefault) })
	resetAllowedLocalRootsForTest([]string{filepath.Join(root, "input"), filepath.Join(root, "uploads")})
	return root
}

func TestResourceManager_ResolveLocalPath(t *testing.T) {
	root := setupTestLocalRoots(t)
	uploads := filepath.Join(root, "uploads")
	os.MkdirAll(uploads, 0755)
	p := filepath.Join(uploads, "local.txt")
	if err := os.WriteFile(p, []byte("local content"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.New()
	store.Set("sess1", "user-abc")
	storage := newFakeStorage()
	mgr := NewResourceManager(storage, store)

	r, err := mgr.ResolveForTool(context.Background(), "sess1", "file://"+p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.Name, "local") {
		t.Fatalf("expected name starting with local, got %s", r.Name)
	}
	if r.Size != 13 {
		t.Fatalf("expected size 13, got %d", r.Size)
	}

	infos, err := storage.List(context.Background(), "users", "user-abc/local/")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 stored local object, got %d", len(infos))
	}
	if infos[0].Size != 13 {
		t.Fatalf("expected stored size 13, got %d", infos[0].Size)
	}
}

func TestResourceManager_ResolveLocalPath_AllowedInput(t *testing.T) {
	root := setupTestLocalRoots(t)
	inputDir := filepath.Join(root, "input")
	os.MkdirAll(inputDir, 0755)
	p := filepath.Join(inputDir, "report.txt")
	if err := os.WriteFile(p, []byte("report"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.New()
	store.Set("sess1", "user-abc")
	storage := newFakeStorage()
	mgr := NewResourceManager(storage, store)

	r, err := mgr.ResolveForTool(context.Background(), "sess1", p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.Name, "report") {
		t.Fatalf("expected name starting with report, got %s", r.Name)
	}
	if !strings.HasPrefix(r.URI, "res://") {
		t.Fatalf("expected res:// URI, got %s", r.URI)
	}
}

func TestResourceManager_ResolveLocalPath_RejectsTraversal(t *testing.T) {
	root := setupTestLocalRoots(t)
	inputDir := filepath.Join(root, "input")
	os.MkdirAll(inputDir, 0755)

	store := session.New()
	store.Set("sess1", "user-abc")
	mgr := NewResourceManager(newFakeStorage(), store)

	cases := []string{
		"file:///etc/passwd",
		filepath.Join(root, "input", "..", "etc", "passwd"),
		filepath.Join(root, "uploads", "..", "..", "etc", "passwd"),
		filepath.Join(root, "other", "file.txt"),
		"/tmp/file.txt",
	}
	for _, raw := range cases {
		_, err := mgr.ResolveForTool(context.Background(), "sess1", raw)
		if !errors.Is(err, ErrPathNotAllowed) {
			t.Fatalf("expected ErrPathNotAllowed for %q, got %v", raw, err)
		}
	}
}

func TestResourceManager_ResolveLocalPath_KeyUnique(t *testing.T) {
	root := setupTestLocalRoots(t)
	uploads := filepath.Join(root, "uploads")
	os.MkdirAll(uploads, 0755)
	p := filepath.Join(uploads, "same.txt")
	if err := os.WriteFile(p, []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}

	store := session.New()
	store.Set("sess1", "user-abc")
	storage := newFakeStorage()
	mgr := NewResourceManager(storage, store)

	r1, err := mgr.ResolveForTool(context.Background(), "sess1", "file://"+p)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := mgr.ResolveForTool(context.Background(), "sess1", "file://"+p)
	if err != nil {
		t.Fatal(err)
	}
	if r1.URI == r2.URI {
		t.Fatal("expected distinct URIs for repeated resolves")
	}

	infos, err := storage.List(context.Background(), "users", "user-abc/local/")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 stored local objects, got %d", len(infos))
	}
}

func TestResourceManager_ResolveFilesPlaceholder(t *testing.T) {
	store := session.New()
	store.Set("sess1", "user-abc")
	storage := newFakeStorage()
	mgr := NewResourceManager(storage, store)

	_, err := storage.Put(context.Background(), "users", "user-abc/file1.txt", strings.NewReader("one"), 3, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	_, err = storage.Put(context.Background(), "users", "user-abc/file2.txt", strings.NewReader("two"), 3, "text/plain")
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ResolveManyForTool(context.Background(), "sess1", []string{"__files__"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 resources for __files__, got %d", len(res))
	}
	for _, r := range res {
		if !strings.HasPrefix(r.URI, "res://") {
			t.Fatalf("expected res:// URI, got %s", r.URI)
		}
	}
}

func TestResourceManager_ReadForUser_OK(t *testing.T) {
	store := session.New()
	store.Set("sess1", "user-abc")
	mgr := NewResourceManager(newFakeStorage(), store)

	res, err := mgr.PutForUser(context.Background(), "sess1", "file.txt", strings.NewReader("hello"), 5, "text/plain")
	if err != nil {
		t.Fatal(err)
	}

	read, err := mgr.ReadForUser(context.Background(), "sess1", res.URI)
	if err != nil {
		t.Fatal(err)
	}
	if read.Reader == nil {
		t.Fatal("expected reader")
	}
	defer read.Close()

	content, err := ioReadAllString(read.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if content != "hello" {
		t.Fatalf("expected hello, got %s", content)
	}
}

func ioReadAllString(r interface{ Read([]byte) (int, error) }) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String(), nil
}
