package transport

import (
	"net/http"
	"testing"

	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/health"
)

// BuildDependencies is what decides which services are critical in production.
// health_status_test.go covers the rule with synthetic DependencyChecks, but
// nothing asserted the wiring: flipping `Critical: false` to true for an
// optional service leaves every rule-level test green while the real server
// starts answering 503 (and Docker's `curl -fsS` marks the container unhealthy)
// on a blip of that single optional service.
//
// This test walks the real path — env vars → BuildDependencies → NewChecker →
// GET /health — with every optional service unreachable.
func TestHealthCriticalityWiring_OptionalServicesNeverCause503(t *testing.T) {
	addr := unreachableAddr(t)
	t.Setenv("SEARXNG_URL", "http://"+addr)
	t.Setenv("CRAWL4AI_URL", "http://"+addr)
	t.Setenv("RUSTFS_ENDPOINT", addr)
	t.Setenv("LLM_API_URL", "http://"+addr)
	// Core datastores are wired through their own clients; keep them out of the
	// dependency list so this test measures only the optional services.
	t.Setenv("REDIS_URL", "")
	t.Setenv("DATABASE_URL", "")

	cfg := &config.Config{Tools: []config.ToolConfig{
		{Name: "searxng_search"},
		{Name: "browser_scraper"},
		{Name: "rustfs_storage"},
		{Name: "analyze_image"},
		{Name: "echo"},
	}}

	deps := health.BuildDependencies(cfg)
	if len(deps) != 4 {
		t.Fatalf("expected 4 optional dependencies from the config, got %d: %+v", len(deps), deps)
	}
	for _, dep := range deps {
		if dep.Critical {
			t.Errorf("dependency %q is wired as critical; an optional service must not fail /health", dep.Name)
		}
	}

	s := &MCPServer{
		serverName:    "mcp-orchestrator",
		version:       "test",
		healthChecker: health.NewChecker(cfg, nil, nil, deps),
	}

	w, body := getHealth(t, s)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with only optional dependencies unreachable, got %d", w.Code)
	}
	if body.Status == string(health.StatusUnhealthy) {
		t.Errorf("expected status other than %q, got %q", health.StatusUnhealthy, body.Status)
	}
	for _, name := range []string{"searxng", "crawl4ai", "rustfs", "ollama"} {
		if got := body.Dependencies[name]; got != string(health.StatusDegraded) {
			t.Errorf("expected dependencies.%s=%q, got %q", name, health.StatusDegraded, got)
		}
	}
}
