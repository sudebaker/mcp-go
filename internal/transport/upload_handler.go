// Package transport provides HTTP transport layer implementations for the MCP server.
// This file implements the file upload handler for POST /upload.

package transport

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
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
	Success     bool   `json:"success"`
	Path        string `json:"path,omitempty"`
	Filename    string `json:"filename,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Error       string `json:"error,omitempty"`
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
//   - Field: file (required) - the binary file
//   - Field: ttl (optional) - time-to-live in seconds (default: 3600)
//   - Field: collection (optional) - subdirectory for organization
//
// Response (200):
//
//	{"success": true, "path": "/data/uploads/abc123.jpg", "filename": "...", "size": N, "content_type": "...", "expires_at": "..."}
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

	// Get upload config from server (use defaults if not configured)
	cfg := s.uploadConfig
	if cfg.MaxSizeMB == 0 {
		cfg.MaxSizeMB = 50
	}
	if len(cfg.AllowedTypes) == 0 {
		cfg.AllowedTypes = DefaultUploadConfig().AllowedTypes
	}
	if cfg.DefaultTTLSeconds == 0 {
		cfg.DefaultTTLSeconds = 3600
	}
	if cfg.MaxTTLSeconds == 0 {
		cfg.MaxTTLSeconds = 86400
	}
	if cfg.UploadDir == "" {
		cfg.UploadDir = "/data/uploads"
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
	if detectedType != contentType {
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

	// Get TTL from form (optional)
	ttl := cfg.DefaultTTLSeconds
	if ttlStr := r.FormValue("ttl"); ttlStr != "" {
		var parsed int
		if _, err := fmt.Sscanf(ttlStr, "%d", &parsed); err == nil {
			if parsed > cfg.MaxTTLSeconds {
				parsed = cfg.MaxTTLSeconds
			}
			if parsed > 0 {
				ttl = parsed
			}
		}
	}

	// Get collection from form (optional)
	collection := strings.TrimSpace(r.FormValue("collection"))
	if collection != "" {
		// Sanitize collection name - only allow alphanumeric and hyphens
		collection = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				return r
			}
			return -1
		}, collection)
		// SECURITY: Limit collection name length and reject path separators
		if len(collection) > 64 || strings.Contains(collection, "..") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(UploadResponse{
				Success: false,
				Error:   "Invalid collection name: max 64 chars, no path traversal",
			})
			return
		}
	}

	// Build upload path
	uploadDir := cfg.UploadDir
	if collection != "" {
		uploadDir = filepath.Join(uploadDir, collection)
	}

	// Ensure directory exists
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		log.Error().Err(err).Str("dir", uploadDir).Msg("Failed to create upload directory")
		http.Error(w, "Failed to create upload directory", http.StatusInternalServerError)
		return
	}

	// Create destination file
	destPath := filepath.Join(uploadDir, uniqueName)
	destFile, err := os.Create(destPath)
	if err != nil {
		log.Error().Err(err).Str("path", destPath).Msg("Failed to create destination file")
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	defer destFile.Close()

	// Copy file content
	written, err := io.Copy(destFile, fileReader)
	if err != nil {
		log.Error().Err(err).Msg("Failed to write file content")
		os.Remove(destPath) // Clean up partial file
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	// Calculate expiration time
	expiresAt := time.Now().Add(time.Duration(ttl) * time.Second)

	// Write expiration metadata as sidecar file for the cleanup goroutine
	metaPath := destPath + ".meta"
	metaData := map[string]string{
		"expires_at":    expiresAt.Format(time.RFC3339),
		"content_type":  contentType,
		"original_name": originalFilename,
	}
	metaJSON, _ := json.Marshal(metaData)
	if err := os.WriteFile(metaPath, metaJSON, 0644); err != nil {
		log.Warn().Err(err).Str("path", metaPath).Msg("Failed to write upload metadata (file won't be auto-cleaned)")
	}

	log.Info().
		Str("path", destPath).
		Str("content_type", contentType).
		Int64("size", written).
		Time("expires_at", expiresAt).
		Msg("File uploaded successfully")

	// Send success response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(UploadResponse{
		Success:     true,
		Path:        destPath,
		Filename:    originalFilename,
		Size:        written,
		ContentType: contentType,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
	})
}

// startUploadCleanup runs a background goroutine that periodically scans
// the upload directory and removes files whose TTL has expired.
func (s *MCPServer) startUploadCleanup() {
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
