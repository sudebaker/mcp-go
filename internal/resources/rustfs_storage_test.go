package resources

import (
	"net"
	"os"
	"testing"
	"time"
)

func skipIfNoRustFS(t *testing.T) {
	t.Helper()
	// If RUSTFS_TEST_SKIP is set, skip this test (no RustFS available in unit test env)
	if os.Getenv("RUSTFS_TEST_SKIP") != "" {
		t.Skip("Skipping RustFS integration test (RUSTFS_TEST_SKIP set)")
	}
	// Try to connect to RustFS
	endpoint := os.Getenv("RUSTFS_ENDPOINT")
	if endpoint == "" {
		endpoint = "rustfs:9000"
	}
	// Quick DNS check to see if RustFS is reachable
	_, err := net.DialTimeout("tcp", endpoint, 100*time.Millisecond)
	if err != nil {
		t.Skip("Skipping RustFS integration test: " + err.Error())
	}
}

func TestNewRustFSStorage_DefaultEndpoint(t *testing.T) {
	skipIfNoRustFS(t)
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
	skipIfNoRustFS(t)
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

func TestRustfsEndpoint_Normalization(t *testing.T) {
	cases := []struct {
		in     string
		host   string
		secure bool
	}{
		{"rustfs:9000", "rustfs:9000", false},
		{"http://rustfs:9000", "rustfs:9000", false},
		{"https://rustfs:9000", "rustfs:9000", true},
		{"http://192.168.52.40:9000", "192.168.52.40:9000", false},
		{"https://files.example.com:9000", "files.example.com:9000", true},
		{"rustfs:9000/path", "rustfs:9000", false},
	}
	for _, tc := range cases {
		host, secure, err := rustfsEndpoint(tc.in)
		if err != nil {
			t.Fatalf("rustfsEndpoint(%q) unexpected error: %v", tc.in, err)
		}
		if host != tc.host {
			t.Errorf("rustfsEndpoint(%q) host = %q, want %q", tc.in, host, tc.host)
		}
		if secure != tc.secure {
			t.Errorf("rustfsEndpoint(%q) secure = %v, want %v", tc.in, secure, tc.secure)
		}
	}
}

func TestRustfsEndpoint_Errors(t *testing.T) {
	if _, _, err := rustfsEndpoint("http://"); err == nil {
		t.Error("rustfsEndpoint(\"http://\") expected error, got nil")
	}
}

var _ Storage = (*RustFSStorage)(nil)
