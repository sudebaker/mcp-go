package transport

import (
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

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
