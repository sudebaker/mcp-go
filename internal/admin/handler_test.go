package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func newTestHandler(t *testing.T) (*Handler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	return &Handler{db: db}, mock
}

func TestMiddleware_NoAdminKey(t *testing.T) {
	handler := Middleware("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["error"] != "admin endpoints disabled: ADMIN_API_KEY not set" {
		t.Errorf("unexpected error message: %s", body["error"])
	}
}

func TestMiddleware_NoAuthHeader(t *testing.T) {
	handler := Middleware("test-key", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_InvalidKey(t *testing.T) {
	handler := Middleware("test-key", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_ValidKey(t *testing.T) {
	called := false
	handler := Middleware("test-key", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		requestID := getRequestID(r.Context())
		if requestID == "" {
			t.Error("expected request_id in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler was not called")
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header")
	}
}

func TestMiddleware_RespectsClientRequestID(t *testing.T) {
	handler := Middleware("test-key", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Request-ID", "client-provided-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") != "client-provided-id" {
		t.Errorf("expected client-provided-id, got %s", rec.Header().Get("X-Request-ID"))
	}
}

func TestListUsers_Empty(t *testing.T) {
	h, mock := newTestHandler(t)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, COUNT(*) as doc_count, COALESCE(SUM(octet_length(content)), 0) as bytes FROM kb_documents GROUP BY user_id ORDER BY doc_count DESC`)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "doc_count", "bytes"}))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	rec := httptest.NewRecorder()
	h.ListUsers(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var users []userStat
	if err := json.NewDecoder(rec.Body).Decode(&users); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("expected empty list, got %d items", len(users))
	}
}

func TestListUsers_WithData(t *testing.T) {
	h, mock := newTestHandler(t)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id, COUNT(*) as doc_count, COALESCE(SUM(octet_length(content)), 0) as bytes FROM kb_documents GROUP BY user_id ORDER BY doc_count DESC`)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "doc_count", "bytes"}).
			AddRow("user1", 10, int64(5000)).
			AddRow("user2", 3, int64(1200)))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users", nil)
	rec := httptest.NewRecorder()
	h.ListUsers(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var users []userStat
	if err := json.NewDecoder(rec.Body).Decode(&users); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
	if users[0].UserID != "user1" || users[0].DocCount != 10 {
		t.Errorf("unexpected first user: %+v", users[0])
	}
}

func TestGetUser_InvalidUserID(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/has/slash", nil)
	rec := httptest.NewRecorder()
	h.GetUser(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestGetUser_Valid(t *testing.T) {
	h, mock := newTestHandler(t)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT collection, COUNT(*) as doc_count, COALESCE(SUM(octet_length(content)), 0) as bytes FROM kb_documents WHERE user_id = $1 GROUP BY collection`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"collection", "doc_count", "bytes"}).
			AddRow("default", 5, int64(2000)).
			AddRow("research", 3, int64(1500)))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1", nil)
	rec := httptest.NewRecorder()
	h.GetUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["user_id"] != "user1" {
		t.Errorf("expected user1, got %v", resp["user_id"])
	}
}

func TestDeleteUserData_InvalidUserID(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserData(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestDeleteUserData_NoDocs(t *testing.T) {
	h, mock := newTestHandler(t)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(octet_length(content)), 0) FROM kb_documents WHERE user_id = $1 FOR UPDATE`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(0, int64(0)))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserData(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp deleteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if !resp.Deleted || resp.DocsDeleted != 0 {
		t.Errorf("expected deleted=true, docs=0, got %+v", resp)
	}
}

func TestDeleteUserData_WithDocs(t *testing.T) {
	h, mock := newTestHandler(t)

	reqID := "test-request-id"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(octet_length(content)), 0) FROM kb_documents WHERE user_id = $1 FOR UPDATE`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(5, int64(2500)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_user_id, docs_deleted, bytes_freed, request_id) VALUES ('delete_user', $1, $2, $3, $4)`)).
		WithArgs("user1", 5, int64(2500), reqID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1", nil)

	// Wrap with middleware to inject request_id, then call DeleteUserData
	mw := Middleware("test-key", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/admin/kb/users/user1"
		h.DeleteUserData(w, r)
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
	if resp.DocsDeleted != 5 || resp.DocsBytesFreed != 2500 {
		t.Errorf("expected docs=5, bytes=2500, got %+v", resp)
	}
}

func TestDeleteUserCollection_InvalidCollection(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/users/user1/collections/!!!bad", nil)
	rec := httptest.NewRecorder()
	h.DeleteUserCollection(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestDeleteGlobalCollection_Empty(t *testing.T) {
	h, mock := newTestHandler(t)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(octet_length(content)), 0) FROM kb_documents WHERE collection = $1 FOR UPDATE`)).
		WithArgs("test-collection").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(0, int64(0)))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodDelete, "/admin/kb/collections/test-collection", nil)
	rec := httptest.NewRecorder()
	h.DeleteGlobalCollection(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp deleteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if !resp.Deleted || resp.DocsDeleted != 0 {
		t.Errorf("expected deleted=true, docs=0, got %+v", resp)
	}
}

func TestDeleteGlobalCollection_WithDocs(t *testing.T) {
	h, mock := newTestHandler(t)

	reqID := "test-req"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*), COALESCE(SUM(octet_length(content)), 0) FROM kb_documents WHERE collection = $1 FOR UPDATE`)).
		WithArgs("global-col").
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(10, int64(50000)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM kb_documents WHERE collection = $1`)).
		WithArgs("global-col").
		WillReturnResult(sqlmock.NewResult(0, 10))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO admin_audit_log(admin_action, target_collection, docs_deleted, bytes_freed, request_id) VALUES ('delete_global_collection', $1, $2, $3, $4)`)).
		WithArgs("global-col", 10, int64(50000), reqID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/admin/kb/collections/global-col", nil)
	mw := Middleware("test-key", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/admin/kb/collections/global-col"
		h.DeleteGlobalCollection(w, r)
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
	if resp.DocsDeleted != 10 || resp.DocsBytesFreed != 50000 {
		t.Errorf("expected docs=10, bytes=50000, got %+v", resp)
	}
}

func TestExportUser_InvalidUserID(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users//export", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestExportUser_Pagination(t *testing.T) {
	h, mock := newTestHandler(t)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM kb_documents WHERE user_id = $1`)).
		WithArgs("user1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, doc_hash, file_path, collection, metadata, created_at FROM kb_documents WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)).
		WithArgs("user1", 100, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "doc_hash", "file_path", "collection", "metadata", "created_at"}).
			AddRow(1, "abc123", "/data/doc1.txt", "default", `{"key":"value"}`, "2026-01-01T00:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/admin/kb/users/user1/export?limit=100&offset=0", nil)
	rec := httptest.NewRecorder()
	h.ExportUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	if total := rec.Header().Get("X-Total-Count"); total != "1" {
		t.Errorf("expected X-Total-Count=1, got %s", total)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp["user_id"] != "user1" {
		t.Errorf("expected user1, got %v", resp["user_id"])
	}
}

func TestAuditLog(t *testing.T) {
	h, mock := newTestHandler(t)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id, created_at FROM admin_audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2`)).
		WithArgs(50, 0).
		WillReturnRows(sqlmock.NewRows([]string{"admin_action", "target_user_id", "target_collection", "docs_deleted", "bytes_freed", "request_id", "created_at"}).
			AddRow("delete_user", "user1", nil, 5, int64(2500), "req-1", "2026-01-01T00:00:00Z"))

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
	entries, ok := resp["entries"].([]interface{})
	if !ok {
		t.Fatal("expected entries array")
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestParsePagination_Defaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/audit", nil)
	limit, offset := parsePagination(req, 50, 500)
	if limit != 50 || offset != 0 {
		t.Errorf("expected limit=50, offset=0, got limit=%d, offset=%d", limit, offset)
	}
}

func TestParsePagination_Custom(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/audit?limit=10&offset=20", nil)
	limit, offset := parsePagination(req, 50, 500)
	if limit != 10 || offset != 20 {
		t.Errorf("expected limit=10, offset=20, got limit=%d, offset=%d", limit, offset)
	}
}

func TestParsePagination_MaxLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/audit?limit=5000", nil)
	limit, _ := parsePagination(req, 50, 1000)
	if limit != 1000 {
		t.Errorf("expected limit clamped to 1000, got %d", limit)
	}
}

func TestSplitUserCollectionPath(t *testing.T) {
	tests := []struct {
		path           string
		wantUserID     string
		wantCollection string
	}{
		{"/admin/kb/users/u1/collections/default", "u1", "default"},
		{"/admin/kb/users/test@user/collections/research-data", "test@user", "research-data"},
		{"/admin/kb/users/user1", "", ""},
	}

	for _, tt := range tests {
		userID, collection := splitUserCollectionPath(tt.path)
		if userID != tt.wantUserID || collection != tt.wantCollection {
			t.Errorf("splitUserCollectionPath(%q) = (%q, %q), want (%q, %q)", tt.path, userID, collection, tt.wantUserID, tt.wantCollection)
		}
	}
}

func TestUserIDPattern_Valid(t *testing.T) {
	valid := []string{"user1", "test.user@domain", "admin-1", "user_name", "a@b.co"}
	for _, v := range valid {
		if !userIDPattern.MatchString(v) {
			t.Errorf("expected valid: %q", v)
		}
	}
}

func TestUserIDPattern_Invalid(t *testing.T) {
	invalid := []string{"", "user/name", strings.Repeat("a", 300)}
	for _, v := range invalid {
		if userIDPattern.MatchString(v) {
			t.Errorf("expected invalid: %q", v)
		}
	}
}

func TestCollectionPattern_Valid(t *testing.T) {
	valid := []string{"default", "research-data", "forensic_2026", "a", strings.Repeat("b", 100)}
	for _, v := range valid {
		if !collectionPattern.MatchString(v) {
			t.Errorf("expected valid: %q", v)
		}
	}
}

func TestCollectionPattern_Invalid(t *testing.T) {
	invalid := []string{"", "has space", "has.dot", "has/slash", strings.Repeat("c", 101)}
	for _, v := range invalid {
		if collectionPattern.MatchString(v) {
			t.Errorf("expected invalid: %q", v)
		}
	}
}
