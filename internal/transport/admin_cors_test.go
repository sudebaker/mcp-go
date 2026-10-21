package transport

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/lib/pq" // registers the "postgres" driver used by adminTestDB
)

// adminTestDB returns an open *sql.DB whose DSN points at a port nothing
// listens on. sql.Open does not dial until a query runs, and the CORS layer
// answers every request tested here before any handler reaches the database;
// SetupMigrations only logs its failure, so the admin mount proceeds as in a
// real deployment.
func adminTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", "postgresql://mcp:test@127.0.0.1:1/knowledge?sslmode=disable")
	if err != nil {
		t.Fatalf("failed to open admin test db handle: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func adminTestServer(t *testing.T, adminKey string, origins []string, withDB bool) *MCPServer {
	t.Helper()
	s := NewMCPServer(nil, MCPConfig{
		Host:           "127.0.0.1",
		Port:           0,
		ServerName:     "test-server",
		Version:        "1.0.0",
		AllowedOrigins: origins,
		AdminKey:       adminKey,
	})
	if withDB {
		s.db = adminTestDB(t)
	}
	return s
}

func serveAdmin(t *testing.T, s *MCPServer, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.registerAdminRoutes(mux)

	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Fail-closed: with ADMIN_API_KEY set and no explicit allowed_origins the admin
// surface is not mounted at all — not even for an origin that would otherwise
// be allowed. The DB is present on purpose, so the refusal is proven to come
// from the CORS guard and not from the "admin disabled" early return.
func TestAdminRoutes_RefusedWhenKeySetWithoutOrigins(t *testing.T) {
	s := adminTestServer(t, "admin-key-abc12345", nil, true)

	for _, origin := range []string{"https://evil.example", "https://your-ui.example.com", ""} {
		headers := map[string]string{}
		if origin != "" {
			headers["Origin"] = origin
		}
		rec := serveAdmin(t, s, http.MethodGet, "/admin/kb/users", headers)
		if rec.Code != http.StatusNotFound {
			t.Errorf("origins empty: /admin/kb/users (Origin %q) expected 404 not-mounted, got %d", origin, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origins empty: unexpected CORS header %q on a not-mounted surface", got)
		}
	}
}

// With an explicit origin list the admin surface mounts and its CORS is
// restricted: disallowed browsers are refused before the key is checked,
// allowed origins (and Origin-less CLI clients) reach the bearer check.
func TestAdminRoutes_CORSRestrictedWhenOriginsSet(t *testing.T) {
	const allowedOrigin = "https://your-ui.example.com"
	s := adminTestServer(t, "admin-key-abc12345", []string{allowedOrigin}, true)

	// Disallowed origin: 403 from CORS, before auth and before any handler.
	rec := serveAdmin(t, s, http.MethodGet, "/admin/kb/users", map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("disallowed origin expected 403, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin must not get CORS headers, got %q", got)
	}

	// Allowed origin without a token: past CORS, refused by the bearer check.
	rec = serveAdmin(t, s, http.MethodGet, "/admin/kb/users", map[string]string{"Origin": allowedOrigin})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("allowed origin without token expected 401, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("allowed origin expected Access-Control-Allow-Origin %q, got %q", allowedOrigin, got)
	}

	// Preflight for the allowed origin is answered by the CORS layer itself.
	rec = serveAdmin(t, s, http.MethodOptions, "/admin/kb/users", map[string]string{
		"Origin":                        allowedOrigin,
		"Access-Control-Request-Method": http.MethodDelete,
	})
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("preflight expected Access-Control-Allow-Origin %q, got %q", allowedOrigin, got)
	}

	// curl-like request (no Origin header) is not subject to CORS.
	rec = serveAdmin(t, s, http.MethodGet, "/admin/kb/users", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no-Origin request expected 401 from the bearer check, got %d", rec.Code)
	}
}

// Without ADMIN_API_KEY the admin surface stays disabled, exactly as before the
// fail-closed rule.
func TestAdminRoutes_DisabledWithoutAdminKey(t *testing.T) {
	s := adminTestServer(t, "", []string{"https://your-ui.example.com"}, true)

	rec := serveAdmin(t, s, http.MethodGet, "/admin/kb/users", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("no ADMIN_API_KEY: expected 404 not-mounted, got %d", rec.Code)
	}
}
