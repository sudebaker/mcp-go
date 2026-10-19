package health

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/sudebaker/mcp-go/internal/config"
)

// HealthStatus represents the health state of a component or the overall system.
type HealthStatus string

const (
	StatusHealthy   HealthStatus = "healthy"   // Fully operational
	StatusDegraded  HealthStatus = "degraded"  // Partial functionality, warning
	StatusUnhealthy HealthStatus = "unhealthy" // Critical failure
)

// CheckResult represents a single health check outcome with timing information.
type CheckResult struct {
	Name      string        `json:"name"`              // Name of the checked component
	Status    HealthStatus  `json:"status"`            // Health state
	Message   string        `json:"message,omitempty"` // Human-readable details
	Duration  time.Duration `json:"duration_ms"`       // Check execution time in ms
	Timestamp time.Time     `json:"timestamp"`         // When the check ran
}

// DependencyStatus represents the reachability test result for an external dependency.
type DependencyStatus struct {
	Name      string `json:"name"`            // Dependency name (e.g., "searxng")
	URL       string `json:"url"`             // Resolved URL tested
	Reachable bool   `json:"reachable"`       // True if TCP connection succeeded
	Error     string `json:"error,omitempty"` // Connection error message if unreachable
}

// Checker performs health checks against external dependencies and system resources.
// It validates connectivity to Redis, PostgreSQL, checks memory usage, and verifies
// configuration integrity. Results are used for monitoring and alerting.
type Checker struct {
	cfg         *config.Config // Server configuration for tool validation
	redisClient *redis.Client  // Redis connection for ping check
	db          *sql.DB        // PostgreSQL connection for ping check
	// Dependencies tracks which external services to check based on configured tools.
	Dependencies []DependencyCheck
}

// DependencyCheck describes an external service to verify.
type DependencyCheck struct {
	Name string // Human-readable name (e.g., "searxng")
	URL  string // TCP address (host:port) extracted from env/config
	Tool string // Associated tool name (e.g., "searxng_search"), for mapping
	// Critical decides what an unreachable dependency means: true makes /health
	// answer 503 (StatusUnhealthy), false only degrades it (StatusDegraded, HTTP
	// 200) so a blip of an optional service does not mark the container broken.
	//
	// It does not apply to the "redis" and "postgres" entries: RunAllChecks skips
	// them because they are checked through their own clients (ping), and a failed
	// ping is always unhealthy.
	Critical bool
}

// NewChecker creates a health Checker with dependencies for performing checks.
//
// Args:
//
//	cfg: Server configuration (used to verify tools are configured and detect tool deps)
//	redisClient: Redis client (nil if Redis is not used)
//	db: PostgreSQL database connection (nil if PostgreSQL is not used)
//	deps: External dependency checks (nil if none). Use BuildDependencies() to auto-populate.
//
// Returns:
//
//	A Checker ready to run health checks
func NewChecker(cfg *config.Config, redisClient *redis.Client, db *sql.DB, deps []DependencyCheck) *Checker {
	return &Checker{
		cfg:          cfg,
		redisClient:  redisClient,
		db:           db,
		Dependencies: deps,
	}
}

// BuildDependencies scans the server config and environment to build the list of
// external dependencies that should be health-checked. It detects which tools are
// configured and extracts their required service URLs from environment variables.
//
// This is the preferred way to populate Checker.Dependencies — it ensures the
// health check only verifies services that are actually in use.
func BuildDependencies(cfg *config.Config) []DependencyCheck {
	if cfg == nil {
		return nil
	}

	deps := []DependencyCheck{}

	// redis/postgres are core: they are declared critical for documentation, but
	// RunAllChecks skips them and pings them through their clients (nil client =
	// degraded, failed ping = unhealthy → 503).
	if os.Getenv("REDIS_URL") != "" {
		deps = append(deps, DependencyCheck{Name: "redis", Critical: true})
	}
	// Only add postgres if DATABASE_URL is configured
	if os.Getenv("DATABASE_URL") != "" {
		deps = append(deps, DependencyCheck{Name: "postgres", Critical: true})
	}

	// Optional services behind individual tools: if one is down the tools that do
	// not need it still work, so the server is degraded, not unhealthy.
	for _, tool := range cfg.Tools {
		switch tool.Name {
		case "browser_scraper", "web_scraper":
			crawl4aiURL := osGetenv("CRAWL4AI_URL", "http://crawl4ai:8000")
			hostPort := extractHostPort(crawl4aiURL)
			deps = append(deps, DependencyCheck{
				Name:     "crawl4ai",
				URL:      hostPort,
				Tool:     "browser_scraper",
				Critical: false,
			})
		case "searxng_search":
			searxngURL := osGetenv("SEARXNG_URL", "http://searxng:8080")
			hostPort := extractHostPort(searxngURL)
			deps = append(deps, DependencyCheck{
				Name:     "searxng",
				URL:      hostPort,
				Tool:     "searxng_search",
				Critical: false,
			})
		case "rustfs_storage":
			rustfsEndpoint := osGetenv("RUSTFS_ENDPOINT", "rustfs:9000")
			deps = append(deps, DependencyCheck{
				Name:     "rustfs",
				URL:      rustfsEndpoint,
				Tool:     "rustfs_storage",
				Critical: false,
			})
		case "analyze_image":
			ollamaURL := osGetenv("LLM_API_URL", "http://localhost:11434")
			hostPort := extractHostPort(ollamaURL)
			deps = append(deps, DependencyCheck{
				Name:     "ollama",
				URL:      hostPort,
				Tool:     "analyze_image",
				Critical: false,
			})
		}
	}

	return deps
}

// extractHostPort parses a URL string (e.g., "http://crawl4ai:8000") and returns
// the host:port portion. Returns the original string if parsing fails.
func extractHostPort(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

// osGetenv reads an env var with a default fallback.
func osGetenv(key, defaultVal string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val
}

// RunAllChecks executes all configured health checks and returns their results.
// Checks run sequentially; a slow check doesn't affect others.
//
// Returns:
//
//	Slice of CheckResult, one per check. Order: redis, postgres, config, memory,
//	followed by dependency checks (crawl4ai, searxng, rustfs, ollama).
func (c *Checker) RunAllChecks(ctx context.Context) []CheckResult {
	// Build the list of checks, skipping redis if not configured
	checks := []struct {
		name string
		fn   func(ctx context.Context) CheckResult
	}{
		{"config", c.checkConfig},
		{"memory", c.checkMemory},
	}

	// Only add redis check if client is configured
	if c.redisClient != nil {
		checks = append(checks, struct {
			name string
			fn   func(ctx context.Context) CheckResult
		}{"redis", c.checkRedis})
	}

	// Only add postgres check if db is configured
	if c.db != nil {
		checks = append(checks, struct {
			name string
			fn   func(ctx context.Context) CheckResult
		}{"postgres", c.checkPostgres})
	}

	// Add dependency-specific checks
	for _, dep := range c.Dependencies {
		// Only add non-core deps (redis/postgres are already checked above via clients)
		if dep.Name == "redis" || dep.Name == "postgres" {
			continue
		}
		d := dep // capture loop variable
		checks = append(checks, struct {
			name string
			fn   func(ctx context.Context) CheckResult
		}{
			name: d.Name,
			fn:   func(ctx context.Context) CheckResult { return c.checkDependency(ctx, d) },
		})
	}

	results := make([]CheckResult, 0, len(checks))
	for _, check := range checks {
		result := check.fn(ctx)
		results = append(results, result)
	}

	return results
}

// GetOverallStatus determines the aggregate health status from individual check results.
// Uses worst-case logic: unhealthy > degraded > healthy.
//
// Args:
//
//	results: Slice of CheckResult from RunAllChecks
//
// Returns:
//
//	StatusUnhealthy if any check is unhealthy
//	StatusDegraded if any check is degraded (but none unhealthy)
//	StatusHealthy otherwise
func (c *Checker) GetOverallStatus(results []CheckResult) HealthStatus {
	hasUnhealthy := false
	hasDegraded := false

	for _, r := range results {
		switch r.Status {
		case StatusUnhealthy:
			hasUnhealthy = true
		case StatusDegraded:
			hasDegraded = true
		}
	}

	if hasUnhealthy {
		return StatusUnhealthy
	}
	if hasDegraded {
		return StatusDegraded
	}
	return StatusHealthy
}

// checkRedis validates Redis connectivity with a 2-second timeout.
// Returns StatusDegraded if client is nil, StatusUnhealthy if ping fails.
func (c *Checker) checkRedis(ctx context.Context) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:      "redis",
		Timestamp: start,
	}

	if c.redisClient == nil {
		result.Status = StatusDegraded
		result.Message = "Redis client not configured"
		result.Duration = time.Since(start)
		return result
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	err := c.redisClient.Ping(ctx).Err()
	if err != nil {
		result.Status = StatusUnhealthy
		result.Message = fmt.Sprintf("Redis ping failed: %v", err)
		log.Error().Err(err).Msg("Redis health check failed")
	} else {
		result.Status = StatusHealthy
		result.Message = "Redis connection successful"
	}

	result.Duration = time.Since(start)
	return result
}

// checkPostgres validates PostgreSQL connectivity with a 2-second timeout.
// Returns StatusDegraded if db is nil, StatusUnhealthy if ping fails.
func (c *Checker) checkPostgres(ctx context.Context) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:      "postgres",
		Timestamp: start,
	}

	if c.db == nil {
		result.Status = StatusDegraded
		result.Message = "PostgreSQL database not configured"
		result.Duration = time.Since(start)
		return result
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	err := c.db.PingContext(ctx)
	if err != nil {
		result.Status = StatusUnhealthy
		result.Message = fmt.Sprintf("PostgreSQL ping failed: %v", err)
		log.Error().Err(err).Msg("PostgreSQL health check failed")
	} else {
		result.Status = StatusHealthy
		result.Message = "PostgreSQL connection successful"
	}

	result.Duration = time.Since(start)
	return result
}

// checkConfig validates that configuration is present and tools are defined.
// StatusDegraded if no tools configured (server won't be useful but can start).
// StatusUnhealthy only if config itself is nil.
func (c *Checker) checkConfig(ctx context.Context) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:      "config",
		Timestamp: start,
	}

	if c.cfg == nil {
		result.Status = StatusUnhealthy
		result.Message = "Configuration not loaded"
	} else if len(c.cfg.Tools) == 0 {
		result.Status = StatusDegraded
		result.Message = "No tools configured"
	} else {
		result.Status = StatusHealthy
		result.Message = fmt.Sprintf("Configuration valid with %d tools", len(c.cfg.Tools))
	}

	result.Duration = time.Since(start)
	return result
}

// Memory check thresholds. They are expressed as a fraction of the detected
// memory budget instead of absolute megabytes: the same absolute number is
// meaningless on a 512 MB host and on a 2 GB container.
const (
	// memoryDegradedRatio is the budget fraction at which the check reports degraded.
	memoryDegradedRatio = 0.70
	// memoryCriticalRatio is the budget fraction at which the process is close
	// enough to an OOM kill to warrant an error log and an alert.
	memoryCriticalRatio = 0.90

	// Fallbacks used only when no budget can be detected (no cgroup, no
	// GOMEMLIMIT): the historical absolute thresholds.
	legacyDegradedBytes = 250 << 20
	legacyCriticalBytes = 500 << 20

	// cgroupUnlimited is the order of magnitude of the "no limit" sentinels:
	// cgroup v1 reports ~9.22e18 bytes and cgroup v2 reports the literal "max".
	cgroupUnlimited = int64(1) << 62
)

// memoryBudget returns the number of bytes the process is expected to live
// within, or 0 when it cannot be detected. Resolution order: the container's
// cgroup limit (the hard limit the kernel enforces), then the GOMEMLIMIT soft
// limit, then zero.
func memoryBudget() int64 {
	if limit := cgroupMemoryLimit(); limit > 0 {
		return limit
	}
	// debug.SetMemoryLimit(-1) reads the current soft limit without changing it.
	if limit := debug.SetMemoryLimit(-1); limit > 0 && limit < math.MaxInt64 {
		return limit
	}
	return 0
}

// cgroupMemoryLimit reads the memory limit of the cgroup this process runs in.
// It resolves the process's own cgroup v2 path first (a container reports "/",
// a systemd service reports its slice), then falls back to the well-known v2 and
// v1 paths. Returns 0 when there is no limit file or the limit is "unlimited".
func cgroupMemoryLimit() int64 {
	paths := []string{}
	if path := cgroupV2LimitPath(); path != "" {
		paths = append(paths, path)
	}
	paths = append(paths,
		"/sys/fs/cgroup/memory.max",                   // cgroup v2, container root
		"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
	)
	for _, path := range paths {
		if limit := readCgroupBytesFile(path); limit > 0 {
			return limit
		}
	}
	return 0
}

// cgroupV2LimitPath resolves the memory.max file for this process's own cgroup
// from /proc/self/cgroup (v2 line format: "0::/some/slice"). Returns "" when the
// host is not on the v2 unified hierarchy or the file cannot be resolved.
func cgroupV2LimitPath() string {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return resolveCgroupV2LimitPath(string(raw))
}

// resolveCgroupV2LimitPath is the pure part of cgroupV2LimitPath.
func resolveCgroupV2LimitPath(procSelfCgroup string) string {
	return resolveCgroupV2Path(procSelfCgroup, "memory.max")
}

// resolveCgroupV2Path maps the unified-hierarchy line of /proc/self/cgroup to a
// file inside this process's own cgroup: a container reports "/", a systemd
// service reports its slice. Returns "" when the host is not on the v2 unified
// hierarchy.
func resolveCgroupV2Path(procSelfCgroup, file string) string {
	for _, line := range strings.Split(procSelfCgroup, "\n") {
		rel, ok := strings.CutPrefix(strings.TrimSpace(line), "0::")
		if !ok {
			continue
		}
		return filepath.Join("/sys/fs/cgroup", rel, file)
	}
	return ""
}

// resolveCgroupV1Path maps the cgroup v1 memory-controller line of
// /proc/self/cgroup ("12:memory:/docker/abc", or a shared controller list like
// "4:cpu,memory:/x") to a file under the v1 mount point. Returns "" when the
// memory controller does not appear, which is the normal case on a v2 host.
func resolveCgroupV1Path(procSelfCgroup, file string) string {
	for _, line := range strings.Split(procSelfCgroup, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller != "memory" {
				continue
			}
			return filepath.Join("/sys/fs/cgroup/memory", parts[2], file)
		}
	}
	return ""
}

// readCgroupBytesFile parses one single-valued cgroup memory file (memory.max,
// memory.current, memory.limit_in_bytes, memory.usage_in_bytes) returning 0 for
// a missing file, an unparseable value or an "unlimited" sentinel. The sentinel
// guard is order-of-magnitude wide, so it only ever rejects nonsense: no real
// usage comes near 2^62 bytes.
func readCgroupBytesFile(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "max" {
		return 0
	}
	bytes, err := strconv.ParseInt(value, 10, 64)
	if err != nil || bytes <= 0 || bytes >= cgroupUnlimited {
		return 0
	}
	return bytes
}

// memoryUsage returns the bytes the kernel currently charges to this process's
// cgroup: memory.current on v2, memory.usage_in_bytes on v1. That is the number
// the kernel compares with memory.max before the OOM killer fires, and it
// includes what runtime.MemStats.HeapAlloc does not: runtime metadata, goroutine
// stacks outside the heap, cgo allocations and mmaps. Returns 0 when no usage
// file can be read, in which case callers fall back to the Go heap.
func memoryUsage() int64 {
	paths := make([]string, 0, 4)
	if raw, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		proc := string(raw)
		if path := resolveCgroupV2Path(proc, "memory.current"); path != "" {
			paths = append(paths, path)
		}
		if path := resolveCgroupV1Path(proc, "memory.usage_in_bytes"); path != "" {
			paths = append(paths, path)
		}
	}
	paths = append(paths,
		"/sys/fs/cgroup/memory.current",               // cgroup v2, container root
		"/sys/fs/cgroup/memory/memory.usage_in_bytes", // cgroup v1
	)
	for _, path := range paths {
		if used := readCgroupBytesFile(path); used > 0 {
			return used
		}
	}
	return 0
}

// memorySignal picks the number the memory check scores against the budget: the
// kernel's cgroup usage when it can be read, the Go heap otherwise. The source
// is reported in the check message and the log, so an operator can tell "the
// container is really close to memory.max" from "we only have the heap".
func memorySignal(heapAlloc, cgroupUsage int64) (bytes int64, source string) {
	if cgroupUsage > 0 {
		return cgroupUsage, "cgroup"
	}
	return heapAlloc, "heap"
}

// classifyMemory maps memory usage in bytes to a health status for a given
// budget. A budget of 0 falls back to the legacy absolute thresholds.
//
// Note the deliberate ceiling: memory pressure never yields StatusUnhealthy.
// /health answers 503 only when a check means "this process cannot serve", and
// the container HEALTHCHECK reads that as "replace me". Memory that grew during
// a burst, heap or not, can still serve; calling it unhealthy turned a smoke run
// into a flapping container. The alert for memory near its budget lives in the
// error log and in the memory_current_ratio metric, not in the HTTP status.
func classifyMemory(usedBytes, budget int64) (HealthStatus, string) {
	degradedLimit, criticalLimit := int64(legacyDegradedBytes), int64(legacyCriticalBytes)
	if budget > 0 {
		degradedLimit = int64(float64(budget) * memoryDegradedRatio)
		criticalLimit = int64(float64(budget) * memoryCriticalRatio)
	}

	switch {
	case usedBytes > criticalLimit:
		return StatusDegraded, " (memory near budget)"
	case usedBytes > degradedLimit:
		return StatusDegraded, " (elevated memory usage)"
	default:
		return StatusHealthy, ""
	}
}

// checkMemory monitors memory usage against the detected budget, preferring the
// kernel's own view of the process (cgroup memory.current / usage_in_bytes) and
// falling back to the Go heap when no cgroup usage file is readable.
// StatusHealthy: usage <= 70% of the budget
// StatusDegraded: usage > 70% of the budget (including > 90%, see classifyMemory)
func (c *Checker) checkMemory(ctx context.Context) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:      "memory",
		Timestamp: start,
	}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	heapAllocMB := float64(m.HeapAlloc) / 1024 / 1024
	sysMB := float64(m.Sys) / 1024 / 1024
	budget := memoryBudget()
	usedBytes, source := memorySignal(int64(m.HeapAlloc), memoryUsage())
	usedMB := float64(usedBytes) / 1024 / 1024

	if budget > 0 {
		result.Message = fmt.Sprintf("Memory: %.2f MB (%s), heap: %.2f MB, sys: %.2f MB (budget %.0f MB, %.1f%%)",
			usedMB, source, heapAllocMB, sysMB, float64(budget)/1024/1024,
			float64(usedBytes)/float64(budget)*100)
	} else {
		result.Message = fmt.Sprintf("Memory: %.2f MB (%s), heap: %.2f MB, sys: %.2f MB (budget unknown)",
			usedMB, source, heapAllocMB, sysMB)
	}

	status, suffix := classifyMemory(usedBytes, budget)
	result.Status = status
	result.Message += suffix

	switch suffix {
	case " (memory near budget)":
		log.Error().
			Str("source", source).
			Float64("memory_mb", usedMB).
			Float64("heap_mb", heapAllocMB).
			Float64("budget_mb", float64(budget)/1024/1024).
			Msg("Memory near its budget")
	case " (elevated memory usage)":
		log.Warn().
			Str("source", source).
			Float64("memory_mb", usedMB).
			Float64("heap_mb", heapAllocMB).
			Msg("Elevated memory usage detected")
	}

	result.Duration = time.Since(start)
	return result
}

// checkDependency verifies TCP connectivity to an external service.
// Uses a 2-second dial timeout. If the URL is empty, returns StatusDegraded.
//
// Args:
//
//	dep: the dependency to check (includes Name, URL, Tool, Critical)
//
// Returns:
//
//	CheckResult with reachability status
func (c *Checker) checkDependency(ctx context.Context, dep DependencyCheck) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:      dep.Name,
		Timestamp: start,
	}

	if dep.URL == "" {
		result.Status = StatusDegraded
		result.Message = fmt.Sprintf("%s URL not configured", dep.Name)
		result.Duration = time.Since(start)
		return result
	}

	// TCP dial with 2-second timeout
	d := net.Dialer{Timeout: 2 * time.Second}
	hostPort := extractHostPort(dep.URL)
	conn, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		result.Message = fmt.Sprintf("%s unreachable at %s: %v", dep.Name, dep.URL, err)
		if dep.Critical {
			result.Status = StatusUnhealthy
			log.Error().Err(err).Str("dependency", dep.Name).Str("url", dep.URL).Msg("Critical dependency health check failed")
		} else {
			// Optional service down: serve the tools that still work and keep the
			// container healthy (a 503 here would make Docker/autoheal declare the
			// whole orchestrator broken over a searxng/ollama blip).
			result.Status = StatusDegraded
			log.Warn().Err(err).Str("dependency", dep.Name).Str("url", dep.URL).Msg("Non-critical dependency health check failed")
		}
	} else {
		conn.Close()
		result.Status = StatusHealthy
		result.Message = fmt.Sprintf("%s reachable at %s", dep.Name, dep.URL)
	}

	result.Duration = time.Since(start)
	return result
}

// CheckDependencies returns a slice of DependencyStatus for all configured
// external dependencies. Used by /health/detailed to report per-service state.
func (c *Checker) CheckDependencies(ctx context.Context) []DependencyStatus {
	statuses := make([]DependencyStatus, 0, len(c.Dependencies))
	for _, dep := range c.Dependencies {
		dStatus := DependencyStatus{
			Name: dep.Name,
			URL:  dep.URL,
		}
		// redis and postgres are always reported even if not configured
		if dep.Name == "redis" || dep.Name == "postgres" {
			// These are checked via the dedicated methods; report as present
			dStatus.Reachable = true
			statuses = append(statuses, dStatus)
			continue
		}
		if dep.URL == "" {
			dStatus.Reachable = false
			dStatus.Error = "URL not configured"
		} else {
			d := net.Dialer{Timeout: 2 * time.Second}
			hostPort := extractHostPort(dep.URL)
			conn, err := d.DialContext(ctx, "tcp", hostPort)
			if err != nil {
				dStatus.Reachable = false
				dStatus.Error = err.Error()
			} else {
				conn.Close()
				dStatus.Reachable = true
			}
		}
		statuses = append(statuses, dStatus)
	}
	return statuses
}

// GetHealthMetrics returns current Go runtime metrics for monitoring.
// Includes heap memory, GC statistics, and goroutine count.
//
// Returns:
//
//	Map of metric name to value (in bytes for memory, ns for GC, count for others)
func GetHealthMetrics() map[string]float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	var gcPauseNs uint64
	if m.NumGC > 0 {
		gcPauseNs = m.PauseNs[(m.NumGC+255)%256]
	}

	// Memory budget and how much of it the process really occupies: the kernel's
	// cgroup usage when readable, the heap otherwise. Alert on the ratios, never
	// on the HTTP status (see classifyMemory).
	budget := float64(memoryBudget())
	current := float64(memoryUsage())
	usedRatio := 0.0
	if budget > 0 {
		usedRatio = float64(m.HeapAlloc) / budget
	}
	currentRatio := 0.0
	if budget > 0 && current > 0 {
		currentRatio = current / budget
	}

	return map[string]float64{
		"heap_alloc_bytes":     float64(m.HeapAlloc),
		"heap_sys_bytes":       float64(m.Sys),
		"heap_idle_bytes":      float64(m.HeapIdle),
		"heap_inuse_bytes":     float64(m.HeapInuse),
		"stack_inuse_bytes":    float64(m.StackInuse),
		"gc_pause_ns":          float64(gcPauseNs),
		"goroutines":           float64(runtime.NumGoroutine()),
		"num_gc":               float64(m.NumGC),
		"memory_budget_bytes":  budget,
		"memory_used_ratio":    usedRatio,
		"memory_current_bytes": current,
		"memory_current_ratio": currentRatio,
	}
}

var healthMetricDescs = map[string]*prometheus.Desc{
	"heap_alloc_bytes":     prometheus.NewDesc("mcp_health_heap_alloc_bytes", "Health metric for heap_alloc_bytes", nil, nil),
	"heap_sys_bytes":       prometheus.NewDesc("mcp_health_heap_sys_bytes", "Health metric for heap_sys_bytes", nil, nil),
	"heap_idle_bytes":      prometheus.NewDesc("mcp_health_heap_idle_bytes", "Health metric for heap_idle_bytes", nil, nil),
	"heap_inuse_bytes":     prometheus.NewDesc("mcp_health_heap_inuse_bytes", "Health metric for heap_inuse_bytes", nil, nil),
	"stack_inuse_bytes":    prometheus.NewDesc("mcp_health_stack_inuse_bytes", "Health metric for stack_inuse_bytes", nil, nil),
	"gc_pause_ns":          prometheus.NewDesc("mcp_health_gc_pause_ns", "Health metric for gc_pause_ns", nil, nil),
	"goroutines":           prometheus.NewDesc("mcp_health_goroutines", "Health metric for goroutines", nil, nil),
	"num_gc":               prometheus.NewDesc("mcp_health_num_gc", "Health metric for num_gc", nil, nil),
	"memory_budget_bytes":  prometheus.NewDesc("mcp_health_memory_budget_bytes", "Detected memory budget (cgroup limit or GOMEMLIMIT), 0 if unknown", nil, nil),
	"memory_used_ratio":    prometheus.NewDesc("mcp_health_memory_used_ratio", "HeapAlloc as a fraction of the memory budget, 0 if unknown", nil, nil),
	"memory_current_bytes": prometheus.NewDesc("mcp_health_memory_current_bytes", "Memory charged to the process cgroup (memory.current or memory.usage_in_bytes), 0 if unavailable", nil, nil),
	"memory_current_ratio": prometheus.NewDesc("mcp_health_memory_current_ratio", "Cgroup memory usage as a fraction of the memory budget, 0 if unknown", nil, nil),
}

// ExportMetrics converts health metrics to Prometheus metric format for scraping.
func (c *Checker) ExportMetrics() []prometheus.Metric {
	metrics := make([]prometheus.Metric, 0, len(healthMetricDescs))
	healthMetrics := GetHealthMetrics()
	for name, value := range healthMetrics {
		desc, ok := healthMetricDescs[name]
		if !ok {
			continue
		}
		metrics = append(metrics, prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value))
	}
	return metrics
}
