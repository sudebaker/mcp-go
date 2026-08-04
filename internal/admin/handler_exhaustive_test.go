package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sudebaker/mcp-go/internal/auth"
)

// --- Middleware ---

func TestMiddleware_TruncatesLongRequestID(t *testing.T) {
	longID := strings.Repeat("a", 300)
	handler := auth.BearerAuth(sha256.Sum256([]byte("test-key")), auth.OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := w.Header().Get("X-Request-ID")
		if len(rid) > 255 {
			t.Errorf("request ID was not truncated: len=%d", len(rid))
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Request-ID", longID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestMiddleware_InvalidRequestID_GeneratesNew(t *testing.T) {
	handler := auth.BearerAuth(sha256.Sum256([]byte("test-key")), auth.OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Request-ID", "invalid chars!!!")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	rid := rec.Header().Get("X-Request-ID")
	if rid == "invalid chars!!!" {
		t.Error("expected new generated request ID, got the same invalid one")
	}
}

func TestMiddleware_EmptyBearer(t *testing.T) {
	handler := auth.BearerAuth(sha256.Sum256([]byte("test-key")), auth.OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_NoBearerPrefix(t *testing.T) {
	handler := auth.BearerAuth(sha256.Sum256([]byte("test-key")), auth.OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	req.Header.Set("Authorization", "test-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_SetsRequestIDOnResponse(t *testing.T) {
	handler := auth.BearerAuth(sha256.Sum256([]byte("test-key")), auth.OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header on response")
	}
}

// --- ListUsers ---

func TestListUsers_QueryError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes FROM kb_documents GROUP BY user_id ORDER BY doc_count DESC`)).
		WillReturnError(errors.New("connection refused"))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	rec := httptest.NewRecorder()
	h.ListUsers(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestListUsers_ScanError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes FROM kb_documents GROUP BY user_id ORDER BY doc_count DESC`)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "doc_count", "bytes"}).
			AddRow("user1", "not-an-int", int64(0)))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	rec := httptest.NewRecorder()
	h.ListUsers(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestListUsers_RowsError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes FROM kb_documents GROUP BY user_id ORDER BY doc_count DESC`)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "doc_count", "bytes"}).
			AddRow("user1", 5, int64(100)).
			RowError(0, errors.New("row read error")))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	rec := httptest.NewRecorder()
	h.ListUsers(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// --- GetUser ---

func TestGetUser_EmptyCollections(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT collection, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes FROM kb_documents WHERE user_id = $1 GROUP BY collection`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"collection", "doc_count", "bytes"}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1", nil)
	rec := httptest.NewRecorder()
	h.GetUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	cols := resp["collections"].([]interface{})
	if len(cols) != 0 {
		t.Errorf("expected empty collections, got %d", len(cols))
	}
}

func TestGetUser_QueryError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT collection, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes FROM kb_documents WHERE user_id = $1 GROUP BY collection`)).
		WithArgs("user1").
		WillReturnError(errors.New("connection refused"))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1", nil)
	rec := httptest.NewRecorder()
	h.GetUser(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestGetUser_ScanError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT collection, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes FROM kb_documents WHERE user_id = $1 GROUP BY collection`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"collection", "doc_count", "bytes"}).
			AddRow("default", "bad-value", int64(0)))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1", nil)
	rec := httptest.NewRecorder()
	h.GetUser(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestGetUser_WithSlashPrefixPath(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT collection, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes FROM kb_documents WHERE user_id = $1 GROUP BY collection`)).
		WithArgs("user@domain").
		WillReturnRows(sqlmock.NewRows([]string{"collection", "doc_count", "bytes"}).AddRow("default", 3, int64(500)))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user@domain", nil)
	rec := httptest.NewRecorder()
	h.GetUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// --- DeleteUserData ---

func TestDeleteUserData_TxBeginError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin().WillReturnError(errors.New("tx pool exhausted"))
	h := &Handler{db: db}

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserData(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteUserData_CountQueryError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnError(errors.New("count failed"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserData(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteUserData_DeleteExecError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, int64(1500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnError(errors.New("delete failed"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserData(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteUserData_AuditLogInsertError(t *testing.T) {
	h, mock := newTestHandler(t)
	reqID := "test-req-id"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, int64(1500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_user_id, docs_deleted, bytes_freed, request_id) VALUES ('delete_user', $1, $2, $3, $4)`)).
		WithArgs("user1", 3, int64(1500), reqID).
		WillReturnError(errors.New("audit insert failed"))
	mock.ExpectRollback()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1", nil)
	mw := auth.BearerAuth(sha256.Sum256([]byte("test-key")), auth.OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/admin/kb/users/user1"
		h.DeleteUserData(w, r)
	}))
	r.Header.Set("Authorization", "Bearer test-key")
	r.Header.Set("X-Request-ID", reqID)
	mw.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestDeleteUserData_CommitError(t *testing.T) {
	h, mock := newTestHandler(t)
	reqID := "test-req"
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, int64(1500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_user_id, docs_deleted, bytes_freed, request_id) VALUES ('delete_user', $1, $2, $3, $4)`)).
		WithArgs("user1", 3, int64(1500), reqID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1", nil)
	mw := auth.BearerAuth(sha256.Sum256([]byte("test-key")), auth.OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/admin/kb/users/user1"
		h.DeleteUserData(w, r)
	}))
	r.Header.Set("Authorization", "Bearer test-key")
	r.Header.Set("X-Request-ID", reqID)
	mw.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

// --- DeleteUserCollection ---

func TestDeleteUserCollection_ValidWithDocs(t *testing.T) {
	h, mock := newTestHandler(t)
	reqID := "test-coll-req"
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(7, int64(3500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnResult(sqlmock.NewResult(0, 7))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id) VALUES ('delete_user_collection', $1, $2, $3, $4, $5)`)).
		WithArgs("user1", "research", 7, int64(3500), reqID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1/collections/research", nil)
	mw := auth.BearerAuth(sha256.Sum256([]byte("test-key")), auth.OnEmpty503, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/admin/kb/users/user1/collections/research"
		h.DeleteUserCollection(w, r)
	}))
	r.Header.Set("Authorization", "Bearer test-key")
	r.Header.Set("X-Request-ID", reqID)
	mw.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp deleteResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.DocsDeleted != 7 || resp.DocsBytesFreed != 3500 || resp.Collection != "research" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestDeleteUserCollection_InvalidUserID(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user/name/collections/default", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserCollection(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestDeleteUserCollection_TxBeginError(t *testing.T) {
	db, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock2.ExpectBegin().WillReturnError(errors.New("tx pool exhausted"))
	h := &Handler{db: db}

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1/collections/default", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteUserCollection_CountQueryError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnError(errors.New("count failed"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1/collections/research", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteUserCollection_DeleteExecError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, int64(1500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnError(errors.New("delete failed"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1/collections/research", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteUserCollection_AuditLogInsertError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, int64(1500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id) VALUES ('delete_user_collection', $1, $2, $3, $4, $5)`)).
		WithArgs("user1", "research", 3, int64(1500), sqlmock.AnyArg()).
		WillReturnError(errors.New("audit log failed"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1/collections/research", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteUserCollection_CommitError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, int64(1500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id) VALUES ('delete_user_collection', $1, $2, $3, $4, $5)`)).
		WithArgs("user1", "research", 3, int64(1500), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1/collections/research", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// --- DeleteGlobalCollection ---

func TestDeleteGlobalCollection_InvalidCollection(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/collections/has.dot", nil)
	rec := httptest.NewRecorder()
	h.DeleteGlobalCollection(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestDeleteGlobalCollection_TxBeginError(t *testing.T) {
	db, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock2.ExpectBegin().WillReturnError(errors.New("tx pool exhausted"))
	h := &Handler{db: db}

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/collections/valid-col", nil)
	rec := httptest.NewRecorder()
	h.DeleteGlobalCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteGlobalCollection_CountQueryError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE collection = $1`)).
		WithArgs("valid-col").
		WillReturnError(errors.New("count failed"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/collections/valid-col", nil)
	rec := httptest.NewRecorder()
	h.DeleteGlobalCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteGlobalCollection_DeleteExecError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE collection = $1`)).
		WithArgs("valid-col").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(5, int64(2500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE collection = $1`)).
		WithArgs("valid-col").
		WillReturnError(errors.New("delete failed"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/collections/valid-col", nil)
	rec := httptest.NewRecorder()
	h.DeleteGlobalCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteGlobalCollection_AuditLogInsertError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE collection = $1`)).
		WithArgs("valid-col").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(5, int64(2500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE collection = $1`)).
		WithArgs("valid-col").
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_collection, docs_deleted, bytes_freed, request_id) VALUES ('delete_global_collection', $1, $2, $3, $4)`)).
		WithArgs("valid-col", 5, int64(2500), sqlmock.AnyArg()).
		WillReturnError(errors.New("audit log failed"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/collections/valid-col", nil)
	rec := httptest.NewRecorder()
	h.DeleteGlobalCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestDeleteGlobalCollection_CommitError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE collection = $1`)).
		WithArgs("valid-col").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(5, int64(2500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE collection = $1`)).
		WithArgs("valid-col").
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_collection, docs_deleted, bytes_freed, request_id) VALUES ('delete_global_collection', $1, $2, $3, $4)`)).
		WithArgs("valid-col", 5, int64(2500), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/collections/valid-col", nil)
	rec := httptest.NewRecorder()
	h.DeleteGlobalCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// --- ExportUser ---

func TestExportUser_EmptyExport(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, doc_hash, file_path, collection, metadata, created_at FROM kb_documents WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)).
		WithArgs("user1", 100, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "doc_hash", "file_path", "collection", "metadata", "created_at"}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp["total"].(float64) != 0 {
		t.Errorf("expected total=0, got %v", resp["total"])
	}
}

func TestExportUser_NullMetadata(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, doc_hash, file_path, collection, metadata, created_at FROM kb_documents WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)).
		WithArgs("user1", 100, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "doc_hash", "file_path", "collection", "metadata", "created_at"}).
			AddRow(1, "hash1", "/path/doc.txt", "default", nil, "2026-01-01T00:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	docs := resp["docs"].([]interface{})
	doc := docs[0].(map[string]interface{})
	if doc["metadata"] != nil {
		t.Errorf("expected null metadata, got %v", doc["metadata"])
	}
}

func TestExportUser_CustomPagination(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(50))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, doc_hash, file_path, collection, metadata, created_at FROM kb_documents WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)).
		WithArgs("user1", 10, 20).
		WillReturnRows(sqlmock.NewRows([]string{"id", "doc_hash", "file_path", "collection", "metadata", "created_at"}).
			AddRow(1, "hash1", "/p1.txt", "col1", `{"k":"v"}`, "2026-01-01T00:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export?limit=10&offset=20", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp["limit"].(float64) != 10 || resp["offset"].(float64) != 20 {
		t.Errorf("unexpected pagination: limit=%v offset=%v", resp["limit"], resp["offset"])
	}
}

func TestExportUser_CountQueryError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnError(errors.New("count failed"))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestExportUser_DocsQueryError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, doc_hash, file_path, collection, metadata, created_at FROM kb_documents WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)).
		WithArgs("user1", 100, 0).
		WillReturnError(errors.New("query failed"))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestExportUser_ScanError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, doc_hash, file_path, collection, metadata, created_at FROM kb_documents WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)).
		WithArgs("user1", 100, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "doc_hash", "file_path", "collection", "metadata", "created_at"}).
			AddRow("bad-id", "hash1", "/path/doc.txt", "default", nil, "2026-01-01T00:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestExportUser_RowsError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, doc_hash, file_path, collection, metadata, created_at FROM kb_documents WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)).
		WithArgs("user1", 100, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "doc_hash", "file_path", "collection", "metadata", "created_at"}).
			AddRow(1, "hash1", "/path/doc.txt", "default", `{"k":"v"}`, "2026-01-01T00:00:00Z").
			RowError(0, errors.New("row read error")))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestExportUser_ExportPathWithTrailingSlash(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, doc_hash, file_path, collection, metadata, created_at FROM kb_documents WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)).
		WithArgs("user1", 100, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "doc_hash", "file_path", "collection", "metadata", "created_at"}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// --- AuditLog ---

func TestAuditLog_Empty(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id, created_at FROM admin_audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2`)).
		WithArgs(50, 0).
		WillReturnRows(sqlmock.NewRows([]string{"admin_action", "target_user_id", "target_collection", "docs_deleted", "bytes_freed", "request_id", "created_at"}))

	req := httptest.NewRequest(http.MethodGet, "/admin/audit", nil)
	rec := httptest.NewRecorder()
	h.AuditLog(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	entries := resp["entries"].([]interface{})
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestAuditLog_QueryError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id, created_at FROM admin_audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2`)).
		WithArgs(50, 0).
		WillReturnError(errors.New("query failed"))

	req := httptest.NewRequest(http.MethodGet, "/admin/audit", nil)
	rec := httptest.NewRecorder()
	h.AuditLog(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestAuditLog_ScanError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id, created_at FROM admin_audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2`)).
		WithArgs(50, 0).
		WillReturnRows(sqlmock.NewRows([]string{"admin_action", "target_user_id", "target_collection", "docs_deleted", "bytes_freed", "request_id", "created_at"}).
			AddRow(nil, "user1", nil, 5, int64(2500), "req-1", "2026-01-01T00:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/admin/audit", nil)
	rec := httptest.NewRecorder()
	h.AuditLog(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestAuditLog_CustomPagination(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id, created_at FROM admin_audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2`)).
		WithArgs(20, 40).
		WillReturnRows(sqlmock.NewRows([]string{"admin_action", "target_user_id", "target_collection", "docs_deleted", "bytes_freed", "request_id", "created_at"}).
			AddRow("delete_user", "user1", nil, 5, int64(2500), "req-1", "2026-01-01T00:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/admin/audit?limit=20&offset=40", nil)
	rec := httptest.NewRecorder()
	h.AuditLog(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp["limit"].(float64) != 20 || resp["offset"].(float64) != 40 {
		t.Errorf("unexpected pagination: limit=%v offset=%v", resp["limit"], resp["offset"])
	}
}

func TestAuditLog_AuditSlashPath(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id, created_at FROM admin_audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2`)).
		WithArgs(50, 0).
		WillReturnRows(sqlmock.NewRows([]string{"admin_action", "target_user_id", "target_collection", "docs_deleted", "bytes_freed", "request_id", "created_at"}))

	req := httptest.NewRequest(http.MethodGet, "/admin/audit/", nil)
	rec := httptest.NewRecorder()
	h.AuditLog(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// --- SetupMigrations ---

func TestSetupMigrations_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}

	mock.ExpectExec(regexp.QuoteMeta(`CREATE TABLE IF NOT EXISTS admin_audit_log`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`CREATE INDEX IF NOT EXISTS idx_admin_audit_created_at`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DO $$`)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := h.SetupMigrations(context.Background()); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestSetupMigrations_KBDocumentsExists(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}

	mock.ExpectExec(regexp.QuoteMeta(`CREATE TABLE IF NOT EXISTS admin_audit_log`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`CREATE INDEX IF NOT EXISTS idx_admin_audit_created_at`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DO $$`)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := h.SetupMigrations(context.Background()); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestSetupMigrations_TableCreateError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}

	mock.ExpectExec(regexp.QuoteMeta(`CREATE TABLE IF NOT EXISTS admin_audit_log`)).
		WillReturnError(errors.New("permission denied"))

	if err := h.SetupMigrations(context.Background()); err == nil {
		t.Error("expected error, got nil")
	}
}

func TestSetupMigrations_IndexCreateError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}

	mock.ExpectExec(regexp.QuoteMeta(`CREATE TABLE IF NOT EXISTS admin_audit_log`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`CREATE INDEX IF NOT EXISTS idx_admin_audit_created_at`)).
		WillReturnError(errors.New("permission denied"))

	if err := h.SetupMigrations(context.Background()); err == nil {
		t.Error("expected error, got nil")
	}
}

func TestSetupMigrations_DOBlockError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}

	mock.ExpectExec(regexp.QuoteMeta(`CREATE TABLE IF NOT EXISTS admin_audit_log`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`CREATE INDEX IF NOT EXISTS idx_admin_audit_created_at`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DO $$`)).
		WillReturnError(errors.New("syntax error"))

	if err := h.SetupMigrations(context.Background()); err == nil {
		t.Error("expected error, got nil")
	}
}

// --- Helper functions ---

func TestExtractPathParam(t *testing.T) {
	tests := []struct {
		path   string
		prefix string
		sep    string
		want   string
	}{
		{"/admin/kb/users/user1/collections/default", "/admin/kb/users/", "/collections/", "user1"},
		{"/admin/kb/users/user1", "/admin/kb/users/", "/collections/", "user1"},
		{"/admin/kb/users/", "/admin/kb/users/", "/collections/", ""},
		{"/admin/kb/collections/test-col", "/admin/kb/collections/", "", "test-col"},
	}
	for _, tt := range tests {
		got := extractPathParam(tt.path, tt.prefix, tt.sep)
		if got != tt.want {
			t.Errorf("extractPathParam(%q, %q, %q) = %q, want %q", tt.path, tt.prefix, tt.sep, got, tt.want)
		}
	}
}

func TestSplitUserCollectionPath_EdgeCases(t *testing.T) {
	tests := []struct {
		path           string
		wantUserID     string
		wantCollection string
	}{
		{"/admin/kb/users/u1/collections/default", "u1", "default"},
		{"/admin/kb/users/user@domain.com/collections/research-data", "user@domain.com", "research-data"},
		{"/admin/kb/users/user1/collections/a", "user1", "a"},
		{"/admin/kb/users/user1", "", ""},
		{"/admin/kb/users/user1/collections/", "user1", ""},
		{"/admin/kb/users//collections/default", "", "default"},
		{"", "", ""},
	}
	for _, tt := range tests {
		userID, collection := splitUserCollectionPath(tt.path)
		if userID != tt.wantUserID || collection != tt.wantCollection {
			t.Errorf("splitUserCollectionPath(%q) = (%q, %q), want (%q, %q)", tt.path, userID, collection, tt.wantUserID, tt.wantCollection)
		}
	}
}

func TestParsePagination_InvalidLimitString(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/audit?limit=abc", nil)
	limit, offset := parsePagination(req, 50, 500)
	if limit != 50 || offset != 0 {
		t.Errorf("expected limit=50, offset=0, got limit=%d, offset=%d", limit, offset)
	}
}

func TestParsePagination_InvalidOffsetString(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/audit?offset=abc", nil)
	_, offset := parsePagination(req, 50, 500)
	if offset != 0 {
		t.Errorf("expected offset=0, got %d", offset)
	}
}

func TestParsePagination_NegativeOffset(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/audit?offset=-1", nil)
	_, offset := parsePagination(req, 50, 500)
	if offset != 0 {
		t.Errorf("expected offset=0 for negative, got %d", offset)
	}
}

func TestParsePagination_ZeroLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/audit?limit=0", nil)
	limit, _ := parsePagination(req, 50, 500)
	if limit != 50 {
		t.Errorf("expected default limit=50, got %d", limit)
	}
}

// --- Integration: admin endpoints via the full mux routing ---

func makeAdminMux(t *testing.T, db *sql.DB, adminKey string) http.Handler {
	t.Helper()
	adminHandler := NewHandler(db)
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/admin/kb/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			path := strings.TrimPrefix(r.URL.Path, "/admin/kb/users")
			if path == "" || path == "/" {
				adminHandler.ListUsers(w, r)
			} else if strings.HasSuffix(path, "/export") {
				adminHandler.ExportUser(w, r)
			} else {
				adminHandler.GetUser(w, r)
			}
		} else if r.Method == http.MethodDelete {
			adminHandler.DeleteUserData(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/admin/kb/users/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/admin/kb/users/")
		if strings.Contains(path, "/collections/") {
			if r.Method == http.MethodDelete {
				adminHandler.DeleteUserCollection(w, r)
			} else {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		} else if strings.HasSuffix(path, "/export") {
			adminHandler.ExportUser(w, r)
		} else if r.Method == http.MethodGet {
			adminHandler.GetUser(w, r)
		} else if r.Method == http.MethodDelete {
			adminHandler.DeleteUserData(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/admin/kb/collections/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			adminHandler.DeleteGlobalCollection(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/admin/audit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			adminHandler.AuditLog(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/admin/audit/", func(w http.ResponseWriter, r *http.Request) {
		adminHandler.AuditLog(w, r)
	})
	var keyHash [32]byte
	if adminKey != "" {
		keyHash = sha256.Sum256([]byte(adminKey))
	}
	return auth.BearerAuth(keyHash, auth.OnEmpty503, adminMux)
}

func TestAdminMux_ListUsers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := makeAdminMux(t, db, "test-key")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes FROM kb_documents GROUP BY user_id ORDER BY doc_count DESC`)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "doc_count", "bytes"}).AddRow("user1", 5, int64(1000)))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAdminMux_GetUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := makeAdminMux(t, db, "test-key")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT collection, COUNT(*) as doc_count, COALESCE(SUM(pg_column_size(metadata)), 0) as bytes FROM kb_documents WHERE user_id = $1 GROUP BY collection`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"collection", "doc_count", "bytes"}).AddRow("default", 3, int64(500)))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAdminMux_ExportUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := makeAdminMux(t, db, "test-key")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, doc_hash, file_path, collection, metadata, created_at FROM kb_documents WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)).
		WithArgs("user1", 100, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "doc_hash", "file_path", "collection", "metadata", "created_at"}).
			AddRow(1, "hash1", "/path/doc.txt", "default", `{"k":"v"}`, "2026-01-01T00:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAdminMux_DeleteUserData(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := makeAdminMux(t, db, "test-key")
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(2, int64(1000)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_user_id, docs_deleted, bytes_freed, request_id) VALUES ('delete_user', $1, $2, $3, $4)`)).
		WithArgs("user1", 2, int64(1000), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAdminMux_DeleteUserCollection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := makeAdminMux(t, db, "test-key")
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, int64(1500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1 AND collection = $2`)).
		WithArgs("user1", "research").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id) VALUES ('delete_user_collection', $1, $2, $3, $4, $5)`)).
		WithArgs("user1", "research", 3, int64(1500), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1/collections/research", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAdminMux_DeleteGlobalCollection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := makeAdminMux(t, db, "test-key")
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0) FROM kb_documents WHERE collection = $1`)).
		WithArgs("global-col").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(5, int64(2500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE collection = $1`)).
		WithArgs("global-col").
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_collection, docs_deleted, bytes_freed, request_id) VALUES ('delete_global_collection', $1, $2, $3, $4)`)).
		WithArgs("global-col", 5, int64(2500), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/collections/global-col", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAdminMux_AuditLog(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := makeAdminMux(t, db, "test-key")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id, created_at FROM admin_audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2`)).
		WithArgs(50, 0).
		WillReturnRows(sqlmock.NewRows([]string{"admin_action", "target_user_id", "target_collection", "docs_deleted", "bytes_freed", "request_id", "created_at"}).
			AddRow("delete_user", "user1", nil, 5, int64(2500), "req-1", "2026-01-01T00:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/admin/audit", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAdminMux_MethodNotAllowed(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := makeAdminMux(t, db, "test-key")

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/admin/kb/users"},
		{http.MethodPut, "/admin/kb/users/user1"},
		{http.MethodPost, "/admin/kb/collections/test-col"},
		{http.MethodPost, "/admin/audit"},
		{http.MethodPatch, "/admin/kb/users/user1/collections/test"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("expected 405, got %d for %s %s", rec.Code, tt.method, tt.path)
			}
		})
	}
}

func TestAdminMux_Unauthenticated(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := makeAdminMux(t, db, "test-key")

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAdminMux_Disabled(t *testing.T) {
	mux := makeAdminMux(t, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}
