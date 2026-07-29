package resources

import (
	"os"
	"testing"
)

func TestNewRustFSStorage_DefaultEndpoint(t *testing.T) {
	envVars := []string{
		"RUSTFS_ENDPOINT",
		"RUSTFS_ACCESS_KEY_ID",
		"RUSTFS_SECRET_ACCESS_KEY",
		"RUSTFS_USE_SSL",
		"RUSTFS_PUBLIC_URL",
	}

	for _, key := range envVars {
		os.Unsetenv(key)
	}
	defer func() {
		for _, key := range envVars {
			os.Unsetenv(key)
		}
	}()

	storage, err := NewRustFSStorage()
	if err != nil {
		t.Fatalf("NewRustFSStorage() unexpected error: %v", err)
	}
	if storage == nil {
		t.Fatal("NewRustFSStorage() returned nil storage")
	}
}

func TestNewRustFSStorage_EnvironmentVariables(t *testing.T) {
	envVars := map[string]string{
		"RUSTFS_ENDPOINT":          "custom.example.com:9000",
		"RUSTFS_ACCESS_KEY_ID":     "test-access-key",
		"RUSTFS_SECRET_ACCESS_KEY": "test-secret-key",
		"RUSTFS_USE_SSL":           "true",
		"RUSTFS_PUBLIC_URL":        "https://files.example.com",
	}

	previous := make(map[string]string)
	for key, value := range envVars {
		previous[key] = os.Getenv(key)
		os.Setenv(key, value)
	}
	defer func() {
		for key, value := range previous {
			if value == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, value)
			}
		}
	}()

	storage, err := NewRustFSStorage()
	if err != nil {
		t.Fatalf("NewRustFSStorage() unexpected error: %v", err)
	}
	if storage == nil {
		t.Fatal("NewRustFSStorage() returned nil storage")
	}
	if storage.publicURL != "https://files.example.com" {
		t.Errorf("publicURL = %q, want %q", storage.publicURL, "https://files.example.com")
	}
}

var _ Storage = (*RustFSStorage)(nil)
