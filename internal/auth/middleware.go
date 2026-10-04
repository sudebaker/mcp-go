package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

type contextKey string

const (
	requestIDKey contextKey = "auth_request_id"
	userIDKey    contextKey = "auth_user_id"
)

var validToken = regexp.MustCompile(`^[a-zA-Z0-9._-]{8,256}$`)
var validRequestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,255}$`)

// OnEmptyBehavior decides what a single-key middleware does when its key is not
// configured. The zero value is OnEmpty503, so a middleware built without an
// explicit choice fails closed instead of serving the request.
type OnEmptyBehavior int

const (
	// OnEmpty503 rejects the request with 503 Service Unavailable.
	OnEmpty503 OnEmptyBehavior = iota
	// OnEmpty401 rejects the request with 401 Unauthorized.
	OnEmpty401
)

// GetRequestID returns the request id stored in the context by an auth
// middleware, or "" when the request never passed through one.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// UserIDFromContext returns the user_id bound to the request by KeyringAuth.
//
// An empty string means the request carries no authenticated identity. Callers
// MUST treat that as "unknown user" and never fall back to a client-supplied
// value.
func UserIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(userIDKey).(string); ok {
		return id
	}
	return ""
}

// BearerAuth protects a handler with a single pre-computed key hash.
//
// It is used by endpoints guarded by a dedicated service key (the admin API).
// Endpoints that identify a user go through KeyringAuth instead.
//
// When keyHash is the zero value the middleware is fail-closed: it answers with
// the configured OnEmptyBehavior instead of serving the request.
func BearerAuth(keyHash [32]byte, onEmpty OnEmptyBehavior, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if keyHash == [32]byte{} {
			if onEmpty == OnEmpty401 {
				writeAuthError(w, http.StatusUnauthorized, "authentication not configured")
				return
			}
			writeAuthError(w, http.StatusServiceUnavailable, "endpoints disabled: API key not set")
			return
		}

		token, ok := bearerToken(r)
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "missing or invalid authorization header")
			return
		}

		tokenHash := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(keyHash[:], tokenHash[:]) != 1 {
			writeAuthError(w, http.StatusUnauthorized, "invalid api key")
			return
		}

		next.ServeHTTP(w, withRequestID(w, r))
	})
}

// KeyringAuth authenticates a request against the keyring and binds the
// resolved user_id to the request context.
//
// Behaviour, in order:
//
//   - nil keyring: 503. Never "no auth": a missing keyring is a
//     misconfiguration, not a permission.
//   - keyring disabled (MCP_AUTH_MODE=off): the request is passed through and
//     NO identity is injected, so nothing downstream can mistake a header for
//     a user.
//   - keyring enabled but empty: 503. BuildKeyring refuses to produce this
//     state; the branch is the last line of defence so no misconfiguration can
//     turn into open access.
//   - missing, malformed or unknown token: 401.
//   - valid token: the request continues with the user_id in its context.
//
// The identity is read exclusively from the Authorization header. No other
// header (X-User-Id, Mcp-Session-Id, ...) can carry an identity.
func KeyringAuth(k *Keyring, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k == nil {
			writeAuthError(w, http.StatusServiceUnavailable, "endpoints disabled: authentication not configured")
			return
		}
		if !k.Enabled() {
			next.ServeHTTP(w, r)
			return
		}
		if k.Size() == 0 {
			log.Error().Msg("auth keyring is empty while MCP_AUTH_MODE=required - refusing request")
			writeAuthError(w, http.StatusServiceUnavailable, "endpoints disabled: no auth key configured")
			return
		}

		token, ok := bearerToken(r)
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "missing or invalid authorization header")
			return
		}

		userID, ok := k.Lookup(token)
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "invalid api key")
			return
		}

		r = withRequestID(w, r)
		r = r.WithContext(context.WithValue(r.Context(), userIDKey, userID))

		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts a syntactically valid bearer token from the request.
// It returns ok=false for a missing header, a foreign scheme, or a token that
// fails the charset/length check.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if !validToken.MatchString(token) {
		return "", false
	}
	return token, true
}

// withRequestID sets the X-Request-ID response header and stores the id in the
// request context, reusing a well-formed client-provided id when present.
func withRequestID(w http.ResponseWriter, r *http.Request) *http.Request {
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" || !validRequestID.MatchString(requestID) {
		requestID = uuid.New().String()
	}
	if len(requestID) > 255 {
		requestID = requestID[:255]
	}
	w.Header().Set("X-Request-ID", requestID)
	return r.WithContext(context.WithValue(r.Context(), requestIDKey, requestID))
}

// writeAuthError writes a JSON error response. message is always a constant
// literal defined in this package, never request-derived.
func writeAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(`{"error":"` + message + `"}`))
}
