package transport

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"

	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/health"
)

// On this branch only dependencies flagged Critical turn /health into a 503.
// redis and postgres are declared Critical but are checked through their own
// clients, not through DependencyCheck.Critical: a failed ping must keep
// answering 503 so the new criticality rule does not quietly demote the two
// datastores to a degraded (HTTP 200) state.
func TestHandleHealth_RedisPingFailureIs503(t *testing.T) {
	addr := unreachableAddr(t)
	client := redis.NewClient(&redis.Options{
		Addr:        addr,
		MaxRetries:  -1,
		DialTimeout: 500 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	s := &MCPServer{
		serverName:    "mcp-orchestrator",
		version:       "test",
		healthChecker: health.NewChecker(&config.Config{}, client, nil, nil),
	}

	w, body := getHealth(t, s)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when redis ping fails, got %d", w.Code)
	}
	if body.Status != string(health.StatusUnhealthy) {
		t.Errorf("expected status %q, got %q", health.StatusUnhealthy, body.Status)
	}
	if got := body.Dependencies["redis"]; got != string(health.StatusUnhealthy) {
		t.Errorf("expected dependencies.redis=%q, got %q", health.StatusUnhealthy, got)
	}
}

func TestHandleHealth_PostgresPingFailureIs503(t *testing.T) {
	addr := unreachableAddr(t)
	db, err := sql.Open("postgres", "postgres://qa:qa@"+addr+"/qa?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := &MCPServer{
		serverName:    "mcp-orchestrator",
		version:       "test",
		healthChecker: health.NewChecker(&config.Config{}, nil, db, nil),
	}

	w, body := getHealth(t, s)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when postgres ping fails, got %d", w.Code)
	}
	if body.Status != string(health.StatusUnhealthy) {
		t.Errorf("expected status %q, got %q", health.StatusUnhealthy, body.Status)
	}
	if got := body.Dependencies["postgres"]; got != string(health.StatusUnhealthy) {
		t.Errorf("expected dependencies.postgres=%q, got %q", health.StatusUnhealthy, got)
	}
}

// With a valid config and no external checks failing, nothing may turn the
// answer into a 503 or report an unhealthy dependency: the criticality rule
// must not demote a serving server into "replace me".
//
// The overall status is asserted as "not unhealthy" rather than "healthy"
// because the memory check scores the real cgroup usage of the host running the
// tests: on a machine where that usage is above the legacy thresholds the answer
// is legitimately "degraded" (still 200). Asserting "healthy" makes the test
// depend on the machine instead of on the rule under test.
func TestHandleHealth_NoClientsStaysHealthy(t *testing.T) {
	cfg := &config.Config{Tools: []config.ToolConfig{{Name: "searxng_search"}}}
	s := &MCPServer{
		serverName:    "mcp-orchestrator",
		version:       "test",
		healthChecker: health.NewChecker(cfg, nil, nil, nil),
	}

	w, body := getHealth(t, s)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with no external checks failing, got %d", w.Code)
	}
	if body.Status == string(health.StatusUnhealthy) {
		t.Errorf("expected status other than %q with no external checks failing, got %q",
			health.StatusUnhealthy, body.Status)
	}
	for name, dep := range body.Dependencies {
		if dep == string(health.StatusUnhealthy) {
			t.Errorf("expected no unhealthy dependency, got dependencies.%s=%q", name, dep)
		}
	}
}
