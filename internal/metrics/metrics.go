package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	RequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mcp_requests_total",
			Help: "Total number of MCP requests",
		},
		[]string{"method", "status"},
	)

	RequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mcp_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method"},
	)

	ToolExecutionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mcp_tool_executions_total",
			Help: "Total number of tool executions",
		},
		[]string{"tool_name", "status"},
	)

	ToolExecutionDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mcp_tool_execution_duration_seconds",
			Help:    "Tool execution duration in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"tool_name"},
	)

	ActiveConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcp_active_connections",
			Help: "Number of active connections",
		},
	)

	RateLimitHits = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "mcp_rate_limit_hits_total",
			Help: "Total number of rate limit hits",
		},
	)

	RedisCacheHits = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "mcp_redis_cache_hits_total",
			Help: "Total number of Redis cache hits",
		},
	)

	RedisCacheMisses = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "mcp_redis_cache_misses_total",
			Help: "Total number of Redis cache misses",
		},
	)

	PostgresConnectionsActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcp_postgres_connections_active",
			Help: "Number of active PostgreSQL connections",
		},
	)

	PostgresConnectionsIdle = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcp_postgres_connections_idle",
			Help: "Number of idle PostgreSQL connections",
		},
	)

	PostgresConnectionsWaitCount = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "mcp_postgres_connections_wait_total",
			Help: "Total number of times a connection had to wait",
		},
	)

	LLMRequestTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mcp_llm_requests_total",
			Help: "Total number of LLM requests",
		},
		[]string{"provider", "status"},
	)

	LLMRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mcp_llm_request_duration_seconds",
			Help:    "LLM request duration in seconds",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		},
		[]string{"provider"},
	)
)

func RecordToolExecution(toolName string, success bool, durationSeconds float64) {
	status := "success"
	if !success {
		status = "error"
	}
	ToolExecutionTotal.WithLabelValues(toolName, status).Inc()
	ToolExecutionDuration.WithLabelValues(toolName).Observe(durationSeconds)
}

func RecordRequest(method string, status string, durationSeconds float64) {
	RequestsTotal.WithLabelValues(method, status).Inc()
	RequestDuration.WithLabelValues(method).Observe(durationSeconds)
}

func RecordLLMRequest(provider string, success bool, durationSeconds float64) {
	status := "success"
	if !success {
		status = "error"
	}
	LLMRequestTotal.WithLabelValues(provider, status).Inc()
	LLMRequestDuration.WithLabelValues(provider).Observe(durationSeconds)
}
