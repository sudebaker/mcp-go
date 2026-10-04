package auth

import (
	"crypto/sha256"
	"strings"
	"testing"
)

// envFrom builds a deterministic EnvLookup stub for tests.
func envFrom(pairs map[string]string) EnvLookup {
	return func(key string) (string, bool) {
		v, ok := pairs[key]
		return v, ok
	}
}

func TestResolveMode_DefaultsToRequired(t *testing.T) {
	for name, env := range map[string]EnvLookup{
		"unset": envFrom(nil),
		"empty": envFrom(map[string]string{"MCP_AUTH_MODE": ""}),
		"blank": envFrom(map[string]string{"MCP_AUTH_MODE": "   "}),
	} {
		mode, err := ResolveMode(env)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if mode != ModeRequired {
			t.Errorf("%s: expected %q, got %q", name, ModeRequired, mode)
		}
	}
}

func TestResolveMode_AcceptsKnownModes(t *testing.T) {
	for _, raw := range []string{ModeRequired, ModeOff} {
		mode, err := ResolveMode(envFrom(map[string]string{"MCP_AUTH_MODE": raw}))
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", raw, err)
		}
		if mode != raw {
			t.Errorf("expected %q, got %q", raw, mode)
		}
	}
}

// An unknown mode must abort startup: guessing ("close enough") would silently
// leave MCP endpoints unprotected.
func TestResolveMode_UnknownModeAborts(t *testing.T) {
	for _, raw := range []string{"true", "enforce", "REQUIRED", "Off", "0", "on", "yes", "required!", "no"} {
		if _, err := ResolveMode(envFrom(map[string]string{"MCP_AUTH_MODE": raw})); err == nil {
			t.Errorf("MCP_AUTH_MODE=%q: expected an error, got none", raw)
		}
	}
}

// Surrounding whitespace is an accident of .env formatting, not a different
// mode, so it is normalised instead of aborting the server.
func TestResolveMode_TrimsSurroundingWhitespace(t *testing.T) {
	for raw, want := range map[string]string{
		" required":   ModeRequired,
		"required ":   ModeRequired,
		" required\n": ModeRequired,
		"	off	":       ModeOff,
	} {
		mode, err := ResolveMode(envFrom(map[string]string{"MCP_AUTH_MODE": raw}))
		if err != nil {
			t.Errorf("MCP_AUTH_MODE=%q: unexpected error: %v", raw, err)
			continue
		}
		if mode != want {
			t.Errorf("MCP_AUTH_MODE=%q: expected %q, got %q", raw, want, mode)
		}
	}
}

func TestBuildKeyring_IndexesSHA256OfToken(t *testing.T) {
	token := "amphora-token-abcdef"
	k, err := BuildKeyring(envFrom(map[string]string{
		"MCP_AUTH_MODE":        ModeRequired,
		"MCP_AUTH_KEY_AMPHORA": token,
	}), []Entry{{UserID: "amphora", KeyEnv: "MCP_AUTH_KEY_AMPHORA"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k == nil {
		t.Fatal("expected a keyring, got nil")
	}
	if !k.Enabled() {
		t.Error("required keyring must report Enabled() == true")
	}
	if k.Mode() != ModeRequired {
		t.Errorf("expected mode %q, got %q", ModeRequired, k.Mode())
	}
	if k.Size() != 1 {
		t.Errorf("expected 1 entry, got %d", k.Size())
	}

	userID, ok := k.Lookup(token)
	if !ok || userID != "amphora" {
		t.Errorf("expected (amphora, true), got (%q, %v)", userID, ok)
	}
	if _, ok := k.Lookup("not-the-token-1234"); ok {
		t.Error("unknown token must not resolve")
	}

	// The runtime index is keyed by sha256(token) -> user_id, never by the
	// plaintext token.
	if _, ok := k.index[sha256.Sum256([]byte(token))]; !ok {
		t.Error("expected index to be keyed by sha256(token)")
	}
}

func TestBuildKeyring_MultipleUsers(t *testing.T) {
	k, err := BuildKeyring(envFrom(map[string]string{
		"MCP_AUTH_MODE":        ModeRequired,
		"MCP_AUTH_KEY_AMPHORA": "amphora-token-abcdef",
		"MCP_UPLOAD_API_KEY":   "legacy-upload-token-99",
	}), []Entry{
		{UserID: "amphora", KeyEnv: "MCP_AUTH_KEY_AMPHORA"},
		{UserID: "legacy-upload", KeyEnv: "MCP_UPLOAD_API_KEY"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k.Size() != 2 {
		t.Fatalf("expected 2 entries, got %d", k.Size())
	}
	if userID, ok := k.Lookup("legacy-upload-token-99"); !ok || userID != "legacy-upload" {
		t.Errorf("expected legacy-upload, got (%q, %v)", userID, ok)
	}
}

// Fail-fast parsing: the first offending entry aborts startup with a message
// naming the entry. No entry is ever skipped, and no partial keyring is returned.
func TestBuildKeyring_FailFastOnBadEntry(t *testing.T) {
	okEnv := map[string]string{
		"MCP_AUTH_MODE":        ModeRequired,
		"MCP_AUTH_KEY_AMPHORA": "amphora-token-abcdef",
		"MCP_AUTH_KEY_BOB":     "bob-token-1234567890",
		"MCP_AUTH_KEY_SHORT":   "short",
		"MCP_AUTH_KEY_EMPTY":   "",
	}
	valid := Entry{UserID: "amphora", KeyEnv: "MCP_AUTH_KEY_AMPHORA"}
	second := Entry{UserID: "bob", KeyEnv: "MCP_AUTH_KEY_BOB"}

	tests := []struct {
		name       string
		entries    []Entry
		wantSubstr string
	}{
		{
			name:       "empty keyring in required mode",
			entries:    nil,
			wantSubstr: "auth.keys is empty",
		},
		{
			name:       "unset key_env",
			entries:    []Entry{valid, {UserID: "carol", KeyEnv: "MCP_AUTH_KEY_MISSING"}},
			wantSubstr: "MCP_AUTH_KEY_MISSING",
		},
		{
			name:       "empty key_env value",
			entries:    []Entry{valid, {UserID: "dave", KeyEnv: "MCP_AUTH_KEY_EMPTY"}},
			wantSubstr: "MCP_AUTH_KEY_EMPTY",
		},
		{
			name:       "empty user_id",
			entries:    []Entry{{UserID: "  ", KeyEnv: "MCP_AUTH_KEY_AMPHORA"}},
			wantSubstr: "user_id",
		},
		{
			name:       "empty key_env name",
			entries:    []Entry{{UserID: "amphora", KeyEnv: " "}},
			wantSubstr: "key_env",
		},
		{
			name:       "duplicate user_id",
			entries:    []Entry{valid, {UserID: "amphora", KeyEnv: "MCP_AUTH_KEY_BOB"}},
			wantSubstr: "duplicate user_id",
		},
		{
			name:       "duplicate token",
			entries:    []Entry{valid, {UserID: "bob", KeyEnv: "MCP_AUTH_KEY_AMPHORA"}},
			wantSubstr: "bob",
		},
		{
			name:       "malformed token",
			entries:    []Entry{{UserID: "eve", KeyEnv: "MCP_AUTH_KEY_SHORT"}},
			wantSubstr: "MCP_AUTH_KEY_SHORT",
		},
		{
			name:       "bad entry after a valid one still aborts",
			entries:    []Entry{valid, second, {UserID: "frank", KeyEnv: "MCP_AUTH_KEY_MISSING"}},
			wantSubstr: "MCP_AUTH_KEY_MISSING",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, err := BuildKeyring(envFrom(okEnv), tc.entries)
			if err == nil {
				t.Fatalf("expected an error, got keyring with %d entries", k.Size())
			}
			if k != nil {
				t.Error("a failed build must not return a partial keyring")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestBuildKeyring_OffModeNeedsNoEnv(t *testing.T) {
	k, err := BuildKeyring(envFrom(map[string]string{"MCP_AUTH_MODE": ModeOff}), []Entry{
		{UserID: "amphora", KeyEnv: "MCP_AUTH_KEY_UNSET"},
	})
	if err != nil {
		t.Fatalf("off mode must not require the key environment variables: %v", err)
	}
	if k == nil {
		t.Fatal("expected a keyring, got nil")
	}
	if k.Enabled() {
		t.Error("off mode must report Enabled() == false")
	}
	if _, ok := k.Lookup("anything-12345678"); ok {
		t.Error("off mode must not resolve any token")
	}
}

func TestNewKeyring_RequiredEmptyIsNotEnabledButPresent(t *testing.T) {
	k := NewKeyring(ModeRequired, nil)
	if k == nil {
		t.Fatal("expected a keyring, got nil")
	}
	if !k.Enabled() {
		t.Error("required mode must stay Enabled() == true even with an empty index")
	}
	if k.Size() != 0 {
		t.Errorf("expected empty index, got %d entries", k.Size())
	}
}
