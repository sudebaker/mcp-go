# Production Guide - MCP-Go Orchestrator

**Status**: ✅ PRODUCTION-READY
**Updated**: Mayo 2026

---

## Quick Status

| Area | Status | Score |
|------|--------|-------|
| Architecture | ✅ Solid | 9/10 |
| Code Quality | ✅ Excellent | 8.5/10 |
| Security | ✅ Hardened | 8/10 |
| Testing | ✅ Comprehensive | 8/10 |
| Documentation | ✅ Complete | 9/10 |
| **OVERALL** | **✅ READY** | **8.5/10** |

---

## Production Checklist

### Must Have (Done)

- [x] Rate limiter memory leak fixed
- [x] Prometheus /metrics endpoint exposed
- [x] JSON schema validation for tool inputs
- [x] Integration tests (7 tests passing)
- [x] Distributed tracing (custom Tracer + Span)
- [x] /health/detailed endpoint with component status
- [x] Docker resource limits configured
- [x] 31+ security tests passing

### Should Have (Done)

- [x] SSRF protection via SSRF_ALLOWLIST
- [x] SSTI sandbox (SandboxedEnvironment)
- [x] ReDoS protection (pre-compiled regex)
- [x] YAML safe deserialization
- [x] Prompt injection sanitization
- [x] S3 operation timeouts
- [x] HTTP request logging
- [x] User isolation for KB tools (user_id via MCP session)

### Nice to Have (Optional - Phase 3)

- [x] HTTPS/TLS configuration (documented below)
- [ ] API key authentication
- [x] Audit logging
- [ ] Secret management
- [ ] Network policies
- [ ] PostgreSQL backup strategy
- [ ] Prometheus/Grafana dashboards
- [ ] Alert rules

---

## Deployment

### Prerequisites

```bash
# Verify build
go build -o bin/mcp-server ./cmd/server

# Run tests
go test ./...
go test -race ./...

# Verify Docker services
docker-compose -f deployments/docker-compose.yml ps
```

### Environment Variables

```bash
# Required
LLM_API_URL=http://localhost:11434
LLM_MODEL=llama3
DATABASE_URL=postgresql://mcp:***@localhost:5432/knowledge

# RustFS/S3 (required for storage tools)
RUSTFS_ENDPOINT=http://rustfs:9000
RUSTFS_PUBLIC_URL=http://your-public-url:9000
RUSTFS_ACCESS_KEY_ID=rustfsadmin
RUSTFS_SECRET_ACCESS_KEY=rustfsadmin

# Security (defaults work for most deployments)
SSRF_ALLOWLIST=rustfs
S3_OPERATION_TIMEOUT_SECONDS=30
RUSTFS_PRESIGNED_TTL_SECONDS=3600
```

### Start Services

```bash
cd deployments
docker-compose up -d

# Verify
curl http://localhost:8080/health
curl http://localhost:8080/metrics
```

---

## Monitoring

### Health Endpoints

```bash
# Basic health
curl http://localhost:8080/health

# Detailed health
curl http://localhost:8080/health/detailed

# Prometheus metrics
curl http://localhost:8080/metrics
```

### Logs

```bash
# All logs
docker logs -f mcp-orchestrator

# HTTP requests only
docker logs mcp-orchestrator | grep "Request"

# Errors
docker logs mcp-orchestrator | grep -E "error|panic|timeout"
```

### Metrics to Monitor

| Metric | Threshold | Action |
|--------|-----------|--------|
| Memory usage | > 80% | Scale or investigate |
| Error rate | > 5% | Investigate errors |
| Tool timeout rate | > 10% | Increase timeout or optimize |
| Health check failures | > 0 | Immediate investigation |
| `mcp_admin_operations_total` | unexpected spikes | Review audit log for abuse |

---

## Database Connection Budget

The orchestrator opens multiple database connection pools. PostgreSQL `max_connections` must be sized for the worst-case concurrent load:

| Consumer | Connections | Notes |
|----------|-------------|-------|
| `mcp_app` | 10 | Default Go `sql.DB` pool for tool execution |
| `mcp_admin` | 10 | Admin endpoint `sql.DB` pool |
| `mcp_health` | 2 | Health check `sql.DB` pool |
| Python KB tools | ~10 peak | One connection per concurrent KB request |
| Safety margin | +20% | Headroom for bursts |

**Recommended `max_connections`:**

```text
max_connections = 10 (app) + 10 (admin) + 2 (health) + 10 (python kb) + 20% margin ≈ 40
```

The included `deployments/docker-compose.yml` sets `max_connections=50`, which is suitable for the default deployment. Tune upward for high-traffic production deployments.

---

## Security

See [SECURITY.md](SECURITY.md) for full security documentation.

### Quick Security Checklist

- [ ] SSRF_ALLOWLIST configured (minimum: `rustfs`)
- [ ] RUSTFS_ENDPOINT matches actual hostname
- [ ] S3_OPERATION_TIMEOUT_SECONDS appropriate for network
- [ ] RUSTFS_PRESIGNED_TTL_SECONDS based on security requirements
- [ ] Run security tests: `python -m pytest tests/test_security_mitigations.py -v`

---

## Troubleshooting

### Server not responding

```bash
docker compose ps
docker logs --tail 200 mcp-orchestrator
curl -v http://localhost:8080/health
```

### Tool failures

```bash
docker logs --tail 200 mcp-orchestrator | grep -E "tool|error|timeout"
```

### LLM not responding

Verify Ollama is running and LLM_API_URL/LLM_MODEL are correct in config.

### Memory growing

```bash
# Check for goroutine leaks
docker stats --no-stream mcp-orchestrator

# Restart if needed
docker-compose restart mcp-server
```

---

## TLS / HTTPS

The MCP server itself does not terminate TLS. In production (or any non-air-gapped environment), run it behind a reverse proxy that handles HTTPS.

### Caddy (recommended)

```Caddyfile
mcp.example.com {
    reverse_proxy mcp-orchestrator:8080
}
```

Run with `caddy run --config Caddyfile`. Caddy automatically obtains and renews Let's Encrypt certificates.

### nginx

```nginx
server {
    listen 443 ssl http2;
    server_name mcp.example.com;

    ssl_certificate     /etc/letsencrypt/live/mcp.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mcp.example.com/privkey.pem;

    location / {
        proxy_pass         http://mcp-orchestrator:8080;
        proxy_http_version 1.1;
        proxy_set_header   Host $host;
        proxy_set_header   X-Real-IP $remote_addr;
        proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
    }
}
```

After enabling TLS, set `BASE_URL=https://mcp.example.com` so generated URLs and webhooks use the correct scheme.

### Admin endpoints

Admin endpoints (`/admin/*`) are protected by `ADMIN_API_KEY`. Keep them behind the same TLS terminator; never expose `/admin` over plain HTTP in production.

---

## Related Documentation

| Document | Purpose |
|----------|---------|
| [API.md](API.md) | Complete API reference |
| [ARCHITECTURE.md](ARCHITECTURE.md) | System architecture |
| [DEVELOPMENT.md](DEVELOPMENT.md) | Dev setup and workflow |
| [SECURITY.md](SECURITY.md) | Security mitigations |
| [LOGGING.md](LOGGING.md) | HTTP request logging |
| [TESTING.md](TESTING.md) | Test suite |
| [AGENTS.md](AGENTS.md) | AI agent instructions |

---

**For detailed technical analysis, see the `plans/` and `superpowers/` directories.**