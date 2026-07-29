package resources

import (
	"strings"
	"testing"
	"time"
)

func TestTokenStore_IssueValidate(t *testing.T) {
	s := NewTokenStore()
	token := s.Issue("users", "abc/file.txt", "abc", "sess1", time.Minute)
	e, err := s.Validate(token)
	if err != nil {
		t.Fatal(err)
	}
	if e.Bucket != "users" || e.Key != "abc/file.txt" {
		t.Fatalf("unexpected entry: %+v", e)
	}
	_, err = s.Validate(token)
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("expected already used error, got %v", err)
	}
}

func TestTokenStore_Expired(t *testing.T) {
	s := NewTokenStore()
	token := s.Issue("users", "abc/file.txt", "abc", "sess1", -time.Second)
	_, err := s.Validate(token)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestTokenStore_Cleanup(t *testing.T) {
	s := NewTokenStore()
	token := s.Issue("users", "abc/file.txt", "abc", "sess1", -time.Second)

	s.Cleanup()

	_, err := s.Validate(token)
	if err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("expected invalid token error after cleanup, got %v", err)
	}
}

func TestTokenStore_StartCleanup(t *testing.T) {
	s := NewTokenStore()
	token := s.Issue("users", "abc/file.txt", "abc", "sess1", -time.Second)

	ticker := StartCleanup(s, 50*time.Millisecond)
	defer ticker.Stop()

	// Wait for the cleanup goroutine to run at least once.
	time.Sleep(100 * time.Millisecond)

	_, err := s.Validate(token)
	if err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("expected invalid token error after periodic cleanup, got %v", err)
	}
}
