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

const requestIDKey contextKey = "auth_request_id"

var validToken = regexp.MustCompile(`^[a-zA-Z0-9._-]{8,256}$`)
var validRequestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,255}$`)

type OnEmptyBehavior int

const (
	OnEmptySkip OnEmptyBehavior = iota
	OnEmpty503
	OnEmpty401
)

func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

func BearerAuth(keyHash [32]byte, onEmpty OnEmptyBehavior, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if keyHash == [32]byte{} {
			switch onEmpty {
			case OnEmptySkip:
				log.Warn().Msg("API key not configured - auth disabled")
				next.ServeHTTP(w, r)
				return
			case OnEmpty503:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"error":"endpoints disabled: API key not set"}`))
				return
			case OnEmpty401:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"authentication not configured"}`))
				return
			}
		}

		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"missing or invalid authorization header"}`))
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")

		if !validToken.MatchString(token) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid api key"}`))
			return
		}

		tokenHash := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(keyHash[:], tokenHash[:]) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid api key"}`))
			return
		}

		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" || !validRequestID.MatchString(requestID) {
			requestID = uuid.New().String()
		}
		if len(requestID) > 255 {
			requestID = requestID[:255]
		}
		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
