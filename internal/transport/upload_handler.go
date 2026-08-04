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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sudebaker/mcp-go/internal/resources"
)

var (
	cleanupRestartCount atomic.Int32
	cleanupDisabled     atomic.Bool
)

// UploadConfig holds configuration for the upload endpoint.
type UploadConfig struct {
	// Enabled indicates if upload endpoint is active (default: true)
	Enabled bool
	// MaxSizeMB is the maximum file size in megabytes (default: 50)
	MaxSizeMB int64
	// AllowedTypes is the whitelist of MIME types
	AllowedTypes []string
	// DefaultTTLSeconds is the default time-to-live for uploaded files (default: 3600)
	DefaultTTLSeconds int
	// MaxTTLSeconds is the maximum TTL a client can request (default: 86400)
	MaxTTLSeconds int
	// UploadDir is the base directory for storing uploads (default: /data/uploads)
	UploadDir string
}

// DefaultUploadConfig returns sensible defaults for upload configuration.
func DefaultUploadConfig() UploadConfig {
	return UploadConfig{
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
		DefaultTTLSeconds: 3600,
		MaxTTLSeconds:     86400,
		UploadDir:         "/data/uploads",
	}
}

// UploadResponse is the JSON response for successful uploads.
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
			"text/plain",
			"text/yaml",
			"application/json",
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
	// text/plain and variants are detected as text/plain; charset=utf-8
	if declared == "text/plain" && strings.HasPrefix(detected, "text/plain") {
		return true
	}
	// text/csv is detected as text/plain; charset=utf-8 by Go's http.DetectContentType
	if declared == "text/csv" && strings.HasPrefix(detected, "text/plain") {
		return true
	}
	// text/yaml is detected as text/plain; charset=utf-8
	if declared == "text/yaml" && strings.HasPrefix(detected, "text/plain") {
		return true
	}
	// application/json is detected as text/plain; charset=utf-8
	if declared == "application/json" && strings.HasPrefix(detected, "text/plain") {
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

// startUploadCleanup runs a background goroutine that periodically scans
// the upload directory and removes files whose TTL has expired.
func (s *MCPServer) startUploadCleanup() {
	if cleanupDisabled.Load() {
		log.Warn().Msg("Upload cleanup previously disabled after repeated panics, not restarting")
		return
	}

	defer func() {
		if r := recover(); r != nil {
			count := cleanupRestartCount.Add(1)
			if count > 10 {
				log.Error().
					Int32("restart_count", count).
					Msg("Upload cleanup failed 10+ times, disabling permanently")
				cleanupDisabled.Store(true)
				return
			}
			delay := time.Duration(min(5*count, 300)) * time.Second
			log.Error().
				Interface("panic", r).
				Int32("restart_count", count).
				Dur("delay", delay).
				Msg("Upload cleanup panicked, restarting with delay")
			time.Sleep(delay)
			go s.startUploadCleanup()
		}
	}()

	cfg := s.uploadConfig
	if cfg.UploadDir == "" {
		cfg.UploadDir = "/data/uploads"
	}
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	log.Info().Str("dir", cfg.UploadDir).Msg("Upload TTL cleanup goroutine started")

	for {
		select {
		case <-ticker.C:
			s.cleanExpiredUploads(cfg.UploadDir)
		case <-s.stopCh:
			log.Debug().Msg("Upload cleanup goroutine stopped")
			return
		}
	}
}

// cleanExpiredUploads removes uploaded files and their .meta sidecars whose
// expiration time has passed.
func (s *MCPServer) cleanExpiredUploads(uploadDir string) {
	now := time.Now()
	removed := 0
	errors := 0

	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn().Err(err).Str("dir", uploadDir).Msg("Failed to scan upload directory")
		}
		return
	}

	for _, entry := range entries {
		// Skip .meta sidecar files (we clean them alongside their parent)
		if strings.HasSuffix(entry.Name(), ".meta") {
			continue
		}

		metaPath := filepath.Join(uploadDir, entry.Name()+".meta")
		metaBytes, err := os.ReadFile(metaPath)
		if err != nil {
			// No .meta file — legacy file without metadata, skip
			continue
		}

		var meta map[string]string
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			continue
		}

		expiresAtStr, ok := meta["expires_at"]
		if !ok {
			continue
		}

		expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
		if err != nil {
			continue
		}

		if now.After(expiresAt) {
			filePath := filepath.Join(uploadDir, entry.Name())
			if err := os.Remove(filePath); err != nil {
				errors++
				log.Warn().Err(err).Str("path", filePath).Msg("Failed to remove expired upload")
			} else {
				os.Remove(metaPath) // best-effort sidecar cleanup
				removed++
			}
		}
	}

	// Recurse into collection subdirectories (depth 1 only)
	for _, entry := range entries {
		if entry.IsDir() {
			subDir := filepath.Join(uploadDir, entry.Name())
			subEntries, err := os.ReadDir(subDir)
			if err != nil {
				continue
			}
			for _, subEntry := range subEntries {
				if strings.HasSuffix(subEntry.Name(), ".meta") {
					continue
				}
				metaPath := filepath.Join(subDir, subEntry.Name()+".meta")
				metaBytes, err := os.ReadFile(metaPath)
				if err != nil {
					continue
				}
				var meta map[string]string
				if json.Unmarshal(metaBytes, &meta) != nil {
					continue
				}
				expiresAtStr, ok := meta["expires_at"]
				if !ok {
					continue
				}
				expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
				if err != nil {
					continue
				}
				if now.After(expiresAt) {
					filePath := filepath.Join(subDir, subEntry.Name())
					if os.Remove(filePath) == nil {
						os.Remove(metaPath)
						removed++
					} else {
						errors++
					}
				}
			}
		}
	}

	if removed > 0 || errors > 0 {
		log.Info().Int("removed", removed).Int("errors", errors).Msg("Upload TTL cleanup completed")
	}
}
