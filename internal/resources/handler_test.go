package resources

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sudebaker/mcp-go/internal/session"
)

func TestResourceHandler_ReadHandler_Text(t *testing.T) {
	store := session.New()
	store.Set("sess1", "user-abc")
	mgr := NewResourceManager(newFakeStorage(), store)
	mcpServer := server.NewMCPServer("test", "1.0.0")
	handler := NewResourceHandler(mgr, mcpServer)

	res, err := mgr.PutForUser(context.Background(), "sess1", "notes.txt", strings.NewReader("hello world"), 11, "text/plain")
	if err != nil {
		t.Fatal(err)
	}

	ctx := mcpServer.WithContext(context.Background(), newTestSession("sess1"))
	contents, err := handler.ReadHandler(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{URI: res.URI},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(contents))
	}

	tc, ok := contents[0].(mcp.TextResourceContents)
	if !ok {
		t.Fatalf("expected TextResourceContents, got %T", contents[0])
	}
	if tc.Text != "hello world" {
		t.Fatalf("expected 'hello world', got %q", tc.Text)
	}
	if tc.MIMEType != "text/plain" {
		t.Fatalf("expected text/plain, got %q", tc.MIMEType)
	}
}

func TestResourceHandler_ReadHandler_JSON(t *testing.T) {
	store := session.New()
	store.Set("sess1", "user-abc")
	mgr := NewResourceManager(newFakeStorage(), store)
	mcpServer := server.NewMCPServer("test", "1.0.0")
	handler := NewResourceHandler(mgr, mcpServer)

	res, err := mgr.PutForUser(context.Background(), "sess1", "data.json", strings.NewReader(`{"key":"value"}`), 15, "application/json")
	if err != nil {
		t.Fatal(err)
	}

	ctx := mcpServer.WithContext(context.Background(), newTestSession("sess1"))
	contents, err := handler.ReadHandler(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{URI: res.URI},
	})
	if err != nil {
		t.Fatal(err)
	}

	tc, ok := contents[0].(mcp.TextResourceContents)
	if !ok {
		t.Fatalf("expected TextResourceContents for JSON, got %T", contents[0])
	}
	if tc.Text != `{"key":"value"}` {
		t.Fatalf("unexpected text: %q", tc.Text)
	}
}

func TestResourceHandler_ReadHandler_Binary(t *testing.T) {
	store := session.New()
	store.Set("sess1", "user-abc")
	mgr := NewResourceManager(newFakeStorage(), store)
	mcpServer := server.NewMCPServer("test", "1.0.0")
	handler := NewResourceHandler(mgr, mcpServer)

	binaryData := []byte{0x89, 0x50, 0x4E, 0x47}
	res, err := mgr.PutForUser(context.Background(), "sess1", "image.png", strings.NewReader(string(binaryData)), 4, "image/png")
	if err != nil {
		t.Fatal(err)
	}

	ctx := mcpServer.WithContext(context.Background(), newTestSession("sess1"))
	contents, err := handler.ReadHandler(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{URI: res.URI},
	})
	if err != nil {
		t.Fatal(err)
	}

	bc, ok := contents[0].(mcp.BlobResourceContents)
	if !ok {
		t.Fatalf("expected BlobResourceContents for image/png, got %T", contents[0])
	}
	decoded, err := base64.StdEncoding.DecodeString(bc.Blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(binaryData) {
		t.Fatalf("binary data mismatch")
	}
}

func TestResourceHandler_RegisterForSession(t *testing.T) {
	store := session.New()
	store.Set("sess1", "user-abc")
	storage := newFakeStorage()
	storage.Put(context.Background(), "users", "user-abc/a.txt", strings.NewReader("a"), 1, "text/plain")
	storage.Put(context.Background(), "users", "user-abc/b.txt", strings.NewReader("b"), 1, "text/plain")

	mgr := NewResourceManager(storage, store)
	mcpServer := server.NewMCPServer("test", "1.0.0")
	handler := NewResourceHandler(mgr, mcpServer)

	// Register resources for the session
	resources, err := handler.RegisterForSession(context.Background(), "sess1")
	if err != nil {
		t.Fatal(err)
	}

	if len(resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(resources))
	}

	// Now read one of them via the read handler
	ctx := mcpServer.WithContext(context.Background(), newTestSession("sess1"))
	contents, err := handler.ReadHandler(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{URI: resources[0].URI},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(contents))
	}
}

// testSession implements server.ClientSession for testing.
type testSession struct {
	id string
	ch chan mcp.JSONRPCNotification
}

func newTestSession(id string) *testSession {
	return &testSession{id: id, ch: make(chan mcp.JSONRPCNotification, 10)}
}

func (s *testSession) SessionID() string                                   { return s.id }
func (s *testSession) Initialize()                                         {}
func (s *testSession) Initialized() bool                                   { return true }
func (s *testSession) NotificationChannel() chan<- mcp.JSONRPCNotification { return s.ch }
func (s *testSession) Close() error                                        { return nil }
