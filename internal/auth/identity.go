package auth

import (
	"context"

	"github.com/rs/zerolog/log"
)

// SessionRegistry is the minimal contract needed to bind an authenticated
// identity to an MCP session. *session.Store satisfies it.
type SessionRegistry interface {
	Set(sessionID, userID string)
	Get(sessionID string) (string, bool)
}

// BindSessionUserID binds the authenticated identity of the current request to
// an MCP session.
//
// The identity comes exclusively from the verified bearer token that
// KeyringAuth injected into the context. clientAsserted is the value the client
// sent in capabilities.experimental.user_id: it is deliberately IGNORED and
// accepted only so the attempt can be logged.
//
// It returns (userID, true) when an authenticated identity was bound, and
// ("", false) when the request carried none — in which case the session is left
// untouched, so no downstream tool can observe a client-asserted user_id.
func BindSessionUserID(ctx context.Context, clientAsserted string, store SessionRegistry, sessionID string) (string, bool) {
	authenticated := UserIDFromContext(ctx)

	if clientAsserted != "" && clientAsserted != authenticated {
		log.Warn().
			Str("session_id", sessionID).
			Str("client_asserted_user_id", clientAsserted).
			Msg("ignoring client-asserted user_id: identity is derived from the bearer token")
	}

	if authenticated == "" {
		return "", false
	}

	if store != nil {
		store.Set(sessionID, authenticated)
	}

	return authenticated, true
}
