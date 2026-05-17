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
		Enabled:           true,
		MaxSizeMB:         50,
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
//   {"success": true, "path": "/data/uploads/abc123.jpg", "filename": "...", "size": N, "content_type": "...", "expires_at": "..."}
//
// Response (413):
//   {"success": false, "error": "File exceeds maximum size limit (50MB)"}
//
// Response (415):
//   {"success": false, "error": "Unsupported content type: ..."}
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

	// SECURITY: Verify magic bytes match declared Content-Type
	// Read first 512 bytes to detect actual content type
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		log.Error().Err(err).Msg("Failed to read file header for magic byte detection")
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}
	// Reset file pointer to beginning
	file = io.MultiReader(bytes.NewReader(buffer[:n]), file)
	
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

	// Sanitize filename
	originalFilename := sanitizeFilename(header.Filename)
	if originalFilename == "" {
		originalFilename = "upload"
	}

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
	written, err := io.Copy(destFile, file)
	if err != nil {
		log.Error().Err(err).Msg("Failed to write file content")
		os.Remove(destPath) // Clean up partial file
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	// Calculate expiration time
	expiresAt := time.Now().Add(time.Duration(ttl) * time.Second)

	// Register file for cleanup - store metadata for background cleanup goroutine
	// TODO: Implement background cleanup goroutine in Start() that scans uploadDir
	// and removes files older than their expires_at timestamp.
	// For now, files persist until manual cleanup or disk pressure.

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
