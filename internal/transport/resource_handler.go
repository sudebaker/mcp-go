package transport

import (
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/sudebaker/mcp-go/internal/auth"
)

// internalResourceTokenPattern is the same charset/length rule the auth
// keyring applies to bearer tokens (internal/auth validToken). A
// SANDBOX_API_TOKEN outside this shape is a misconfigured secret, not a
// permission.
var internalResourceTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{8,256}$`)

// internalResourceKeyHash resolves the shared secret that guards
// GET /internal/resource/{token} from SANDBOX_API_TOKEN.
//
// The secret is shared with the sandbox sidecar, which presents it as
// `Authorization: Bearer <SANDBOX_API_TOKEN>` when it resolves res:// URIs
// (docs/SANDBOX_SIDECAR_API.md §5). It also travels in the environment of the
// orchestrator container so the in-container Python tools can stream their own
// res:// URIs through tools/common/resources/resource.py.
//
// Fail-closed, mirroring the keyring rules (internal/auth/keyring.go): unset,
// empty or a charset-invalid value returns the zero hash, which makes
// auth.BearerAuth answer 401 for every request. A token that only satisfied
// "the DNS name resolved" would reintroduce the reviewed finding this guard
// closes: the contract §5.2 promises a Bearer the server must actually check.
func internalResourceKeyHash() ([32]byte, error) {
	var zero [32]byte
	token := strings.TrimSpace(os.Getenv("SANDBOX_API_TOKEN"))
	if token == "" {
		return zero, errors.New("SANDBOX_API_TOKEN is unset or empty")
	}
	if !internalResourceTokenPattern.MatchString(token) {
		return zero, errors.New("SANDBOX_API_TOKEN must match ^[a-zA-Z0-9._-]{8,256}$")
	}
	if auth.IsPlaceholderSecret(token) {
		return zero, errors.New("SANDBOX_API_TOKEN is a placeholder value from .env.example - generate one with 'openssl rand -hex 32'")
	}
	return sha256.Sum256([]byte(token)), nil
}

// internalResourceHandler wraps the resource streaming handler with the
// shared-secret bearer middleware. The zero hash (token not configured)
// refuses every request: 401, never an unauthenticated pass-through.
func (s *MCPServer) internalResourceHandler() http.Handler {
	return auth.BearerAuth(
		s.internalResourceKeyHash,
		auth.OnEmpty401,
		http.HandlerFunc(s.handleInternalResource),
	)
}

func (s *MCPServer) handleInternalResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := strings.TrimPrefix(r.URL.Path, "/internal/resource/")
	if token == "" {
		http.Error(w, "Missing token", http.StatusBadRequest)
		return
	}

	entry, err := s.resourceManager.Tokens().Validate(token)
	if err != nil {
		http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()
	reader, err := s.resourceManager.Storage().Open(ctx, entry.Bucket, entry.Key)
	if err != nil {
		log.Error().Err(err).Str("key", entry.Key).Msg("Failed to open resource")
		http.Error(w, "Failed to open resource", http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	info, err := s.resourceManager.Storage().Stat(ctx, entry.Bucket, entry.Key)
	if err == nil && info.ContentType != "" {
		w.Header().Set("Content-Type", info.ContentType)
	}
	w.Header().Set("X-Resource-Name", entry.Name)

	if _, err := io.Copy(w, reader); err != nil {
		log.Error().Err(err).Msg("Failed to stream resource")
	}
}
