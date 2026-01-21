package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePath_Valid(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test_allowed")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	err = ValidatePath(testFile, tmpDir)
	if err != nil {
		t.Errorf("Expected no error for valid path, got: %v", err)
	}
}

func TestValidatePath_TraversalAttempt(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test_allowed")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	blockedDir, err := os.MkdirTemp("", "test_blocked")
	if err != nil {
		t.Fatalf("Failed to create blocked dir: %v", err)
	}
	defer os.RemoveAll(blockedDir)

	testFile := filepath.Join(blockedDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	err = ValidatePath(testFile, tmpDir)
	if err != ErrPathTraversal {
		t.Errorf("Expected ErrPathTraversal, got: %v", err)
	}
}

func TestValidatePath_SymlinkOutside(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test_allowed")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	externalDir, err := os.MkdirTemp("", "test_external")
	if err != nil {
		t.Fatalf("Failed to create external dir: %v", err)
	}
	defer os.RemoveAll(externalDir)

	testFile := filepath.Join(externalDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	symlinkPath := filepath.Join(tmpDir, "symlink")
	if err := os.Symlink(externalDir, symlinkPath); err != nil {
		t.Fatalf("Failed to create symlink: %v", err)
	}

	symlinkFile := filepath.Join(symlinkPath, "test.txt")
	err = ValidatePath(symlinkFile, tmpDir)
	if err != ErrPathTraversal {
		t.Errorf("Expected ErrPathTraversal for symlink outside, got: %v", err)
	}
}

func TestValidatePath_FileNotExists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test_allowed")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	nonExistentFile := filepath.Join(tmpDir, "nonexistent.txt")

	err = ValidatePath(nonExistentFile, tmpDir)
	if err == nil {
		t.Error("Expected error for non-existent file")
	}
}
