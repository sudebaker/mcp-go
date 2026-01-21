package executor

import (
	"errors"
	"path/filepath"
	"strings"
)

var ErrPathTraversal = errors.New("path traversal attempt detected")

func ValidatePath(path string, allowedDir string) error {
	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return err
	}
	allowedResolved, err := filepath.EvalSymlinks(allowedDir)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resolved, allowedResolved) {
		return ErrPathTraversal
	}
	return nil
}
