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

	checker := NewChecker(nil, nil, nil)
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

	checker := NewChecker(nil, nil, nil)
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

	checker := NewChecker(nil, nil, nil)
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

	checker := NewChecker(nil, nil, nil)
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
	checker := NewChecker(nil, nil, nil)
	result := checker.checkConfig(context.Background())

	if result.Status != StatusUnhealthy {
		t.Errorf("expected %s, got %s", StatusUnhealthy, result.Status)
	}
	if result.Name != "config" {
		t.Error("name mismatch")
	}
}

func TestChecker_CheckMemory(t *testing.T) {
	checker := NewChecker(nil, nil, nil)
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

func TestChecker_CheckToolPath_EmptyCommand(t *testing.T) {
	checker := NewChecker(nil, nil, nil)
	toolCfg := config.ToolConfig{
		Name:        "test_tool",
		Command:     "",
		Description: "test",
	}
	result := checker.checkToolPath(context.Background(), toolCfg)

	if result.Status != StatusDegraded {
		t.Errorf("expected %s, got %s", StatusDegraded, result.Status)
	}
}

func TestChecker_CheckToolPath_ValidCommand(t *testing.T) {
	checker := NewChecker(nil, nil, nil)
	toolCfg := config.ToolConfig{
		Name:        "test_tool",
		Command:     "python3",
		Description: "test",
	}
	result := checker.checkToolPath(context.Background(), toolCfg)

	if result.Status != StatusHealthy {
		t.Errorf("expected %s, got %s", StatusHealthy, result.Status)
	}
}
