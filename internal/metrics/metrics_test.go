package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMetricsRegistered(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		RequestsTotal,
		RequestDuration,
		ToolExecutionTotal,
		ToolExecutionDuration,
		ActiveConnections,
		RateLimitHits,
		RedisCacheHits,
		RedisCacheMisses,
		PostgresConnectionsActive,
		PostgresConnectionsIdle,
		PostgresConnectionsWaitCount,
		LLMRequestTotal,
		LLMRequestDuration,
	)

	rec, err := registry.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	if len(rec) == 0 {
		t.Error("No metrics were registered")
	}
}

func TestRecordToolExecution(t *testing.T) {
	toolName := "test_tool"
	metric := ToolExecutionTotal.WithLabelValues(toolName, "success")
	metric.(prometheus.Counter).Add(100)

	before := getCounterValue(metric)

	RecordToolExecution(toolName, true, 0.5)

	after := getCounterValue(metric)
	if after <= before {
		t.Error("Tool execution was not recorded")
	}
}

func TestRecordToolExecutionError(t *testing.T) {
	toolName := "test_tool_error"
	metric := ToolExecutionTotal.WithLabelValues(toolName, "error")
	metric.(prometheus.Counter).Add(100)

	before := getCounterValue(metric)

	RecordToolExecution(toolName, false, 0.5)

	after := getCounterValue(metric)
	if after <= before {
		t.Error("Tool execution error was not recorded")
	}
}

func TestRecordRequest(t *testing.T) {
	method := "test_method"
	metric := RequestsTotal.WithLabelValues(method, "200")
	metric.(prometheus.Counter).Add(100)

	before := getCounterValue(metric)

	RecordRequest(method, "200", 0.1)

	after := getCounterValue(metric)
	if after <= before {
		t.Error("Request was not recorded")
	}
}

func TestRecordLLMRequest(t *testing.T) {
	provider := "test_provider"
	metric := LLMRequestTotal.WithLabelValues(provider, "success")
	metric.(prometheus.Counter).Add(100)

	before := getCounterValue(metric)

	RecordLLMRequest(provider, true, 1.0)

	after := getCounterValue(metric)
	if after <= before {
		t.Error("LLM request was not recorded")
	}
}

func TestActiveConnectionsGauge(t *testing.T) {
	before := getGaugeValue(ActiveConnections)
	ActiveConnections.Inc()
	after := getGaugeValue(ActiveConnections)
	if after <= before {
		t.Error("Active connections gauge did not increment")
	}
}

func TestRateLimitHitsCounter(t *testing.T) {
	metric := RateLimitHits
	metric.(prometheus.Counter).Add(100)

	before := getCounterValue(metric)

	RateLimitHits.Inc()

	after := getCounterValue(metric)
	if after <= before {
		t.Error("Rate limit hits counter did not increment")
	}
}

func getCounterValue(metric prometheus.Metric) float64 {
	pb := &dto.Metric{}
	metric.Write(pb)
	return pb.GetCounter().GetValue()
}

func getGaugeValue(metric prometheus.Metric) float64 {
	pb := &dto.Metric{}
	metric.Write(pb)
	return pb.GetGauge().GetValue()
}
