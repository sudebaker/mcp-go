# 📋 PRODUCTION CHECKLIST - MCP-Go Orchestrator

**Quick Reference Guide para llevar el sistema a Producción**

---

## 🚀 QUICK START (Primero esto)

```bash
# 1. Leer el roadmap
cat ROADMAP.md | head -100

# 2. Verificar estado actual
go test ./...
docker-compose -f deployments/docker-compose.yml ps

# 3. Ejecutar suite de tests
./tests/test_excel_analysis.sh
./tests/test_logging.sh

# 4. Ver status de services
curl http://localhost:8080/health
curl http://localhost:8080/metrics 2>/dev/null | head -20
```

---

## 🔴 FASE 1: FIXES CRÍTICOS (4-5 horas) - ¡HAZLO AHORA!

### C1: Fix Rate Limiter Memory Leak
```bash
# Archivo: internal/transport/ratelimit.go
# Estado: NOT DONE
# Impact: Memory leak, crashes on restart
# Fix Time: 30 min

# Verificar problema actual:
grep -n "cleanup()" internal/transport/ratelimit.go
grep -n "Stop()" internal/transport/ratelimit.go

# Implementar fix:
# 1. Agregar Stop() method a RateLimiter struct
# 2. Llamar limiter.Stop() en cmd/server/main.go shutdown
# 3. Test: docker-compose restart mcp-server 10 veces, check memory

TODO_C1="
func (rl *RateLimiter) Stop() {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    if !rl.stopped {
        close(rl.stopCh)
        rl.stopped = true
    }
}
"
```

### C2: Expose Prometheus Metrics Endpoint
```bash
# Archivo: cmd/server/main.go
# Estado: NOT DONE
# Impact: No metrics visibility
# Fix Time: 30 min

# Verificar estado actual:
curl http://localhost:8080/metrics 2>&1 | head -5
# Debería devolver 404

# Implementar fix:
# 1. Import: github.com/prometheus/client_golang/prometheus/promhttp
# 2. En el router HTTP: http.Handle("/metrics", promhttp.Handler())
# 3. Test: curl http://localhost:8080/metrics (debe devolver 200)

TODO_C2="
import 'github.com/prometheus/client_golang/prometheus/promhttp'

// In setupHTTPServer():
http.Handle('/metrics', promhttp.Handler())
"

# Verificar después:
docker-compose -f deployments/docker-compose.yml restart mcp-server
sleep 3
curl -s http://localhost:8080/metrics | grep "# HELP" | head -5
```

### C3: Add JSON Schema Validation
```bash
# Archivo: internal/executor/subprocess.go
# Estado: NOT DONE
# Impact: Accept invalid arguments, unpredictable behavior
# Fix Time: 1-2 hours

# Verificar estado actual:
grep -n "validateSchema\|jsonschema" internal/executor/subprocess.go
# Debería devolver nada

# Implementar fix:
# 1. Create validateInputArguments() function
# 2. Validate args against toolCfg.InputSchema before execution
# 3. Test: Pass invalid arg types, verify error

TODO_C3="
func validateInputArguments(schema map[string]interface{}, args map[string]interface{}) error {
    // Check required fields
    // Check types match schema
    // Check field constraints
    return nil
}
"

# Test cases to add:
# - Missing required field
# - Wrong type (string vs number)
# - Invalid enum value
# - Missing object properties
```

### C4: Add Integration Tests
```bash
# Archivo: tests/integration_test.go (nuevo)
# Estado: NOT DONE
# Impact: No end-to-end testing
# Fix Time: 2-3 hours

# Crear archivo de tests:
cat > tests/integration_test.go << 'EOF'
package main

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestServerStartupShutdown(t *testing.T) {
	// Test server can start and shutdown gracefully
}

func TestHealthEndpoint(t *testing.T) {
	// Test /health returns 200 OK
	resp, err := http.Get("http://localhost:8080/health")
	if err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	// Test /metrics returns Prometheus metrics
	resp, err := http.Get("http://localhost:8080/metrics")
	if err != nil {
		t.Fatalf("Metrics endpoint failed: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}
}

func TestToolExecution(t *testing.T) {
	// Test actual tool execution end-to-end
}

func TestRateLimiting(t *testing.T) {
	// Test rate limiting per IP works correctly
}

func TestGracefulShutdown(t *testing.T) {
	// Test in-flight requests complete before shutdown
}
EOF

# Ejecutar tests:
go test -v ./tests/integration_test.go
```

---

## ✅ FASE 1 COMPLETION CHECKLIST

```bash
# Después de completar los 4 fixes:

# 1. Build and test
go build -o bin/mcp-server ./cmd/server
go test ./... -v

# 2. Verify fixes
grep -n "Stop()" internal/transport/ratelimit.go       # C1 ✅
grep -n "/metrics" cmd/server/main.go                  # C2 ✅
grep -n "validateInputArguments" internal/executor/subprocess.go  # C3 ✅
test -f tests/integration_test.go                      # C4 ✅

# 3. Docker deployment test
cd deployments && docker-compose up -d
sleep 5
curl http://localhost:8080/health
curl http://localhost:8080/metrics | head -10
docker-compose ps  # All healthy?

# 4. Run all test suites
cd .. && ./tests/test_excel_analysis.sh
./tests/test_logging.sh
go test ./tests/... -v

# 5. Check memory stability (10 min test)
for i in {1..5}; do
  docker stats --no-stream mcp-orchestrator | grep -oP '\d+\.\d+M' | head -1
  docker-compose restart mcp-server
  sleep 10
done
# Memory should be stable, not growing

echo "✅ FASE 1 COMPLETE - Ready for Staging"
```

---

## 🟠 FASE 2: HIGH PRIORITY IMPROVEMENTS (5-7 hours)

### H1: OpenTelemetry Tracing (Optional but recommended)
```bash
# Status: RECOMMENDED
# Impact: Production debugging, distributed tracing
# Time: 3-4 hours

# Decision: Use Jaeger (open source) or DataDog
# Recommendation: Jaeger for air-gap

# Add to go.mod:
go get github.com/open-telemetry/opentelemetry-go
go get github.com/open-telemetry/opentelemetry-exporter-jaeger

# Files to modify:
# - cmd/server/main.go: Initialize tracer
# - internal/executor/subprocess.go: Add spans
# - internal/transport/sse.go: Add request tracing
```

### H2: Health Checks Detailed Endpoint
```bash
# Status: IN PROGRESS
# Impact: Better monitoring and debugging
# Time: 1 hour

# Add endpoint: GET /health/detailed
# Response includes: database, llm_service, disk_space, memory

cat > /tmp/health_detailed.go << 'EOF'
func healthDetailed(w http.ResponseWriter, r *http.Request) {
    response := map[string]interface{}{
        "status": "healthy",
        "timestamp": time.Now().Format(time.RFC3339),
        "components": map[string]string{
            "database": checkDatabase(),
            "llm_service": checkLLM(),
            "disk_space": checkDisk(),
            "memory": checkMemory(),
        },
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}
EOF

# Test:
curl http://localhost:8080/health/detailed | jq .
```

### H3: Stricter Config Validation
```bash
# Status: NOT DONE
# Impact: Catch configuration errors early
# Time: 1-2 hours

# Add validation in internal/config/config.go:
# - Required fields check
# - Port ranges validation
# - Timeout value validation
# - File path permissions check

grep -A 10 "func (c *Config) Validate()" internal/config/config.go
# Should validate all required fields
```

### H4: Docker Resource Limits
```bash
# Status: NOT DONE
# Impact: Prevent resource exhaustion
# Time: 30 min

# Edit: deployments/docker-compose.yml
# Add to mcp-server service:
deploy:
  resources:
    limits:
      cpus: '2'
      memory: 4G
    reservations:
      cpus: '1'
      memory: 2G

# Test:
docker-compose -f deployments/docker-compose.yml up -d
docker stats mcp-orchestrator  # Verify limits
```

---

## 🟡 FASE 3: PRODUCTION HARDENING (11-16 hours)

### P1: Production Deployment Guide
```bash
# Create: docs/DEPLOYMENT_PRODUCTION.md
# Content:
# - Pre-deployment checklist
# - Environment variables setup
# - SSL/TLS configuration (nginx)
# - Database backup strategy
# - Monitoring & alerting
# - Rollback procedures
# - Capacity planning

# Time: 2-3 hours (mostly documentation)
```

### P2: Security Hardening
```bash
# Tasks:
# ☐ Enable HTTPS with reverse proxy (nginx)
# ☐ Add API key authentication
# ☐ Enable audit logging
# ☐ Configure secret management
# ☐ Set up network policies
# ☐ Enable CORS restrictions

# Time: 4-6 hours
```

### P3: Monitoring & Alerting
```bash
# Setup:
# - Prometheus scrape config
# - Grafana dashboards
# - Alert rules:
#   - Memory > 80%
#   - Error rate > 5%
#   - Tool timeout > 10%
#   - Health check failures

# Time: 3-4 hours
```

### P4: Disaster Recovery
```bash
# Tasks:
# ☐ PostgreSQL backup strategy
# ☐ Hot standby setup
# ☐ RTO/RPO definition
# ☐ Recovery procedures
# ☐ Data encryption at rest
# ☐ Test recovery procedures

# Time: 2-3 hours
```

---

## 📊 PRE-PRODUCTION FINAL CHECKLIST

```bash
#!/bin/bash

echo "🔍 PRE-PRODUCTION VERIFICATION"
echo "================================"

# Code Quality
echo -e "\n✅ Code Quality Checks:"
go test ./... && echo "  ✓ All tests passing"
go fmt ./... && echo "  ✓ Code formatted"
go vet ./... && echo "  ✓ Vet checks pass"
go test -cover ./... | grep "coverage" && echo "  ✓ Coverage > 70%"

# Deployment
echo -e "\n✅ Deployment Checks:"
docker-compose -f deployments/docker-compose.yml ps | grep healthy && echo "  ✓ All services healthy"
curl -s http://localhost:8080/health | jq . && echo "  ✓ Health endpoint OK"
curl -s http://localhost:8080/metrics | head -5 && echo "  ✓ Metrics endpoint OK"

# Testing
echo -e "\n✅ Integration Tests:"
./tests/test_excel_analysis.sh && echo "  ✓ Excel analysis passing"
./tests/test_logging.sh && echo "  ✓ Logging tests passing"
go test -run Integration ./tests && echo "  ✓ Integration tests passing"

# Security
echo -e "\n✅ Security Checks:"
grep -r "hardcoded" . --include="*.go" || echo "  ✓ No hardcoded secrets"
grep -r "DATABASE_URL" internal --include="*.go" | grep -v "= os.Getenv" || echo "  ✓ DB connection from env vars"
test -f deployments/.env.example && echo "  ✓ .env.example exists"

# Documentation
echo -e "\n✅ Documentation Checks:"
test -f ROADMAP.md && echo "  ✓ ROADMAP.md exists"
test -f PRODUCTION_CHECKLIST.md && echo "  ✓ PRODUCTION_CHECKLIST.md exists"
test -f docs/DEPLOYMENT_PRODUCTION.md && echo "  ✓ DEPLOYMENT_PRODUCTION.md exists"

echo -e "\n🎉 All checks complete!"
```

---

## 🚨 QUICK TROUBLESHOOTING

### Memory keeps growing
```bash
# Check for goroutine leaks:
curl -s http://localhost:8080/debug/pprof/goroutine | grep goroutine | head -5

# Restart and monitor:
docker-compose restart mcp-server
watch 'docker stats --no-stream mcp-orchestrator | tail -2'

# If still growing: Check C1 (rate limiter) fix
```

### Metrics endpoint 404
```bash
# Verify fix C2:
grep -n "promhttp.Handler()" cmd/server/main.go

# Restart:
docker-compose restart mcp-server

# Test:
curl -v http://localhost:8080/metrics 2>&1 | grep "< HTTP"
# Should return 200
```

### Tools failing with validation error
```bash
# Check fix C3:
grep -n "validateInputArguments" internal/executor/subprocess.go

# Check tool config schema:
cat configs/config.yaml | grep -A 10 "input_schema"

# Enable debug logging:
export LOG_LEVEL=debug
docker-compose up mcp-server
```

### Integration tests failing
```bash
# Make sure tests/integration_test.go exists:
ls -l tests/integration_test.go

# Run with verbose output:
go test -v ./tests/integration_test.go

# Check server is running:
docker-compose ps | grep mcp-orchestrator
```

---

## 📞 ESCALATION PATH

If something breaks in production:

1. **Immediate** (< 5 min):
   - Check `/health` endpoint
   - Check docker logs: `docker logs -f mcp-orchestrator`
   - Check metrics: `curl http://localhost:8080/metrics`

2. **Short term** (5-30 min):
   - Check rate limiter not leaking memory
   - Check PostgreSQL is running: `docker-compose ps postgres`
   - Check LLM service availability

3. **Medium term** (30 min - 2 hours):
   - Review ROADMAP.md for known issues
   - Check PRODUCTION_CHECKLIST.md
   - Review docs/DEPLOYMENT_PRODUCTION.md

4. **Long term**:
   - Create GitHub issue with:
     - Error logs
     - Metrics snapshot
     - Steps to reproduce
     - Environment details

---

## 📅 TIMELINE SUMMARY

```
CRITICAL PATH TO PRODUCTION:
├─ Week 1: Implement Fase 1 (C1-C4) - 4-5 hours
├─ Week 1: Test in Staging - 4 hours  
├─ Week 2: Implement Fase 2 (H1-H4) - 5-7 hours
├─ Week 2: Test in Staging - 4 hours
└─ Week 3: Deploy to Production ✅

OPTIONAL:
└─ Week 3-4: Implement Fase 3 (P1-P4) - 11-16 hours

TOTAL EFFORT: 20-28 hours of development time
DEPLOYMENT READY: Week 3-4
```

---

**For more details see: ROADMAP.md**

