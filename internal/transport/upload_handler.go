// Package transport provides HTTP transport layer implementations for the MCP server.
// This file implements the file upload handler for POST /upload.

package transport

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/sudebaker/mcp-go/internal/resources"
)

// UploadResponse is the JSON response for upload operations.
type UploadResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// generateRandomFilename creates a unique filename with the original extension.
func generateRandomFilename(originalName string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, bytes); err != nil {
		return "", err
	}
	ext := filepath.Ext(originalName)
	return hex.EncodeToString(bytes) + ext, nil
}

// sanitizeFilename removes dangerous path components from the filename.
func sanitizeFilename(name string) string {
	// Remove path separators and parent directory references
	name = filepath.Base(name)
	// Remove null bytes
	name = strings.ReplaceAll(name, "\x00", "")
	// Limit length
	if len(name) > 255 {
		name = name[:255]
	}
	return name
}

// handleUpload handles POST /upload requests for file uploads.
//
// Request:
//   - Content-Type: multipart/form-data
//   - Header: X-Session-ID (required) - identifies the authenticated session
//   - Field: file (required) - the binary file
//
// Response (200):
//
//	{"success": true, "uri": "res://...", "sha256": "...", "size": N, "content_type": "...", "name": "..."}
//
// Response (413):
//
//	{"success": false, "error": "File exceeds maximum size limit (50MB)"}
//
// Response (415):
//
//	{"success": false, "error": "Unsupported content type: ..."}
func (s *MCPServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.Header.Get("X-Session-ID")
	if sessionID == "" {
		http.Error(w, "Missing X-Session-ID", http.StatusUnauthorized)
		return
	}

	// Get upload config from server (use defaults if not configured)
	cfg := s.uploadConfig
	if cfg.MaxSizeMB == 0 {
		cfg.MaxSizeMB = 50
	}
	if len(cfg.AllowedTypes) == 0 {
		cfg.AllowedTypes = []string{
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
		}
	}

	// Parse multipart form with max size limit
	maxSize := cfg.MaxSizeMB * 1024 * 1024
	if err := r.ParseMultipartForm(maxSize); err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			json.NewEncoder(w).Encode(UploadResponse{
				Success: false,
				Error:   fmt.Sprintf("File exceeds maximum size limit (%dMB)", cfg.MaxSizeMB),
			})
			return
		}
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// Get file from form
	file, header, err := r.FormFile("file")
	if err != nil {
		if err == http.ErrMissingFile {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(UploadResponse{
				Success: false,
				Error:   "Missing required field: file",
			})
			return
		}
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Validate content type
	contentType := header.Header.Get("Content-Type")
	allowed := false
	for _, t := range cfg.AllowedTypes {
		if contentType == t {
			allowed = true
			break
		}
	}
	if !allowed {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		json.NewEncoder(w).Encode(UploadResponse{
			Success: false,
			Error:   fmt.Sprintf("Unsupported content type: %s. Allowed: %s", contentType, strings.Join(cfg.AllowedTypes, ", ")),
		})
		return
	}

	// Sanitize filename
	originalFilename := sanitizeFilename(header.Filename)
	if originalFilename == "" {
		originalFilename = "upload"
	}

	// SECURITY: Verify magic bytes match declared Content-Type
	// Read first 512 bytes to detect actual content type
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		log.Error().Err(err).Msg("Failed to read file header for magic byte detection")
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}

	// Detect actual content type from magic bytes
	detectedType := http.DetectContentType(buffer[:n])
	if !matchMIME(contentType, detectedType) {
		log.Warn().
			Str("declared", contentType).
			Str("detected", detectedType).
			Str("filename", originalFilename).
			Msg("MIME type mismatch - declared type does not match magic bytes")
		// Reject upload if types don't match - prevents MIME spoofing attacks
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		json.NewEncoder(w).Encode(UploadResponse{
			Success: false,
			Error:   fmt.Sprintf("Content type mismatch: declared %s but detected %s", contentType, detectedType),
		})
		return
	}

	// Combine buffer and remaining file content for writing
	fileReader := io.MultiReader(bytes.NewReader(buffer[:n]), file)

	// Generate unique filename
	uniqueName, err := generateRandomFilename(originalFilename)
	if err != nil {
		http.Error(w, "Failed to generate filename", http.StatusInternalServerError)
		return
	}

	// Wrap the stream so we can compute SHA256 while it is being stored.
	hasher := sha256.New()
	teeReader := io.TeeReader(fileReader, hasher)

	// Enforce the configured size limit while still streaming. The limit reader
	// allows one extra byte so we can detect when the upload exceeds the limit.
	limitedReader := io.LimitReader(teeReader, maxSize+1)

	res, err := s.resourceManager.PutForUser(r.Context(), sessionID, uniqueName, limitedReader, -1, contentType)
	if err != nil {
		if errors.Is(err, resources.ErrUnauthenticated) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(UploadResponse{
				Success: false,
				Error:   "Missing or invalid X-Session-ID",
			})
			return
		}
		log.Error().Err(err).Msg("Failed to upload to storage")
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	if res.Size > maxSize {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		json.NewEncoder(w).Encode(UploadResponse{
			Success: false,
			Error:   fmt.Sprintf("File exceeds maximum size limit (%dMB)", cfg.MaxSizeMB),
		})
		return
	}

	sha := hex.EncodeToString(hasher.Sum(nil))

	log.Info().
		Str("uri", res.URI).
		Str("content_type", contentType).
		Int64("size", res.Size).
		Str("sha256", sha).
		Msg("File uploaded successfully")

	// Send success response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"success":      true,
		"uri":          res.URI,
		"sha256":       sha,
		"size":         res.Size,
		"content_type": res.MIMEType,
		"name":         originalFilename,
	})
}

// matchMIME compares declared vs detected content type with relaxed rules
// for types that Go's DetectContentType cannot distinguish.
func matchMIME(declared, detected string) bool {
	if declared == detected {
		return true
	}
	// text/csv is detected as text/plain; charset=utf-8 by Go's http.DetectContentType
	if declared == "text/csv" && strings.HasPrefix(detected, "text/plain") {
		return true
	}
	// .xlsx (ZIP-based) is detected as application/zip
	if declared == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" && detected == "application/zip" {
		return true
	}
	// .xls (OLE2-based) is detected as application/octet-stream
	if declared == "application/vnd.ms-excel" && detected == "application/octet-stream" {
		return true
	}
	return false
}
