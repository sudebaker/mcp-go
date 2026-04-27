package transport

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/rs/zerolog/log"
)

const (
	storageTypeLocal  = "local"
	storageTypeRustfs = "rustfs"
)

type DownloadHandler struct {
	dataDir            string
	defaultExpiryHours int
}

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
	useSSL := os.Getenv("RUSTFS_USE_SSL") == "true"

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		log.Error().Err(err).Str("bucket", bucket).Str("object", object).Msg("Failed to create Minio client")
		http.Error(w, "Failed to connect to storage", http.StatusInternalServerError)
		return
	}

	presignedURL, err := client.PresignedGetObject(r.Context(), bucket, object, time.Duration(h.defaultExpiryHours)*time.Hour, nil)
	if err != nil {
		log.Error().Err(err).Str("bucket", bucket).Str("object", object).Msg("Failed to generate presigned URL")
		http.Error(w, "Failed to generate download URL", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, presignedURL.String(), http.StatusTemporaryRedirect)
}

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

func (h *DownloadHandler) GenerateLocalURL(filename string) string {
	baseURL := getBaseURL()
	return fmt.Sprintf("%s/download/local/%s", baseURL, filepath.Base(filename))
}

func (h *DownloadHandler) GenerateRustfsURL(bucket, object string) string {
	baseURL := getBaseURL()
	return fmt.Sprintf("%s/download/rustfs/%s/%s", baseURL, bucket, object)
}

func getBaseURL() string {
	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	return strings.TrimSuffix(baseURL, "/")
}

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
