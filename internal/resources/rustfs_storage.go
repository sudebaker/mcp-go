package resources

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/rs/zerolog/log"
)

type RustFSStorage struct {
	client    *minio.Client
	publicURL string
}

// rustfsEndpoint normalizes a RUSTFS_ENDPOINT value into a host[:port] and the
// scheme-derived TLS flag. minio-go prepends the scheme itself, so the endpoint
// must be passed WITHOUT a scheme; passing "http://rustfs:9000" produces the
// malformed URL "http://http://rustfs:9000" and minio.New fails with
// "Endpoint url cannot have fully qualified paths."
func rustfsEndpoint(endpoint string) (host string, secure bool, err error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", false, nil
	}
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return "", false, fmt.Errorf("parse RUSTFS_ENDPOINT %q: %w", endpoint, err)
		}
		if u.Host == "" {
			return "", false, fmt.Errorf("RUSTFS_ENDPOINT %q has no host", endpoint)
		}
		return u.Host, u.Scheme == "https", nil
	}
	// Handle "host:port/path" without a scheme (drop the path).
	if idx := strings.Index(endpoint, "/"); idx > 0 {
		endpoint = endpoint[:idx]
	}
	return endpoint, false, nil
}

func NewRustFSStorage() (*RustFSStorage, error) {
	endpoint := os.Getenv("RUSTFS_ENDPOINT")
	if endpoint == "" {
		endpoint = "rustfs:9000"
	}
	host, secureFromURL, err := rustfsEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	accessKey := os.Getenv("RUSTFS_ACCESS_KEY_ID")
	secretKey := os.Getenv("RUSTFS_SECRET_ACCESS_KEY")
	useSSL := secureFromURL
	if v := os.Getenv("RUSTFS_USE_SSL"); v != "" {
		useSSL, _ = strconv.ParseBool(v)
	}

	client, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, err
	}

	// Ensure the default bucket exists with retry (RustFS may not be ready yet)
	bucket := defaultBucket
	if err := ensureBucketWithRetry(client, bucket); err != nil {
		return nil, err
	}

	return &RustFSStorage{
		client:    client,
		publicURL: os.Getenv("RUSTFS_PUBLIC_URL"),
	}, nil
}

// ensureBucketWithRetry checks if the bucket exists and creates it if not,
// retrying up to 30 seconds with 1-second intervals.
func ensureBucketWithRetry(client *minio.Client, bucket string) error {
	ctx := context.Background()
	var lastErr error
	for i := 0; i < 30; i++ {
		exists, err := client.BucketExists(ctx, bucket)
		if err == nil {
			if !exists {
				if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
					return fmt.Errorf("create bucket %q: %w", bucket, err)
				}
			}
			return nil
		}
		lastErr = err
		if i < 29 {
			log.Warn().Err(err).Str("bucket", bucket).Int("attempt", i+1).Msg("Failed to check bucket, retrying")
			time.Sleep(time.Second)
		}
	}
	return fmt.Errorf("check bucket %q after 30 attempts: %w", bucket, lastErr)
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
