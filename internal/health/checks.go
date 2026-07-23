package health

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"runtime"
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
	Name     string // Human-readable name (e.g., "searxng")
	URL      string // TCP address (host:port) extracted from env/config
	Tool     string // Associated tool name (e.g., "searxng_search"), for mapping
	Critical bool   // If true, /health returns 503 when unreachable
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

	// Only add redis if REDIS_URL is configured
	if os.Getenv("REDIS_URL") != "" {
		deps = append(deps, DependencyCheck{Name: "redis", Critical: false})
	}
	// Only add postgres if DATABASE_URL is configured
	if os.Getenv("DATABASE_URL") != "" {
		deps = append(deps, DependencyCheck{Name: "postgres", Critical: false})
	}

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

// checkMemory monitors Go runtime memory usage.
// StatusHealthy: heap < 250MB
// StatusDegraded: heap 250-500MB
// StatusUnhealthy: heap > 500MB
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

	result.Message = fmt.Sprintf("Heap: %.2f MB, Sys: %.2f MB", heapAllocMB, sysMB)

	if heapAllocMB > 500 {
		result.Status = StatusUnhealthy
		result.Message += " (high memory usage)"
		log.Warn().Float64("heap_mb", heapAllocMB).Msg("High memory usage detected")
	} else if heapAllocMB > 250 {
		result.Status = StatusDegraded
		result.Message += " (elevated memory usage)"
	} else {
		result.Status = StatusHealthy
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
	conn, err := d.DialContext(ctx, "tcp", dep.URL)
	if err != nil {
		result.Status = StatusUnhealthy
		result.Message = fmt.Sprintf("%s unreachable at %s: %v", dep.Name, dep.URL, err)
		log.Error().Err(err).Str("dependency", dep.Name).Str("url", dep.URL).Msg("Dependency health check failed")
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
			conn, err := d.DialContext(ctx, "tcp", dep.URL)
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

	return map[string]float64{
		"heap_alloc_bytes":  float64(m.HeapAlloc),
		"heap_sys_bytes":    float64(m.Sys),
		"heap_idle_bytes":   float64(m.HeapIdle),
		"heap_inuse_bytes":  float64(m.HeapInuse),
		"stack_inuse_bytes": float64(m.StackInuse),
		"gc_pause_ns":       float64(gcPauseNs),
		"goroutines":        float64(runtime.NumGoroutine()),
		"num_gc":            float64(m.NumGC),
	}
}

var healthMetricDescs = map[string]*prometheus.Desc{
	"heap_alloc_bytes":  prometheus.NewDesc("mcp_health_heap_alloc_bytes", "Health metric for heap_alloc_bytes", nil, nil),
	"heap_sys_bytes":    prometheus.NewDesc("mcp_health_heap_sys_bytes", "Health metric for heap_sys_bytes", nil, nil),
	"heap_idle_bytes":   prometheus.NewDesc("mcp_health_heap_idle_bytes", "Health metric for heap_idle_bytes", nil, nil),
	"heap_inuse_bytes":  prometheus.NewDesc("mcp_health_heap_inuse_bytes", "Health metric for heap_inuse_bytes", nil, nil),
	"stack_inuse_bytes": prometheus.NewDesc("mcp_health_stack_inuse_bytes", "Health metric for stack_inuse_bytes", nil, nil),
	"gc_pause_ns":       prometheus.NewDesc("mcp_health_gc_pause_ns", "Health metric for gc_pause_ns", nil, nil),
	"goroutines":        prometheus.NewDesc("mcp_health_goroutines", "Health metric for goroutines", nil, nil),
	"num_gc":            prometheus.NewDesc("mcp_health_num_gc", "Health metric for num_gc", nil, nil),
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
