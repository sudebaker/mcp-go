package transport

import (
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

// CORSMiddleware returns a middleware that handles CORS preflight requests and adds
// CORS headers to responses. If an Origin header is present but not in the allowed
// list, it responds with HTTP 403 Forbidden as required by the MCP spec.
// If allowed origins is empty, it allows all origins (*).
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
