package auth

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// Auth modes accepted by MCP_AUTH_MODE.
//
// MCP_AUTH_MODE is the single switch that decides whether the MCP endpoints
// require a bearer token. There is no second knob, and no mode may silently
// degrade into "no auth".
const (
	// ModeRequired rejects unauthenticated requests on /mcp, /sse and /message.
	// This is the default when MCP_AUTH_MODE is unset.
	ModeRequired = "required"
	// ModeOff disables authentication. Only for isolated local development.
	ModeOff = "off"
)

// EnvLookup resolves an environment variable, mirroring os.LookupEnv. It is a
// parameter instead of a direct os.LookupEnv call so the keyring can be built
// deterministically in tests.
type EnvLookup func(key string) (string, bool)

// Entry declares one member of the keyring. The token itself is never written
// in a config file: it lives in the environment variable named by KeyEnv and is
// read once at startup.
type Entry struct {
	UserID string
	KeyEnv string
}

// placeholderSecrets are the values shipped in deployments/.env.example. They
// satisfy the charset and length rules for a token, so without an explicit
// rejection an operator who copies the template would start the server with a
// credential anyone can guess.
var placeholderSecrets = map[string]struct{}{
	"change_me":       {},
	"changeme":        {},
	"change-me":       {},
	"placeholder":     {},
	"replace_me":      {},
	"replace-me":      {},
	"your_token_here": {},
	"your-token-here": {},
}

// IsPlaceholderSecret reports whether a secret is one of the placeholder values
// shipped in .env.example, compared case-insensitively and ignoring surrounding
// whitespace.
//
// Callers use it to refuse a configuration that would otherwise run with a
// guessable credential. It matches a fixed deny-list on purpose: guessing at
// "weak" secrets would be unreliable in both directions.
func IsPlaceholderSecret(secret string) bool {
	_, found := placeholderSecrets[strings.ToLower(strings.TrimSpace(secret))]
	return found
}

// Keyring is the runtime identity index: sha256(token) -> user_id.
//
// Only the digest of a token is kept, so the index cannot be replayed into a
// working credential. Lookups are a single map access, which makes the
// per-request cost independent of the number of configured users.
type Keyring struct {
	mode  string
	index map[[32]byte]string
}

// NewKeyring builds a keyring from an already resolved index. Prefer
// BuildKeyring, which validates the configuration; this constructor exists for
// callers that must represent an explicit runtime state (including the
// fail-closed "required but empty" case) and for tests.
func NewKeyring(mode string, index map[[32]byte]string) *Keyring {
	if index == nil {
		index = make(map[[32]byte]string)
	}
	return &Keyring{mode: mode, index: index}
}

// Mode returns the resolved auth mode.
func (k *Keyring) Mode() string {
	return k.mode
}

// Enabled reports whether requests must carry a valid bearer token.
func (k *Keyring) Enabled() bool {
	return k.mode == ModeRequired
}

// Size returns the number of configured entries, for startup logging.
func (k *Keyring) Size() int {
	return len(k.index)
}

// Lookup resolves a bearer token to its user_id.
//
// The token is hashed and compared against the index; the plaintext token is
// never stored and is not derivable from the keyring.
func (k *Keyring) Lookup(token string) (string, bool) {
	if k == nil || token == "" {
		return "", false
	}
	userID, ok := k.index[sha256.Sum256([]byte(token))]
	return userID, ok
}

// ResolveMode reads MCP_AUTH_MODE and validates it.
//
// An unset or empty value means ModeRequired. Any other value than
// ModeRequired or ModeOff is an error: an unrecognised mode aborts startup
// instead of being interpreted as "probably fine", which would leave the MCP
// endpoints unprotected.
func ResolveMode(lookup EnvLookup) (string, error) {
	raw, _ := lookup("MCP_AUTH_MODE")
	mode := strings.TrimSpace(raw)
	switch mode {
	case "":
		return ModeRequired, nil
	case ModeRequired, ModeOff:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid MCP_AUTH_MODE %q: want %q or %q", raw, ModeRequired, ModeOff)
	}
}

// BuildKeyring resolves the auth mode and materialises the sha256(token) ->
// user_id index from the configured entries.
//
// The parse is fail-fast and skips nothing: the first entry that cannot be
// resolved aborts startup with an error that names it, and a partial keyring is
// never returned. The failure modes are:
//
//   - unknown MCP_AUTH_MODE (see ResolveMode)
//   - mode "required" with an empty auth.keys list
//   - an entry with an empty user_id or key_env
//   - a key_env that is unset or empty in the environment
//   - a token that does not match ^[a-zA-Z0-9._-]{8,256}$
//   - a duplicate user_id
//   - two entries resolving to the same token
//
// In ModeOff the keyring is returned disabled and the entries are not resolved,
// so a local developer does not need the secrets exported.
func BuildKeyring(lookup EnvLookup, entries []Entry) (*Keyring, error) {
	mode, err := ResolveMode(lookup)
	if err != nil {
		return nil, err
	}

	if mode == ModeOff {
		return NewKeyring(ModeOff, nil), nil
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("MCP_AUTH_MODE=%s but auth.keys is empty: configure at least one key entry in the config file", mode)
	}

	index := make(map[[32]byte]string, len(entries))
	users := make(map[string]struct{}, len(entries))

	for i, entry := range entries {
		userID := strings.TrimSpace(entry.UserID)
		keyEnv := strings.TrimSpace(entry.KeyEnv)

		if userID == "" {
			return nil, fmt.Errorf("auth.keys[%d]: user_id is empty", i)
		}
		if keyEnv == "" {
			return nil, fmt.Errorf("auth.keys[%d] (user_id %q): key_env is empty", i, userID)
		}

		token, found := lookup(keyEnv)
		if !found || token == "" {
			return nil, fmt.Errorf("auth.keys[%d] (user_id %q): environment variable %s is unset or empty", i, userID, keyEnv)
		}
		if !validToken.MatchString(token) {
			return nil, fmt.Errorf("auth.keys[%d] (user_id %q): token from %s must match %s", i, userID, keyEnv, validToken.String())
		}
		if IsPlaceholderSecret(token) {
			return nil, fmt.Errorf("auth.keys[%d] (user_id %q): token from %s is the placeholder value shipped in .env.example - generate one with 'openssl rand -hex 32'", i, userID, keyEnv)
		}

		if _, duplicate := users[userID]; duplicate {
			return nil, fmt.Errorf("auth.keys[%d]: duplicate user_id %q", i, userID)
		}

		digest := sha256.Sum256([]byte(token))
		if owner, collision := index[digest]; collision {
			return nil, fmt.Errorf("auth.keys[%d] (user_id %q): token from %s is already used by user_id %q", i, userID, keyEnv, owner)
		}

		index[digest] = userID
		users[userID] = struct{}{}
	}

	return NewKeyring(mode, index), nil
}

// Describe renders the keyring for startup logs without leaking any token.
func (k *Keyring) Describe() string {
	if k == nil {
		return "auth=<nil>"
	}
	users := make([]string, 0, len(k.index))
	for _, userID := range k.index {
		users = append(users, userID)
	}
	// Sort for a stable log line.
	for i := 1; i < len(users); i++ {
		for j := i; j > 0 && users[j] < users[j-1]; j-- {
			users[j], users[j-1] = users[j-1], users[j]
		}
	}
	return fmt.Sprintf("mode=%s users=[%s] tokens=%d", k.mode, strings.Join(users, " "), len(k.index))
}
