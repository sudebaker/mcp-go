package transport

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/health"
)

// unreachableAddr returns a host:port that nothing is listening on. Binding and
// closing a port keeps the test independent of any hard-coded port staying free.
func unreachableAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("failed to close the reservation listener: %v", err)
	}
	return addr
}

// healthTestServer builds an MCPServer whose only check targets one external
// dependency, with the given criticality. Everything else (config, memory) is
// healthy, so the dependency alone decides the /health status.
func healthTestServer(t *testing.T, dep health.DependencyCheck) *MCPServer {
	t.Helper()
	cfg := &config.Config{Tools: []config.ToolConfig{{Name: "searxng_search"}}}
	return &MCPServer{
		serverName:    "mcp-orchestrator",
		version:       "test",
		healthChecker: health.NewChecker(cfg, nil, nil, []health.DependencyCheck{dep}),
	}
}

// healthResponse is the subset of the /health JSON body these tests assert on.
type healthResponse struct {
	Status       string            `json:"status"`
	Dependencies map[string]string `json:"dependencies"`
}

func getHealth(t *testing.T, s *MCPServer) (*httptest.ResponseRecorder, healthResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	s.handleHealth(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}

	var body healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid /health JSON body %q: %v", w.Body.String(), err)
	}
	t.Logf("GET /health -> %d %s", w.Code, w.Body.String())
	return w, body
}

// An unreachable non-critical dependency must not turn /health into a 503:
// Docker's healthcheck (curl -fsS) fails on 5xx, so a searxng/crawl4ai/ollama/
// rustfs blip would otherwise mark the container unhealthy.
func TestHandleHealth_NonCriticalDependencyUnreachable(t *testing.T) {
	s := healthTestServer(t, health.DependencyCheck{
		Name:     "searxng",
		URL:      unreachableAddr(t),
		Tool:     "searxng_search",
		Critical: false,
	})

	w, body := getHealth(t, s)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for unreachable non-critical dependency, got %d", w.Code)
	}
	if body.Status != string(health.StatusDegraded) {
		t.Errorf("expected status %q, got %q", health.StatusDegraded, body.Status)
	}
	if got := body.Dependencies["searxng"]; got != string(health.StatusDegraded) {
		t.Errorf("expected dependencies.searxng=%q, got %q", health.StatusDegraded, got)
	}
}

// A critical dependency is a real failure: /health keeps answering 503.
func TestHandleHealth_CriticalDependencyUnreachable(t *testing.T) {
	s := healthTestServer(t, health.DependencyCheck{
		Name:     "critical-dep",
		URL:      unreachableAddr(t),
		Tool:     "kb_search",
		Critical: true,
	})

	w, body := getHealth(t, s)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for unreachable critical dependency, got %d", w.Code)
	}
	if body.Status != string(health.StatusUnhealthy) {
		t.Errorf("expected status %q, got %q", health.StatusUnhealthy, body.Status)
	}
	if got := body.Dependencies["critical-dep"]; got != string(health.StatusUnhealthy) {
		t.Errorf("expected dependencies.critical-dep=%q, got %q", health.StatusUnhealthy, got)
	}
}
