package executor

import (
	"context"
	"testing"
	"time"

	"github.com/sudebaker/mcp-go/internal/config"
	"github.com/sudebaker/mcp-go/internal/tracing"
)

func TestNewProcessPool_DefaultMax(t *testing.T) {
	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 5
	pool := NewProcessPool(cfg, tracing.NoOpTracer(), nil, 0)
	defer pool.Close()

	if pool.maxPerTool != 5 {
		t.Errorf("expected maxPerTool 5, got %d", pool.maxPerTool)
	}
}

func TestNewProcessPool_CustomMax(t *testing.T) {
	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 10
	pool := NewProcessPool(cfg, tracing.NoOpTracer(), nil, 3)
	defer pool.Close()

	if pool.maxPerTool != 3 {
		t.Errorf("expected maxPerTool 3, got %d", pool.maxPerTool)
	}
}

func TestProcessPool_ExecuteNonexistentTool(t *testing.T) {
	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 5
	pool := NewProcessPool(cfg, tracing.NoOpTracer(), nil, 2)
	defer pool.Close()

	_, err := pool.Execute(context.Background(), "nonexistent", map[string]interface{}{})
	if err == nil {
		t.Error("expected error for nonexistent tool")
	}
}

func TestProcessPool_AcquireSlot(t *testing.T) {
	// This test verifies the pool can acquire slots and respects limits
	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 10

	// Register an echo tool that reads a line and writes back
	cfg.Tools = []config.ToolConfig{
		{
			Name:    "test_pool_tool",
			Command: "echo",
			Args:    []string{`{"success":true,"request_id":"test"}`},
		},
	}

	pool := NewProcessPool(cfg, tracing.NoOpTracer(), nil, 2)
	defer pool.Close()

	slot, err := pool.tryCreate(context.Background(), "test_pool_tool")
	if err != nil {
		t.Fatalf("failed to create slot: %v", err)
	}
	if slot == nil {
		t.Fatal("expected non-nil slot")
	}
	if !slot.inUse {
		t.Error("expected slot to be marked in use")
	}

	// Release the slot
	pool.mu.Lock()
	slot.inUse = false
	pool.mu.Unlock()

	// Acquire again via idle
	slot2 := pool.tryAcquireIdle("test_pool_tool")
	if slot2 == nil {
		t.Fatal("expected to acquire idle slot")
	}
	if slot2 != slot {
		t.Error("expected same slot")
	}

	pool.Close()
}

func TestProcessPool_Limit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 10
	cfg.Tools = []config.ToolConfig{
		{
			Name:    "test_limit",
			Command: "echo",
			Args:    []string{"test"},
		},
	}

	pool := NewProcessPool(cfg, tracing.NoOpTracer(), nil, 2)
	defer pool.Close()

	// Create 2 slots (max)
	s1, err := pool.tryCreate(context.Background(), "test_limit")
	if err != nil {
		t.Fatalf("slot 1: %v", err)
	}
	s2, err := pool.tryCreate(context.Background(), "test_limit")
	if err != nil {
		t.Fatalf("slot 2: %v", err)
	}

	// Third should hit pool full
	_, err = pool.tryCreate(context.Background(), "test_limit")
	if err == nil {
		t.Error("expected pool full error")
	}

	// Release both, mark not in use
	s1.inUse = false
	s2.inUse = false

	// Now acquire idle should work
	s := pool.tryAcquireIdle("test_limit")
	if s == nil {
		t.Error("expected to acquire idle slot after release")
	}
}

func TestProcessPool_ReapIdle(t *testing.T) {
	cfg := &config.Config{}
	cfg.Execution.MaxConcurrency = 10
	cfg.Tools = []config.ToolConfig{
		{
			Name:    "test_reap",
			Command: "echo",
			Args:    []string{"reap"},
		},
	}

	pool := NewProcessPool(cfg, tracing.NoOpTracer(), nil, 2)
	defer pool.Close()

	// Create a slot
	slot, err := pool.tryCreate(context.Background(), "test_reap")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Set lastUsed far in the past
	pool.mu.Lock()
	slot.inUse = false
	slot.lastUsed = time.Now().Add(-10 * time.Minute)
	pool.mu.Unlock()

	// Reap
	pool.reapIdle()

	// Slot should be gone
	pool.mu.Lock()
	slots := pool.pool["test_reap"]
	pool.mu.Unlock()
	if len(slots) != 0 {
		t.Errorf("expected 0 slots after reap, got %d", len(slots))
	}
}
