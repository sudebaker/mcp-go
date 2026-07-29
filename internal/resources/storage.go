package resources

import (
	"context"
	"io"
	"time"
)

type Storage interface {
	Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) (ObjectInfo, error)
	Open(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	Stat(ctx context.Context, bucket, key string) (ObjectInfo, error)
	List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error)
	Delete(ctx context.Context, bucket, key string) error
}

type ObjectInfo struct {
	Key          string
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
}
