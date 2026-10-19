package health

import (
	"context"
	"net"
	"os"
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
	// Regression: a heap over the old 500 MB absolute threshold used to report
	// unhealthy, which turned /health into 503 and marked the container
	// unhealthy after a smoke run. Memory pressure must never be fatal.
	if result.Status == StatusUnhealthy {
		t.Errorf("memory check must never report unhealthy, got %s (%s)", result.Status, result.Message)
	}
}

func TestClassifyMemory_RelativeToBudget(t *testing.T) {
	const budget = int64(2) << 30 // the 2G cgroup limit of the mcp-server container

	tests := []struct {
		name       string
		heap       int64
		wantStatus HealthStatus
		wantSuffix string
	}{
		{"idle", 100 << 20, StatusHealthy, ""},
		{"just under 70%", 1400 << 20, StatusHealthy, ""},
		{"over 70%", 1500 << 20, StatusDegraded, " (elevated memory usage)"},
		{"over 90%", 1900 << 20, StatusDegraded, " (memory near budget)"},
		{"over budget", 3 << 30, StatusDegraded, " (memory near budget)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, suffix := classifyMemory(tt.heap, budget)
			if status != tt.wantStatus {
				t.Errorf("status: expected %s, got %s", tt.wantStatus, status)
			}
			if suffix != tt.wantSuffix {
				t.Errorf("suffix: expected %q, got %q", tt.wantSuffix, suffix)
			}
			if status == StatusUnhealthy {
				t.Error("classifyMemory must never return unhealthy")
			}
		})
	}
}

func TestClassifyMemory_NoBudgetUsesLegacyThresholds(t *testing.T) {
	tests := []struct {
		name       string
		heap       int64
		wantStatus HealthStatus
	}{
		{"below 250MB", 100 << 20, StatusHealthy},
		{"between 250MB and 500MB", 300 << 20, StatusDegraded},
		{"above the old 500MB ceiling", 600 << 20, StatusDegraded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _ := classifyMemory(tt.heap, 0)
			if status != tt.wantStatus {
				t.Errorf("expected %s, got %s", tt.wantStatus, status)
			}
		})
	}
}

func TestReadMemoryLimitFile(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name    string
		content string
		want    int64
	}{
		{"cgroup v2 limit", "2147483648\n", 2 << 30},
		{"cgroup v2 unlimited", "max\n", 0},
		{"cgroup v1 unlimited sentinel", "9223372036854771712\n", 0},
		{"empty file", "\n", 0},
		{"garbage", "not-a-number\n", 0},
		{"zero", "0\n", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := dir + "/" + tt.name
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			if got := readMemoryLimitFile(path); got != tt.want {
				t.Errorf("expected %d, got %d", tt.want, got)
			}
		})
	}

	if got := readMemoryLimitFile(dir + "/does-not-exist"); got != 0 {
		t.Errorf("missing file: expected 0, got %d", got)
	}
}

func TestMemoryBudget_DetectsSomethingOnLinux(t *testing.T) {
	// Not an assertion on a specific value: on a container this is the cgroup
	// limit, on bare metal it may be 0 (no cgroup limit, no GOMEMLIMIT).
	budget := memoryBudget()
	if budget < 0 {
		t.Errorf("budget must never be negative, got %d", budget)
	}
	t.Logf("detected memory budget: %d bytes (%.0f MB)", budget, float64(budget)/1024/1024)
}

func TestResolveCgroupV2LimitPath(t *testing.T) {
	tests := []struct {
		name string
		proc string
		want string
	}{
		{
			name: "container root",
			proc: "0::/\n",
			want: "/sys/fs/cgroup/memory.max",
		},
		{
			name: "systemd user slice",
			proc: "0::/user.slice/user-1000.slice/session-3.scope\n",
			want: "/sys/fs/cgroup/user.slice/user-1000.slice/session-3.scope/memory.max",
		},
		{
			name: "v1 only host",
			proc: "12:memory:/docker/abc\n1:name=systemd:/\n",
			want: "",
		},
		{
			name: "empty",
			proc: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveCgroupV2LimitPath(tt.proc); got != tt.want {
				t.Errorf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestGetHealthMetrics_ExposesMemoryRatio(t *testing.T) {
	metrics := GetHealthMetrics()
	if _, ok := metrics["memory_budget_bytes"]; !ok {
		t.Error("memory_budget_bytes metric is missing")
	}
	if ratio, ok := metrics["memory_used_ratio"]; !ok {
		t.Error("memory_used_ratio metric is missing")
	} else if ratio < 0 || ratio > 1 {
		t.Errorf("memory_used_ratio out of range: %f", ratio)
	}
}

// TestHighHeapSimulatedDoesNotFailHealth covers the deployed incident: after
// 61 tool calls the heap sat at 531 MB and /health answered 503, so the image
// HEALTHCHECK (curl -f /health) marked the container unhealthy even though the
// process was serving and the GC reclaimed the heap seconds later.
//
// The simulated heap goes through the real classifier and the real aggregation
// used by handleHealth (internal/transport/sse.go maps exactly StatusUnhealthy
// to 503), so no unavailable memory budget is invented for the test.
func TestHighHeapSimulatedDoesNotFailHealth(t *testing.T) {
	const budget = int64(2) << 30 // the mcp-server compose memory limit

	checker := NewChecker(&config.Config{Tools: []config.ToolConfig{{Name: "kb_search"}}}, nil, nil, nil)

	for _, heap := range []int64{531 << 20, 1600 << 20, 2 << 30, 4 << 30} {
		status, _ := classifyMemory(heap, budget)
		if status == StatusUnhealthy {
			t.Fatalf("heap %d MB must never classify as unhealthy", heap>>20)
		}

		results := []CheckResult{
			{Name: "config", Status: StatusHealthy},
			{Name: "memory", Status: status},
		}
		if overall := checker.GetOverallStatus(results); overall == StatusUnhealthy {
			t.Errorf("heap %d MB: overall status must not be unhealthy (got %s, /health would answer 503)", heap>>20, overall)
		}
	}
}

// TestHighHeapDoesNotMaskFatalChecks is the other half of the contract: the
// memory ceiling must not hide a dependency that really cannot serve.
func TestHighHeapDoesNotMaskFatalChecks(t *testing.T) {
	checker := NewChecker(&config.Config{Tools: []config.ToolConfig{{Name: "kb_search"}}}, nil, nil, nil)

	status, _ := classifyMemory(2<<30, 2<<30) // at the budget: degraded
	results := []CheckResult{
		{Name: "config", Status: StatusHealthy},
		{Name: "memory", Status: status},
		{Name: "postgres", Status: StatusUnhealthy},
	}

	if overall := checker.GetOverallStatus(results); overall != StatusUnhealthy {
		t.Errorf("expected unhealthy when a fatal check fails, got %s", overall)
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

func TestCheckDependency_URLWithScheme(t *testing.T) {
	// Start a local TCP listener so the dependency is actually reachable.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()

	checker := NewChecker(nil, nil, nil, nil)
	dep := DependencyCheck{
		Name: "reachable-with-scheme",
		// Pass a full URL with scheme; checkDependency must extract host:port before dialing.
		URL:  "http://" + ln.Addr().String(),
		Tool: "test_tool",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result := checker.checkDependency(ctx, dep)
	if result.Status != StatusHealthy {
		t.Errorf("expected healthy for reachable http:// URL, got %s: %s", result.Status, result.Message)
	}
	if result.Name != "reachable-with-scheme" {
		t.Errorf("expected name 'reachable-with-scheme', got %s", result.Name)
	}
}

func TestCheckDependencies_URLWithScheme(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()

	deps := []DependencyCheck{
		{Name: "reachable-with-scheme", URL: "http://" + ln.Addr().String(), Tool: "test_tool"},
	}
	checker := NewChecker(nil, nil, nil, deps)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	statuses := checker.CheckDependencies(ctx)
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if !statuses[0].Reachable {
		t.Errorf("expected reachable-with-scheme to be reachable, got error: %s", statuses[0].Error)
	}
	if statuses[0].URL != "http://"+ln.Addr().String() {
		t.Errorf("expected URL to be preserved, got %s", statuses[0].URL)
	}
}
