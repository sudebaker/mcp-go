// Package transport provides HTTP transport layer implementations for the MCP server.
//
// This package implements the server-side HTTP handlers for the MCP protocol,
// supporting both the legacy SSE (Server-Sent Events) transport and the modern
// Streamable HTTP specification. It wraps the mcp-go library server and adds
// middleware for CORS, rate limiting, request logging, and distributed tracing.
package transport

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rs/zerolog/log"
)

// Storage type constants for routing download requests.
// Local storage serves files from the server's filesystem.
// RustFS storage proxies requests to the S3-compatible object storage.
const (
	storageTypeLocal  = "local"
	storageTypeRustfs = "rustfs"
)

// DownloadHandler serves file download requests through the MCP server's
// proxy layer. It supports two storage backends: local filesystem and S3-compatible
// object storage (RustFS). Files are served through time-limited presigned URLs
// or direct proxying to maintain security isolation.
//
// Security features:
//   - Path traversal prevention via filepath.Base() and prefix validation
//   - Presigned URLs for RustFS with configurable expiry
//   - File age-based expiration for local downloads
type DownloadHandler struct {
	dataDir            string              // Base directory for local file storage
	defaultExpiryHours int                 // Default presigned URL lifetime in hours
}

// NewDownloadHandler creates a DownloadHandler with the specified expiry time.
//
// Args:
//   expiryHours: Presigned URL validity period in hours. Must be positive.
//     A value <= 0 defaults to 24 hours.
//
// Returns:
//   A configured DownloadHandler instance ready to serve download requests.
//
// Example:
//   handler := NewDownloadHandler(24) // 24-hour presigned URLs
func NewDownloadHandler(expiryHours int) *DownloadHandler {
	dataDir := os.Getenv("OUTPUT_DIR")
	if dataDir == "" {
		dataDir = "/data/reports"
	}

	if expiryHours <= 0 {
		expiryHours = 24
	}

	return &DownloadHandler{
		dataDir:            dataDir,
		defaultExpiryHours: expiryHours,
	}
}

// HandleDownload routes incoming download requests to the appropriate storage backend.
//
// The URL path format is: /download/{storage_type}/{path}
//   - /download/local/filename.pdf -> local filesystem
//   - /download/rustfs/bucket/objectkey -> S3-compatible storage
//
// Args:
//   w: HTTP response writer for writing the redirect or error response
//   r: HTTP request containing the download path
//
// Errors:
//   400 Bad Request: Invalid path format or missing storage type
//   404 Not Found: File does not exist (local storage only)
//   410 Gone: Download link has expired
//   503 Service Unavailable: RustFS endpoint not configured
func (h *DownloadHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/download/")
	if path == r.URL.Path || path == "" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "Invalid path format", http.StatusBadRequest)
		return
	}

	storageType := parts[0]
	filePath := parts[1]

	switch storageType {
	case storageTypeLocal:
		h.handleLocalDownload(w, r, filePath)
	case storageTypeRustfs:
		h.handleRustfsDownload(w, r, filePath)
	default:
		http.Error(w, "Unknown storage type", http.StatusBadRequest)
	}
}

// handleRustfsDownload generates a presigned URL for RustFS/S3 object access.
//
// Fetches credentials from environment variables and creates a temporary
// presigned URL that allows direct download from RustFS without exposing
// credentials to the client.
//
// Args:
//   w: HTTP response writer
//   r: HTTP request
//   path: Combined bucket/object path (format: "bucket/objectkey")
//
// Errors:
//   400 Bad Request: Missing bucket or object name
//   500 Internal Server Error: Failed to load AWS config or generate URL
//   503 Service Unavailable: RUSTFS_ENDPOINT not configured
func (h *DownloadHandler) handleRustfsDownload(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "Invalid RustFS path format (expected bucket/object)", http.StatusBadRequest)
		return
	}

	bucket := parts[0]
	object := parts[1]

	if bucket == "" || object == "" {
		http.Error(w, "Bucket and object name are required", http.StatusBadRequest)
		return
	}

	endpoint := os.Getenv("RUSTFS_ENDPOINT")
	if endpoint == "" {
		http.Error(w, "RustFS endpoint not configured", http.StatusServiceUnavailable)
		return
	}

	accessKey := os.Getenv("RUSTFS_ACCESS_KEY_ID")
	secretKey := os.Getenv("RUSTFS_SECRET_ACCESS_KEY")

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		log.Error().Err(err).Str("bucket", bucket).Str("object", object).Msg("Failed to load AWS config")
		http.Error(w, "Failed to connect to storage", http.StatusInternalServerError)
		return
	}

	customResolver := s3.EndpointResolverFromURL(getStorageURL(endpoint))

	client := s3.New(s3.Options{
		Region:           "us-east-1",
		Credentials:      cfg.Credentials,
		EndpointResolver: customResolver,
	})

	presignClient := s3.NewPresignClient(client)

	presignedReq, err := presignClient.PresignGetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(object),
	}, s3.WithPresignExpires(time.Duration(h.defaultExpiryHours)*time.Hour))
	if err != nil {
		log.Error().Err(err).Str("bucket", bucket).Str("object", object).Msg("Failed to generate presigned URL")
		http.Error(w, "Failed to generate download URL", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, presignedReq.URL, http.StatusTemporaryRedirect)
}

// handleLocalDownload serves files from the local filesystem.
//
// Security measures:
//   - filepath.Base() strips directory traversal attempts
//   - strings.HasPrefix() validates the resolved path stays within dataDir
//   - File age check rejects downloads of files older than expiry window
//
// Args:
//   w: HTTP response writer
//   r: HTTP request
//   filename: Name of the file to serve from dataDir
//
// Errors:
//   403 Forbidden: Path traversal attempt detected
//   404 Not Found: File doesn't exist or is a directory
//   410 Gone: File modification time exceeds expiry window
func (h *DownloadHandler) handleLocalDownload(w http.ResponseWriter, r *http.Request, filename string) {
	filename = filepath.Base(filename)
	filePath := filepath.Join(h.dataDir, filename)

	if !strings.HasPrefix(filePath, h.dataDir) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	if time.Since(info.ModTime()) > time.Duration(h.defaultExpiryHours)*time.Hour {
		http.Error(w, "Download link expired", http.StatusGone)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	http.ServeFile(w, r, filePath)
}

// GenerateLocalURL constructs a download URL for a local file.
//
// Args:
//   filename: Name of the file in the data directory
//
// Returns:
//   Full URL path: {BASE_URL}/download/local/{filename}
func (h *DownloadHandler) GenerateLocalURL(filename string) string {
	baseURL := getBaseURL()
	return fmt.Sprintf("%s/download/local/%s", baseURL, filepath.Base(filename))
}

// GenerateRustfsURL constructs a download URL for a RustFS/S3 object.
//
// Args:
//   bucket: S3 bucket name
//   object: Object key within the bucket
//
// Returns:
//   Full URL path: {BASE_URL}/download/rustfs/{bucket}/{object}
func (h *DownloadHandler) GenerateRustfsURL(bucket, object string) string {
	baseURL := getBaseURL()
	return fmt.Sprintf("%s/download/rustfs/%s/%s", baseURL, bucket, object)
}

// getBaseURL retrieves the public-facing BASE_URL from environment or defaults.
//
// Returns:
//   The configured BASE_URL with trailing slash removed, or "http://localhost:8080"
//   if not set.
func getBaseURL() string {
	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	return strings.TrimSuffix(baseURL, "/")
}

// getStorageURL normalizes a RustFS endpoint URL by extracting protocol
// from the endpoint string.
//
// Handles three formats:
//   - "rustfs:9000" -> "http://rustfs:9000"
//   - "http://rustfs:9000" -> "http://rustfs:9000"
//   - "https://rustfs:9000" -> "https://rustfs:9000"
//
// Args:
//   endpoint: RustFS endpoint URL or hostname
//
// Returns:
//   Normalized URL with explicit protocol prefix
func getStorageURL(endpoint string) string {
	protocol := "http"
	if strings.HasPrefix(endpoint, "https://") {
		protocol = "https"
		endpoint = strings.TrimPrefix(endpoint, "https://")
	} else if strings.HasPrefix(endpoint, "http://") {
		endpoint = strings.TrimPrefix(endpoint, "http://")
	}
	return fmt.Sprintf("%s://%s", protocol, endpoint)
}

// DownloadFile fetches a file from a URL and saves it to a local path.
//
// Used by tools that need to download external resources (e.g., presigned URLs
// from RustFS) and store them locally before processing.
//
// Args:
//   ctx: Context for cancellation and timeout
//   downloadURL: URL to fetch the file from
//   destPath: Local filesystem path to save the file
//
// Returns:
//   Error if the download fails, including:
//   - Failed to create HTTP request
//   - HTTP status code != 200
//   - Failed to create destination file
func DownloadFile(ctx context.Context, downloadURL string, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer out.Close()

	_, err = out.ReadFrom(resp.Body)
	return err
}