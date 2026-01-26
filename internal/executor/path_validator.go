package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrPathTraversal = errors.New("path traversal attempt detected")
	ErrSymlinkLoop   = errors.New("symlink loop detected")
	ErrInvalidPath   = errors.New("invalid or inaccessible path")
)

const maxSymlinkDepth = 40

func ValidatePath(path string, allowedDir string) error {
	if path == "" || allowedDir == "" {
		return ErrInvalidPath
	}

	cleanedPath := filepath.Clean(path)
	cleanedAllowed := filepath.Clean(allowedDir)

	if !filepath.IsAbs(cleanedPath) {
		cleanedPath = filepath.Join(cleanedAllowed, cleanedPath)
	}

	rel, err := filepath.Rel(cleanedAllowed, cleanedPath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPathTraversal, err)
	}

	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return ErrPathTraversal
	}

	allowedResolved, err := filepath.EvalSymlinks(cleanedAllowed)
	if err != nil {
		return fmt.Errorf("failed to resolve allowed directory: %w", err)
	}

	resolved, err := resolveAllSymlinks(cleanedPath)
	if err != nil {
		if os.IsNotExist(err) {
			absPath, absErr := filepath.Abs(cleanedPath)
			if absErr != nil {
				return absErr
			}
			relCheck, relErr := filepath.Rel(allowedResolved, absPath)
			if relErr != nil {
				return fmt.Errorf("%w: %v", ErrPathTraversal, relErr)
			}
			if strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) || relCheck == ".." {
				return ErrPathTraversal
			}
			return nil
		}
		return err
	}

	relResolved, err := filepath.Rel(allowedResolved, resolved)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPathTraversal, err)
	}

	if strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) || relResolved == ".." {
		return ErrPathTraversal
	}

	return nil
}

func resolveSymlinksWithDepth(path string, depth int) (string, error) {
	if depth > maxSymlinkDepth {
		return "", ErrSymlinkLoop
	}

	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}

	if info.Mode()&os.ModeSymlink == 0 {
		return filepath.Abs(path)
	}

	link, err := os.Readlink(path)
	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}

	resolved, err := resolveSymlinksWithDepth(link, depth+1)
	if err != nil {
		return "", err
	}

	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}

	return absPath, nil
}

func resolveAllSymlinks(path string) (string, error) {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	current := ""

	if filepath.IsAbs(path) {
		current = string(filepath.Separator)
	}

	depth := 0
	for _, part := range parts {
		if part == "" {
			continue
		}

		current = filepath.Join(current, part)

		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.Abs(current)
			}
			return "", err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if depth > maxSymlinkDepth {
				return "", ErrSymlinkLoop
			}
			depth++

			link, err := os.Readlink(current)
			if err != nil {
				return "", err
			}

			if !filepath.IsAbs(link) {
				link = filepath.Join(filepath.Dir(current), link)
			}

			current = link
		}
	}

	return filepath.Abs(current)
}
