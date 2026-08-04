package transport

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

// DefaultMaxMCPBodySize is the default max body size for MCP endpoints in bytes.
const DefaultMaxMCPBodySize int64 = 10 * 1024 * 1024 // 10 MB

// MaxBodyMiddleware returns a middleware that limits the request body size using
// http.MaxBytesReader. If the body exceeds the limit, the reader returns
// http.ErrBodyTooLarge and the handler can respond accordingly.
// Safe for SSE endpoints: http.MaxBytesReader only affects bytes read from r.Body,
// and SSE GET handlers don't read the request body at all.
// maxBodySize returns the configured max body size in bytes, or DefaultMaxMCPBodySize
// if the config value is zero or negative.
func maxBodySize(mb int64) int64 {
	if mb <= 0 {
		return DefaultMaxMCPBodySize
	}
	return mb * 1024 * 1024
}

func MaxBodyMiddleware(maxSize int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxSize)
			next.ServeHTTP(w, r)
		})
	}
}

// CORSMiddleware returns a middleware that handles CORS preflight requests and adds
// CORS headers to responses. If an Origin header is present but not in the allowed
// list, it responds with HTTP 403 Forbidden as required by the MCP spec.
// If allowed origins is empty, it reflects the request origin (permissive mode).
//
// In restricted mode (non-empty allowed list), disallowed origins are rejected with 403
// before the inner handler runs, so there is no risk of the handler overwriting CORS
// headers. In permissive mode (empty list), the handler may set conflicting CORS headers,
// but this is functionally acceptable since all origins are already allowed.
func CORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	// Pre-compile the set of allowed origins for O(1) lookup
	allowedSet := make(map[string]struct{}, len(allowedOrigins))
	permissive := len(allowedOrigins) == 0
	for _, origin := range allowedOrigins {
		allowedSet[strings.TrimSpace(origin)] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			var corsAllowed bool
			var corsOrigin string

			// Validate origin if header is present
			if origin != "" {
				if permissive {
					// Empty allowed list = allow all
					corsAllowed = true
					corsOrigin = origin
				} else if _, ok := allowedSet[origin]; ok {
					// Origin in allowed set
					corsAllowed = true
					corsOrigin = origin
				} else {
					// Origin not allowed
					log.Warn().
						Str("origin", origin).
						Msg("CORS request rejected: origin not in allowed list")
					http.Error(w, "Origin not allowed", http.StatusForbidden)
					return
				}
			} else if permissive {
				// No Origin header and permissive mode = allow all with *
				corsAllowed = true
				corsOrigin = "*"
			}

			// Set CORS headers
			if corsAllowed {
				w.Header().Set("Access-Control-Allow-Origin", corsOrigin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}

			// Handle preflight requests
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// AdminCORSMiddleware returns a middleware that applies strict CORS validation
// for admin endpoints. Unlike CORSMiddleware, it:
//   - Allows Authorization header (for Bearer token auth)
//   - Restricts methods to GET, DELETE, OPTIONS
//   - Does not set wildcard origins even in permissive mode (since admin
//     endpoints carry sensitive data)
//
// If the Origin header is present but not in the allowed list, the request
// is rejected with 403 before the inner handler runs. Requests without an
// Origin header (CLI, curl, Postman) are always allowed.
func AdminCORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowedSet := make(map[string]struct{}, len(allowedOrigins))
	permissive := len(allowedOrigins) == 0
	for _, origin := range allowedOrigins {
		allowedSet[strings.TrimSpace(origin)] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))

			if origin != "" {
				if !permissive {
					if _, ok := allowedSet[origin]; !ok {
						log.Warn().
							Str("origin", origin).
							Msg("Admin CORS request rejected: origin not in allowed list")
						http.Error(w, fmt.Sprintf(`{"error":"origin '%s' not allowed"}`, origin), http.StatusForbidden)
						return
					}
				}
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}

			w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
