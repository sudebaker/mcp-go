package health

import (
	"context"
	"testing"
	"time"

	"github.com/sudebaker/mcp-go/internal/config"
)

func TestHealthStatus(t *testing.T) {
	tests := []struct {
		name     string
		status   HealthStatus
		expected string
	}{
		{"healthy", StatusHealthy, "healthy"},
		{"degraded", StatusDegraded, "degraded"},
		{"unhealthy", StatusUnhealthy, "unhealthy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.status) != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, tt.status)
			}
		})
	}
}

func TestCheckResult(t *testing.T) {
	result := CheckResult{
		Name:      "test_check",
		Status:    StatusHealthy,
		Message:   "Test message",
		Duration:  100 * time.Millisecond,
		Timestamp: time.Now(),
	}

	if result.Name != "test_check" {
		t.Error("Name mismatch")
	}
	if result.Status != StatusHealthy {
		t.Error("Status mismatch")
	}
	if result.Message != "Test message" {
		t.Error("Message mismatch")
	}
}

func TestGetOverallStatus_AllHealthy(t *testing.T) {
	results := []CheckResult{
		{Name: "a", Status: StatusHealthy},
		{Name: "b", Status: StatusHealthy},
		{Name: "c", Status: StatusHealthy},
	}

	checker := NewChecker(nil, nil, nil, nil)
	status := checker.GetOverallStatus(results)
	if status != StatusHealthy {
		t.Errorf("expected %s, got %s", StatusHealthy, status)
	}
}

func TestGetOverallStatus_OneDegraded(t *testing.T) {
	results := []CheckResult{
		{Name: "a", Status: StatusHealthy},
		{Name: "b", Status: StatusDegraded},
		{Name: "c", Status: StatusHealthy},
	}

	checker := NewChecker(nil, nil, nil, nil)
	status := checker.GetOverallStatus(results)
	if status != StatusDegraded {
		t.Errorf("expected %s, got %s", StatusDegraded, status)
	}
}

func TestGetOverallStatus_OneUnhealthy(t *testing.T) {
	results := []CheckResult{
		{Name: "a", Status: StatusHealthy},
		{Name: "b", Status: StatusUnhealthy},
		{Name: "c", Status: StatusHealthy},
	}

	checker := NewChecker(nil, nil, nil, nil)
	status := checker.GetOverallStatus(results)
	if status != StatusUnhealthy {
		t.Errorf("expected %s, got %s", StatusUnhealthy, status)
	}
}

func TestGetOverallStatus_Mixed(t *testing.T) {
	results := []CheckResult{
		{Name: "a", Status: StatusHealthy},
		{Name: "b", Status: StatusUnhealthy},
		{Name: "c", Status: StatusDegraded},
	}

	checker := NewChecker(nil, nil, nil, nil)
	status := checker.GetOverallStatus(results)
	if status != StatusUnhealthy {
		t.Errorf("expected %s (unhealthy takes precedence), got %s", StatusUnhealthy, status)
	}
}

func TestGetHealthMetrics(t *testing.T) {
	metrics := GetHealthMetrics()

	if _, ok := metrics["heap_alloc_bytes"]; !ok {
		t.Error("heap_alloc_bytes not in metrics")
	}
	if _, ok := metrics["goroutines"]; !ok {
		t.Error("goroutines not in metrics")
	}

	if metrics["heap_alloc_bytes"] < 0 {
		t.Error("heap_alloc_bytes should be non-negative")
	}
	if metrics["goroutines"] < 0 {
		t.Error("goroutines should be non-negative")
	}
}

func TestChecker_CheckConfig_NoConfig(t *testing.T) {
	checker := NewChecker(nil, nil, nil, nil)
	result := checker.checkConfig(context.Background())

	if result.Status != StatusUnhealthy {
		t.Errorf("expected %s, got %s", StatusUnhealthy, result.Status)
	}
	if result.Name != "config" {
		t.Error("name mismatch")
	}
}

func TestChecker_CheckMemory(t *testing.T) {
	checker := NewChecker(nil, nil, nil, nil)
	result := checker.checkMemory(context.Background())

	if result.Name != "memory" {
		t.Error("name mismatch")
	}
	if result.Timestamp.IsZero() {
		t.Error("timestamp should be set")
	}
	if result.Duration == 0 {
		t.Error("duration should be set")
	}
}

// --- Dependency check tests ---

func TestBuildDependencies_EmptyConfig(t *testing.T) {
	deps := BuildDependencies(nil)
	if deps != nil {
		t.Errorf("expected nil deps for nil config, got %d", len(deps))
	}

	emptyCfg := &config.Config{}
	deps = BuildDependencies(emptyCfg)
	// redis and postgres only included when REDIS_URL and DATABASE_URL are set
	if len(deps) != 0 {
		t.Errorf("expected 0 deps without env vars, got %d", len(deps))
	}
}

func TestBuildDependencies_WithTools(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("DATABASE_URL", "postgres://localhost:5432")

	cfg := &config.Config{
		Tools: []config.ToolConfig{
			{Name: "browser_scraper"},
			{Name: "searxng_search"},
			{Name: "rustfs_storage"},
			{Name: "analyze_image"},
		},
	}

	deps := BuildDependencies(cfg)
	// redis + postgres (2) + crawl4ai, searxng, rustfs, ollama (4) = 6
	if len(deps) != 6 {
		t.Errorf("expected 6 deps, got %d: %v", len(deps), deps)
	}

	names := make(map[string]bool)
	for _, d := range deps {
		names[d.Name] = true
	}

	expected := []string{"redis", "postgres", "crawl4ai", "searxng", "rustfs", "ollama"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected dependency %q not found", name)
		}
	}
}

func TestBuildDependencies_NoExternalTools(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("DATABASE_URL", "postgres://localhost:5432")

	cfg := &config.Config{
		Tools: []config.ToolConfig{
			{Name: "echo"},
			{Name: "datetime"},
		},
	}

	deps := BuildDependencies(cfg)
	// Only redis + postgres (no external tool deps)
	if len(deps) != 2 {
		t.Errorf("expected 2 deps (redis, postgres only), got %d", len(deps))
	}
}

func TestCheckDependency_EmptyURL(t *testing.T) {
	checker := NewChecker(nil, nil, nil, nil)
	dep := DependencyCheck{
		Name: "test-dep",
		URL:  "",
		Tool: "test_tool",
	}

	result := checker.checkDependency(context.Background(), dep)
	if result.Status != StatusDegraded {
		t.Errorf("expected degraded for empty URL, got %s", result.Status)
	}
	if result.Name != "test-dep" {
		t.Errorf("expected name 'test-dep', got %s", result.Name)
	}
}

func TestCheckDependency_Unreachable(t *testing.T) {
	checker := NewChecker(nil, nil, nil, nil)
	dep := DependencyCheck{
		Name: "unreachable-dep",
		URL:  "127.0.0.1:19999", // no service listening here
		Tool: "test_tool",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result := checker.checkDependency(ctx, dep)
	if result.Status != StatusUnhealthy {
		t.Errorf("expected unhealthy for unreachable dep, got %s: %s", result.Status, result.Message)
	}
}

func TestRunAllChecks_WithDependencies(t *testing.T) {
	deps := []DependencyCheck{
		{Name: "test-dep", URL: "", Tool: "test_tool"},
	}

	checker := NewChecker(nil, nil, nil, deps)
	results := checker.RunAllChecks(context.Background())

	// Should include: config, memory + test-dep = 3
	// (redis/postgres only added when redisClient/db are non-nil)
	if len(results) != 3 {
		t.Errorf("expected 3 checks (config, memory, test-dep), got %d", len(results))
	}

	// Verify test-dep is in results
	found := false
	for _, r := range results {
		if r.Name == "test-dep" {
			found = true
			if r.Status != StatusDegraded {
				t.Errorf("expected test-dep to be degraded (empty URL), got %s", r.Status)
			}
		}
	}
	if !found {
		t.Error("test-dep check not found in results")
	}
}

func TestDependencyStatus(t *testing.T) {
	ds := DependencyStatus{
		Name:      "crawl4ai",
		URL:       "crawl4ai:8000",
		Reachable: true,
	}

	if ds.Name != "crawl4ai" {
		t.Error("Name mismatch")
	}
	if ds.URL != "crawl4ai:8000" {
		t.Error("URL mismatch")
	}
	if !ds.Reachable {
		t.Error("Reachable should be true")
	}
	if ds.Error != "" {
		t.Error("Error should be empty for reachable dependency")
	}
}

func TestCheckDependencies_WithUnreachable(t *testing.T) {
	deps := []DependencyCheck{
		{Name: "redis", URL: "", Critical: false},
		{Name: "postgres", URL: "", Critical: false},
		{Name: "unreachable-dep", URL: "127.0.0.1:19999", Tool: "test_tool"},
	}

	checker := NewChecker(nil, nil, nil, deps)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	statuses := checker.CheckDependencies(ctx)
	if len(statuses) < 1 {
		t.Error("expected at least 1 status result")
	}

	// Only non-redis/postgres deps are in the result (those are skipped)
	found := false
	for _, s := range statuses {
		if s.Name == "unreachable-dep" {
			found = true
			if s.Reachable {
				t.Error("expected unreachable-dep to not be reachable")
			}
		}
	}
	if !found {
		t.Error("unreachable-dep not found in dependency statuses")
	}
}

func TestExtractHostPort(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"http://browserless:3000", "browserless:3000"},
		{"http://searxng:8080", "searxng:8080"},
		{"https://ollama.example.com:11434", "ollama.example.com:11434"},
		{"rustfs:9000", "rustfs:9000"},
		{"http://localhost:8080/path", "localhost:8080"},
		{"invalid-url", "invalid-url"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := extractHostPort(tt.input)
			if result != tt.expected {
				t.Errorf("extractHostPort(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
