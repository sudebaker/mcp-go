package resources

import (
	"errors"
	"io"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.EOF }
func (failingReader) Close() error             { return errors.New("close failed") }

func TestResource_Close(t *testing.T) {
	r := &Resource{Reader: failingReader{}}
	err := r.Close()
	if err == nil || err.Error() != "close failed" {
		t.Fatalf("expected close error, got %v", err)
	}
}

func TestResource_Close_NilReader(t *testing.T) {
	r := &Resource{}
	if err := r.Close(); err != nil {
		t.Fatalf("expected nil error for nil reader, got %v", err)
	}
}
