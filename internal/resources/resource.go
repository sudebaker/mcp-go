package resources

import "io"

type Resource struct {
	URI      string
	Name     string
	MIMEType string
	Size     int64
	SHA256   string
	Reader   io.ReadCloser
}

func (r *Resource) Close() error {
	if r.Reader != nil {
		return r.Reader.Close()
	}
	return nil
}
