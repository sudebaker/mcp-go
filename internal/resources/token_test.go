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
	// Multi-use: the same token validates on every presented read until it
	// expires (docs/API.md, Resource resolution flow).
	e2, err := s.Validate(token)
	if err != nil {
		t.Fatalf("second validation of a live token must succeed, got %v", err)
	}
	if e2.Bucket != e.Bucket || e2.Key != e.Key || e2.UserID != e.UserID || e2.SessionID != e.SessionID {
		t.Fatalf("second validation returned a different entry: %+v vs %+v", e, e2)
	}
}

// TestTokenTTLMatchesDocumentedContract anchors tokenTTL to the value
// docs/API.md publishes for res:// tokens (1 hour). Changing the TTL is a
// documentation change: update docs/API.md and this anchor in the same PR.
func TestTokenTTLMatchesDocumentedContract(t *testing.T) {
	if tokenTTL != 3600*time.Second {
		t.Fatalf("tokenTTL = %v, but docs/API.md publishes 1 hour (3600s): update the doc and this anchor together", tokenTTL)
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
