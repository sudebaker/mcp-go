package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

type contextKey string

const requestIDKey contextKey = "admin_request_id"

var (
	userIDPattern     = regexp.MustCompile(`^[^/]{1,255}$`)
	collectionPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
	requestIDPattern  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,255}$`)
)

func getRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// Middleware wraps a handler with ADMIN_API_KEY authentication.
func Middleware(adminKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if adminKey == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "admin endpoints disabled: ADMIN_API_KEY not set"})
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "missing or invalid authorization header"})
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		if token != adminKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid admin key"})
			return
		}

		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" || !requestIDPattern.MatchString(requestID) {
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

// Handler holds database access for admin endpoints.
type Handler struct {
	db *sql.DB
}

func NewHandler(db *sql.DB) *Handler {
	return &Handler{db: db}
}

type userStat struct {
	UserID   string `json:"user_id"`
	DocCount int    `json:"doc_count"`
	Bytes    int64  `json:"bytes"`
}

type collectionStat struct {
	Collection string `json:"collection"`
	DocCount   int    `json:"doc_count"`
	Bytes      int64  `json:"bytes"`
}

type deleteResponse struct {
	Deleted        bool   `json:"deleted"`
	UserID         string `json:"user_id,omitempty"`
	Collection     string `json:"collection,omitempty"`
	DocsDeleted    int    `json:"docs_deleted"`
	DocsBytesFreed int64  `json:"docs_bytes_freed"`
}

type doc struct {
	ID         int             `json:"id"`
	DocHash    string          `json:"doc_hash"`
	FilePath   string          `json:"file_path"`
	Collection string          `json:"collection"`
	Metadata   json.RawMessage `json:"metadata"`
	CreatedAt  string          `json:"created_at"`
}

// ListUsers returns all users with document counts and storage size.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT user_id, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes
		FROM kb_documents
		GROUP BY user_id
		ORDER BY doc_count DESC
	`)
	if err != nil {
		log.Error().Err(err).Msg("admin: failed to list users")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	users := make([]userStat, 0)
	for rows.Next() {
		var u userStat
		if err := rows.Scan(&u.UserID, &u.DocCount, &u.Bytes); err != nil {
			log.Error().Err(err).Msg("admin: failed to scan user row")
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("admin: rows iteration error")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	writeJSON(w, http.StatusOK, users)
}

// GetUser returns detail for a specific user (collections and their stats).
func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	userID := extractPathParam(r.URL.Path, "/admin/kb/users/", "/collections/")
	if userID == "" {
		userID = strings.TrimPrefix(r.URL.Path, "/admin/kb/users/")
		// Strip any trailing path
		if idx := strings.Index(userID, "/"); idx > 0 {
			userID = userID[:idx]
		}
	}

	if !userIDPattern.MatchString(userID) {
		writeError(w, http.StatusBadRequest, "invalid user_id format")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT collection, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes
		FROM kb_documents
		WHERE user_id = $1
		GROUP BY collection
	`, userID)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("admin: failed to get user")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	collections := make([]collectionStat, 0)
	for rows.Next() {
		var c collectionStat
		if err := rows.Scan(&c.Collection, &c.DocCount, &c.Bytes); err != nil {
			log.Error().Err(err).Msg("admin: failed to scan collection row")
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		collections = append(collections, c)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user_id":     userID,
		"collections": collections,
	})
}

// DeleteUserData hard-deletes all documents for a user.
func (h *Handler) DeleteUserData(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimPrefix(r.URL.Path, "/admin/kb/users/")
	if idx := strings.Index(userID, "/"); idx > 0 {
		userID = userID[:idx]
	}

	if !userIDPattern.MatchString(userID) {
		writeError(w, http.StatusBadRequest, "invalid user_id format")
		return
	}

	reqID := getRequestID(r.Context())

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		log.Error().Err(err).Msg("admin: failed to begin transaction")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback()

	var docsDeleted int
	var bytesFreed int64
	err = tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0)
		FROM kb_documents WHERE user_id = $1
	`, userID).Scan(&docsDeleted, &bytesFreed)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("admin: failed to count documents")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	if docsDeleted == 0 {
		tx.Commit()
		writeJSON(w, http.StatusOK, deleteResponse{
			Deleted:        true,
			UserID:         userID,
			DocsDeleted:    0,
			DocsBytesFreed: 0,
		})
		return
	}

	_, err = tx.ExecContext(r.Context(), `DELETE FROM kb_documents WHERE user_id = $1`, userID)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("admin: failed to delete documents")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO admin_audit_log(admin_action, target_user_id, docs_deleted, bytes_freed, request_id)
		VALUES ('delete_user', $1, $2, $3, $4)
	`, userID, docsDeleted, bytesFreed, reqID)
	if err != nil {
		log.Error().Err(err).Msg("admin: failed to write audit log")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	if err := tx.Commit(); err != nil {
		log.Error().Err(err).Msg("admin: failed to commit transaction")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	log.Info().
		Str("user_id", userID).
		Int("docs_deleted", docsDeleted).
		Int64("bytes_freed", bytesFreed).
		Str("request_id", reqID).
		Msg("admin: deleted user data")

	writeJSON(w, http.StatusOK, deleteResponse{
		Deleted:        true,
		UserID:         userID,
		DocsDeleted:    docsDeleted,
		DocsBytesFreed: bytesFreed,
	})
}

// DeleteUserCollection hard-deletes a specific collection for a user.
func (h *Handler) DeleteUserCollection(w http.ResponseWriter, r *http.Request) {
	userID, collection := splitUserCollectionPath(r.URL.Path)
	if !userIDPattern.MatchString(userID) {
		writeError(w, http.StatusBadRequest, "invalid user_id format")
		return
	}
	if !collectionPattern.MatchString(collection) {
		writeError(w, http.StatusBadRequest, "invalid collection name format")
		return
	}

	reqID := getRequestID(r.Context())

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		log.Error().Err(err).Msg("admin: failed to begin transaction")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback()

	var docsDeleted int
	var bytesFreed int64
	err = tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0)
		FROM kb_documents WHERE user_id = $1 AND collection = $2
	`, userID, collection).Scan(&docsDeleted, &bytesFreed)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Str("collection", collection).Msg("admin: failed to count documents")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	if docsDeleted == 0 {
		tx.Commit()
		writeJSON(w, http.StatusOK, deleteResponse{
			Deleted:        true,
			UserID:         userID,
			Collection:     collection,
			DocsDeleted:    0,
			DocsBytesFreed: 0,
		})
		return
	}

	_, err = tx.ExecContext(r.Context(), `DELETE FROM kb_documents WHERE user_id = $1 AND collection = $2`, userID, collection)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Str("collection", collection).Msg("admin: failed to delete collection")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO admin_audit_log(admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id)
		VALUES ('delete_user_collection', $1, $2, $3, $4, $5)
	`, userID, collection, docsDeleted, bytesFreed, reqID)
	if err != nil {
		log.Error().Err(err).Msg("admin: failed to write audit log")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	if err := tx.Commit(); err != nil {
		log.Error().Err(err).Msg("admin: failed to commit transaction")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	log.Info().
		Str("user_id", userID).
		Str("collection", collection).
		Int("docs_deleted", docsDeleted).
		Int64("bytes_freed", bytesFreed).
		Str("request_id", reqID).
		Msg("admin: deleted user collection")

	writeJSON(w, http.StatusOK, deleteResponse{
		Deleted:        true,
		UserID:         userID,
		Collection:     collection,
		DocsDeleted:    docsDeleted,
		DocsBytesFreed: bytesFreed,
	})
}

// DeleteGlobalCollection hard-deletes a collection for all users.
func (h *Handler) DeleteGlobalCollection(w http.ResponseWriter, r *http.Request) {
	collection := strings.TrimPrefix(r.URL.Path, "/admin/kb/collections/")
	if !collectionPattern.MatchString(collection) {
		writeError(w, http.StatusBadRequest, "invalid collection name format")
		return
	}

	reqID := getRequestID(r.Context())

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		log.Error().Err(err).Msg("admin: failed to begin transaction")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback()

	var docsDeleted int
	var bytesFreed int64
	err = tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0)
		FROM kb_documents WHERE collection = $1
	`, collection).Scan(&docsDeleted, &bytesFreed)
	if err != nil {
		log.Error().Err(err).Str("collection", collection).Msg("admin: failed to count documents")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	if docsDeleted == 0 {
		tx.Commit()
		writeJSON(w, http.StatusOK, deleteResponse{
			Deleted:        true,
			Collection:     collection,
			DocsDeleted:    0,
			DocsBytesFreed: 0,
		})
		return
	}

	_, err = tx.ExecContext(r.Context(), `DELETE FROM kb_documents WHERE collection = $1`, collection)
	if err != nil {
		log.Error().Err(err).Str("collection", collection).Msg("admin: failed to delete global collection")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO admin_audit_log(admin_action, target_collection, docs_deleted, bytes_freed, request_id)
		VALUES ('delete_global_collection', $1, $2, $3, $4)
	`, collection, docsDeleted, bytesFreed, reqID)
	if err != nil {
		log.Error().Err(err).Msg("admin: failed to write audit log")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	if err := tx.Commit(); err != nil {
		log.Error().Err(err).Msg("admin: failed to commit transaction")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	log.Info().
		Str("collection", collection).
		Int("docs_deleted", docsDeleted).
		Int64("bytes_freed", bytesFreed).
		Str("request_id", reqID).
		Msg("admin: deleted global collection")

	writeJSON(w, http.StatusOK, deleteResponse{
		Deleted:        true,
		Collection:     collection,
		DocsDeleted:    docsDeleted,
		DocsBytesFreed: bytesFreed,
	})
}

// ExportUser returns all documents for a user as JSON (paginado).
func (h *Handler) ExportUser(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimPrefix(r.URL.Path, "/admin/kb/users/")
	if idx := strings.Index(userID, "/export"); idx > 0 {
		userID = userID[:idx]
	}
	if idx := strings.Index(userID, "/"); idx > 0 {
		userID = userID[:idx]
	}

	if !userIDPattern.MatchString(userID) {
		writeError(w, http.StatusBadRequest, "invalid user_id format")
		return
	}

	limit, offset := parsePagination(r, 100, 1000)

	var total int
	err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`, userID).Scan(&total)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("admin: failed to count documents for export")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, doc_hash, file_path, collection, metadata, created_at
		FROM kb_documents
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("admin: failed to export documents")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	docs := make([]doc, 0, limit)
	for rows.Next() {
		var d doc
		var metadata sql.NullString
		if err := rows.Scan(&d.ID, &d.DocHash, &d.FilePath, &d.Collection, &metadata, &d.CreatedAt); err != nil {
			log.Error().Err(err).Msg("admin: failed to scan document row")
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		if metadata.Valid {
			d.Metadata = json.RawMessage(metadata.String)
		} else {
			d.Metadata = json.RawMessage("null")
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("admin: rows iteration error")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user_id": userID,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
		"docs":    docs,
	})
}

// AuditLog returns the admin audit log paginated.
func (h *Handler) AuditLog(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r, 50, 500)

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id, created_at
		FROM admin_audit_log
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		log.Error().Err(err).Msg("admin: failed to query audit log")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	type entry struct {
		Action       string  `json:"action"`
		TargetUserID *string `json:"target_user_id,omitempty"`
		TargetColl   *string `json:"target_collection,omitempty"`
		DocsDeleted  *int    `json:"docs_deleted,omitempty"`
		BytesFreed   *int64  `json:"bytes_freed,omitempty"`
		RequestID    string  `json:"request_id"`
		CreatedAt    string  `json:"created_at"`
	}

	entries := make([]entry, 0, limit)
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.Action, &e.TargetUserID, &e.TargetColl, &e.DocsDeleted, &e.BytesFreed, &e.RequestID, &e.CreatedAt); err != nil {
			log.Error().Err(err).Msg("admin: failed to scan audit row")
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("admin: rows iteration error")
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"limit":   limit,
		"offset":  offset,
	})
}

// SetupMigrations ensures admin tables exist.
func (h *Handler) SetupMigrations(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS admin_audit_log (
			id BIGSERIAL PRIMARY KEY,
			admin_action VARCHAR(50) NOT NULL,
			target_user_id VARCHAR(255),
			target_collection VARCHAR(255),
			docs_deleted INTEGER,
			bytes_freed BIGINT,
			request_id VARCHAR(255),
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_admin_audit_created_at ON admin_audit_log(created_at DESC)`,
	}

	for _, q := range queries {
		if _, err := h.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("admin migration failed: %w", err)
		}
	}

	kbIndexQuery := `
		DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_name = 'kb_documents'
			) THEN
				CREATE INDEX IF NOT EXISTS idx_kb_documents_user_collection
				ON kb_documents(user_id, collection);
			END IF;
		END $$;
	`
	if _, err := h.db.ExecContext(ctx, kbIndexQuery); err != nil {
		return fmt.Errorf("admin migration failed: %w", err)
	}
	log.Info().Msg("Admin migration applied")
	return nil
}

// Helpers

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func parsePagination(r *http.Request, defaultLimit, maxLimit int) (limit, offset int) {
	limit = defaultLimit
	offset = 0

	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			if n <= maxLimit {
				limit = n
			} else {
				limit = maxLimit
			}
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}
	return
}

func extractPathParam(path, prefix, sep string) string {
	s := strings.TrimPrefix(path, prefix)
	if idx := strings.Index(s, sep); idx > 0 {
		return s[:idx]
	}
	return s
}

func splitUserCollectionPath(path string) (userID, collection string) {
	// Path: /admin/kb/users/{user_id}/collections/{collection}
	s := strings.TrimPrefix(path, "/admin/kb/users/")
	parts := strings.SplitN(s, "/collections/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}
