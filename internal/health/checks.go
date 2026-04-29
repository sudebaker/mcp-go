package health

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
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
	StatusDegraded  HealthStatus = "degraded"   // Partial functionality, warning
	StatusUnhealthy HealthStatus = "unhealthy"  // Critical failure
)

// CheckResult represents a single health check outcome with timing information.
type CheckResult struct {
	Name      string        `json:"name"`               // Name of the checked component
	Status    HealthStatus  `json:"status"`            // Health state
	Message   string        `json:"message,omitempty"` // Human-readable details
	Duration  time.Duration `json:"duration_ms"`       // Check execution time in ms
	Timestamp time.Time     `json:"timestamp"`         // When the check ran
}

// Checker performs health checks against external dependencies and system resources.
// It validates connectivity to Redis, PostgreSQL, checks memory usage, and verifies
// configuration integrity. Results are used for monitoring and alerting.
type Checker struct {
	cfg         *config.Config      // Server configuration for tool validation
	redisClient *redis.Client        // Redis connection for ping check
	db          *sql.DB              // PostgreSQL connection for ping check
	httpClient  *http.Client         // HTTP client for LLM endpoint checks
}

// NewChecker creates a health Checker with dependencies for performing checks.
//
// Args:
//   cfg: Server configuration (used to verify tools are configured)
//   redisClient: Redis client (nil if Redis is not used)
//   db: PostgreSQL database connection (nil if PostgreSQL is not used)
//
// Returns:
//   A Checker ready to run health checks
func NewChecker(cfg *config.Config, redisClient *redis.Client, db *sql.DB) *Checker {
	return &Checker{
		cfg:         cfg,
		redisClient: redisClient,
		db:          db,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// RunAllChecks executes all configured health checks and returns their results.
// Checks run sequentially; a slow check doesn't affect others.
//
// Returns:
//   Slice of CheckResult, one per check. Order: redis, postgres, config, memory
func (c *Checker) RunAllChecks(ctx context.Context) []CheckResult {
	checks := []struct {
		name string
		fn   func(ctx context.Context) CheckResult
	}{
		{"redis", c.checkRedis},
		{"postgres", c.checkPostgres},
		{"config", c.checkConfig},
		{"memory", c.checkMemory},
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
//   results: Slice of CheckResult from RunAllChecks
//
// Returns:
//   StatusUnhealthy if any check is unhealthy
//   StatusDegraded if any check is degraded (but none unhealthy)
//   StatusHealthy otherwise
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

// checkToolPaths validates configuration of all registered tools.
func (c *Checker) checkToolPaths(ctx context.Context) []CheckResult {
	if c.cfg == nil {
		return nil
	}

	var results []CheckResult
	for _, tool := range c.cfg.Tools {
		result := c.checkToolPath(ctx, tool)
		results = append(results, result)
	}
	return results
}

// checkToolPath validates a single tool's configuration.
func (c *Checker) checkToolPath(ctx context.Context, tool config.ToolConfig) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:      fmt.Sprintf("tool_path:%s", tool.Name),
		Timestamp: start,
	}

	if tool.Command == "" {
		result.Status = StatusDegraded
		result.Message = "Tool command not configured"
		result.Duration = time.Since(start)
		return result
	}

	result.Status = StatusHealthy
	result.Message = "Tool path configuration valid"
	result.Duration = time.Since(start)
	return result
}

// checkLLMEndpoint verifies LLM API endpoint is reachable via HTTP GET.
// Returns StatusHealthy if endpoint is empty (optional), StatusDegraded on failure.
func (c *Checker) checkLLMEndpoint(ctx context.Context, endpoint string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:      fmt.Sprintf("llm:%s", endpoint),
		Timestamp: start,
	}

	if endpoint == "" {
		result.Status = StatusHealthy
		result.Message = "No LLM endpoint configured (optional)"
		result.Duration = time.Since(start)
		return result
	}

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		result.Status = StatusDegraded
		result.Message = fmt.Sprintf("Failed to create request: %v", err)
		result.Duration = time.Since(start)
		return result
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		result.Status = StatusDegraded
		result.Message = fmt.Sprintf("LLM endpoint unreachable: %v", err)
		log.Warn().Err(err).Str("endpoint", endpoint).Msg("LLM health check failed")
		result.Duration = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		result.Status = StatusDegraded
		result.Message = fmt.Sprintf("LLM endpoint returned status %d", resp.StatusCode)
	} else {
		result.Status = StatusHealthy
		result.Message = "LLM endpoint reachable"
	}

	result.Duration = time.Since(start)
	return result
}

// GetHealthMetrics returns current Go runtime metrics for monitoring.
// Includes heap memory, GC statistics, and goroutine count.
//
// Returns:
//   Map of metric name to value (in bytes for memory, ns for GC, count for others)
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

// ExportMetrics converts health metrics to Prometheus metric format for scraping.
func (c *Checker) ExportMetrics() []prometheus.Metric {
	metrics := make([]prometheus.Metric, 0)

	healthMetrics := GetHealthMetrics()
	for name, value := range healthMetrics {
		metrics = append(metrics, prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				fmt.Sprintf("mcp_health_%s", name),
				fmt.Sprintf("Health metric for %s", name),
				nil, nil,
			),
			prometheus.GaugeValue,
			value,
		))
	}

	return metrics
}
