package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/resources"
	"github.com/sudebaker/mcp-go/internal/session"
)

func setupUploadServer(t *testing.T) (*MCPServer, *fakeStorage, *session.Store) {
	t.Helper()

	store := session.New()
	store.Set("session-123", "user-abc")

	storage := newFakeStorage()
	mgr := resources.NewResourceManager(storage, store)

	mcpServer := server.NewMCPServer("test", "1.0.0")
	srv := NewMCPServer(mcpServer, MCPConfig{
		Host: "127.0.0.1",
		Port: 0,
		Upload: config.UploadConfig{
			Enabled:   true,
			MaxSizeMB: 50,
			AllowedTypes: []string{
				"image/jpeg",
				"image/png",
				"image/webp",
				"image/gif",
				"application/pdf",
				"audio/mpeg",
				"audio/wav",
				"audio/ogg",
				"audio/webm",
				"audio/flac",
				"text/csv",
				"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
				"application/vnd.ms-excel",
			},
		},
	})
	srv.SetResourceManager(mgr)

	return srv, storage, store
}

func makeUploadRequest(t *testing.T, srv *MCPServer, sessionID, fieldName, fileName, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, fileName))
	partHeader.Set("Content-Type", contentType)
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

	rr := httptest.NewRecorder()
	srv.authMiddleware(srv.handleUpload).ServeHTTP(rr, req)
	return rr
}

func TestUpload_Success_ReturnsResURI(t *testing.T) {
	srv, storage, _ := setupUploadServer(t)

	body := validPNG()
	expectedSHA := sha256.Sum256(body)

	rr := makeUploadRequest(t, srv, "session-123", "file", "report.png", "image/png", body)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp["success"] != true {
		t.Fatalf("expected success true, got %v", resp["success"])
	}
	uri, ok := resp["uri"].(string)
	if !ok || !strings.HasPrefix(uri, "res://") {
		t.Fatalf("expected res:// uri, got %v", resp["uri"])
	}
	if resp["sha256"] != hex.EncodeToString(expectedSHA[:]) {
		t.Fatalf("expected sha256 %s, got %v", hex.EncodeToString(expectedSHA[:]), resp["sha256"])
	}
	if resp["size"] != float64(len(body)) {
		t.Fatalf("expected size %d, got %v", len(body), resp["size"])
	}
	if resp["content_type"] != "image/png" {
		t.Fatalf("expected content_type image/png, got %v", resp["content_type"])
	}
	if resp["name"] != "report.png" {
		// The opaque URI path uses the randomized storage key; the human-readable
		// original filename is returned in the response.
		t.Fatalf("expected name report.png, got %v", resp["name"])
	}

	// Verify the object was actually stored.
	if len(storage.objects) != 1 {
		t.Fatalf("expected 1 stored object, got %d", len(storage.objects))
	}
	for _, obj := range storage.data {
		if string(obj) != string(body) {
			t.Fatalf("stored content mismatch")
		}
	}
}

func TestUpload_MissingSessionID(t *testing.T) {
	srv, _, _ := setupUploadServer(t)

	rr := makeUploadRequest(t, srv, "", "file", "report.png", "image/png", validPNG())

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUpload_InvalidSessionID(t *testing.T) {
	srv, _, _ := setupUploadServer(t)

	rr := makeUploadRequest(t, srv, "unknown-session", "file", "report.png", "image/png", validPNG())

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUpload_MissingFile(t *testing.T) {
	srv, _, _ := setupUploadServer(t)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Session-ID", "session-123")

	rr := httptest.NewRecorder()
	srv.authMiddleware(srv.handleUpload).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Missing required field") {
		t.Fatalf("expected missing file error, got %s", rr.Body.String())
	}
}

func TestUpload_UnsupportedContentType(t *testing.T) {
	srv, _, _ := setupUploadServer(t)

	body := []byte("plain text")
	rr := makeUploadRequest(t, srv, "session-123", "file", "note.txt", "text/plain", body)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUpload_MIMEMagicMismatch(t *testing.T) {
	srv, _, _ := setupUploadServer(t)

	// Declared as a JPEG but content is a real PNG.
	rr := makeUploadRequest(t, srv, "session-123", "file", "fake.jpg", "image/jpeg", validPNG())

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Content type mismatch") {
		t.Fatalf("expected content type mismatch error, got %s", rr.Body.String())
	}
}

func TestUpload_SizeLimitExceeded(t *testing.T) {
	srv, _, _ := setupUploadServer(t)

	png := validPNG()
	// Build a body clearly larger than the 1MB limit using valid PNG bytes so
	// the MIME magic check passes before the size limit is enforced.
	large := bytes.Repeat(png, 5*(1024*1024/len(png))+1)
	srv.uploadConfig.MaxSizeMB = 1

	rr := makeUploadRequest(t, srv, "session-123", "file", "big.png", "image/png", large)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rr.Code, rr.Body.String())
	}
}

// validPNG returns a small 1x1 PNG whose magic bytes are detected as image/png.
func validPNG() []byte {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAACklEQVR4nGMAAAABAgAFDQAAABpJREFUeJxjZGRkZAAAAAAEAAEKTJHV/wAAAABJRU5ErkJggg==")
	if err != nil {
		panic(err)
	}
	return data
}
