package health

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/amphora/mcp-go/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

type HealthStatus string

const (
	StatusHealthy   HealthStatus = "healthy"
	StatusDegraded  HealthStatus = "degraded"
	StatusUnhealthy HealthStatus = "unhealthy"
)

type CheckResult struct {
	Name      string        `json:"name"`
	Status    HealthStatus  `json:"status"`
	Message   string        `json:"message,omitempty"`
	Duration  time.Duration `json:"duration_ms"`
	Timestamp time.Time     `json:"timestamp"`
}

type Checker struct {
	cfg         *config.Config
	redisClient *redis.Client
	db          *sql.DB
	httpClient  *http.Client
}

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
	} else {
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			result.Status = StatusDegraded
			result.Message = fmt.Sprintf("LLM endpoint returned status %d", resp.StatusCode)
		} else {
			result.Status = StatusHealthy
			result.Message = "LLM endpoint reachable"
		}
	}

	result.Duration = time.Since(start)
	return result
}

func GetHealthMetrics() map[string]float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return map[string]float64{
		"heap_alloc_bytes":  float64(m.HeapAlloc),
		"heap_sys_bytes":    float64(m.Sys),
		"heap_idle_bytes":   float64(m.HeapIdle),
		"heap_inuse_bytes":  float64(m.HeapInuse),
		"stack_inuse_bytes": float64(m.StackInuse),
		"gc_pause_ns":       float64(m.PauseNs[(m.NumGC+255)%256]),
		"goroutines":        float64(runtime.NumGoroutine()),
	}
}

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
