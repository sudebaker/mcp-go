# AGENTS.md - MCP-Go

Compact instructions for working in this MCP Orchestrator repo.

## Essential Commands

```bash
# Build & test Go
go build -o bin/mcp-server ./cmd/server
go fmt ./... && go vet ./... && go test ./...

# Run Go tests
go test ./...                          # all
go test -run TestName ./package -v     # specific
go test -race ./...                    # with race detector

# Python security tests
python -m pytest tests/test_security_mitigations.py -v

# MCP test client (requires services running)
python tests/mcp_test_client.py  # needs DATABASE_URL env var
python tests/mcp_test_client.py --skip-external

# Docker services
cd deployments
docker-compose up -d
docker logs -f mcp-orchestrator
```

## Go Code Style

**Imports (3 groups, alphabetical):**
```go
import (
    "context"
    "encoding/json"

    "github.com/google/uuid"
    "github.com/rs/zerolog/log"

    "github.com/sudebaker/mcp-go/internal/config"
)
```

**Error handling:** Always wrap errors with context:
```go
if err != nil {
    return nil, fmt.Errorf("operation failed: %w", err)
}
```

**Logging:** Use zerolog with structured fields:
```go
log.Info().Str("tool", name).Msg("Executing")
log.Error().Err(err).Str("file", path).Msg("Failed")
```

## Project Structure

```
mcp-go/
├── cmd/server/main.go        # Entry point
├── internal/
│   ├── config/               # YAML config loading
│   ├── executor/             # Tool subprocess execution
│   ├── mcp/                  # MCP types (SubprocessRequest/Response)
│   ├── session/              # Session store for user_id lookup
│   ├── transport/            # HTTP/SSE handlers
│   └── ...
├── tools/                    # Python tools (stdin/stdout JSON)
├── configs/config.yaml       # Tool definitions
└── deployments/              # Docker Compose
```

## Python Tools Protocol

Tools communicate via JSON over stdin/stdout:
```python
import json, sys

request = json.loads(sys.stdin.read())
# request = {"request_id": "...", "tool_name": "...", "arguments": {...}, "context": {...}}

response = {"success": True, "content": [{"type": "text", "text": "..."}]}
print(json.dumps(response, default=str))
```

**Error response:**
```python
{"success": False, "error": {"code": "ERROR_CODE", "message": str(e)}}
```

## Key Patterns

### Adding a new tool
1. Create `tools/new_tool/main.py` with JSON stdin/stdout protocol and `tool.yaml` manifest
2. Add the tool name to the appropriate toolset in `configs/toolsets.yaml`
3. Restart container: `docker-compose restart mcp-server`

### User Isolation (KB tools)
KB tools (`kb_ingest`, `kb_search`) use `context.user_id` for data isolation:
- User identity comes from `capabilities.experimental.user_id` in MCP initialize
- Go server stores `session_id → user_id` mapping in `internal/session/store.go`
- Python KB tool receives `user_id` in the `context` object of the request
- All queries filter by `user_id` - users only see their own documents

**Performance:** KB tools use a persistent process pool (5 processes per tool) to avoid reloading embedding models and database connections on each call. Latency drops from ~7s (cold) to <1s (warm).

### mcp-go Library Hooks
Uses `github.com/mark3labs/mcp-go` server hooks:
```go
hooks := &server.Hooks{}
hooks.AddAfterInitialize(func(ctx context.Context, id any, msg *mcp.InitializeRequest, result *mcp.InitializeResult) {
    if userID, ok := msg.Params.Capabilities.Experimental["user_id"].(string); ok {
        sessionStore.Set(sess.SessionID(), userID)
    }
})
hooks.AddOnUnregisterSession(func(ctx context.Context, sess server.ClientSession) {
    sessionStore.Delete(sess.SessionID())
})
```

## External Services

| Service | Internal URL | Purpose |
|---------|--------------|---------|
| PostgreSQL | `postgres:5432` | KB storage (pgvector) |
| RustFS | `rustfs:9000` | S3-compatible storage |
| Ollama | `ollama:11434` | LLM inference |
| SearXNG | `searxng:8080` | Private web search |
| browserless | `browserless:3000` | JS rendering |

## Important Paths

- **Config:** `configs/config.yaml` - tool definitions
- **Templates:** `templates/` - PDF report templates
- **Data:** `/data/` inside container - read/write workspace

## Security Mitigations

- **SSRF**: URL validation blocks cloud metadata/internal networks
- **SSTI**: Jinja2 `SandboxedEnvironment` prevents template injection
- **ReDoS**: Regex patterns pre-compiled and cached at startup
- **Prompt injection**: Content sanitization strips injection patterns

See [SECURITY.md](SECURITY.md) for details.

## Related Docs

| Doc | Purpose |
|-----|---------|
| [API.md](API.md) | MCP endpoints and tool reference |
| [DEVELOPMENT.md](DEVELOPMENT.md) | Building and adding tools |
| [SECURITY.md](SECURITY.md) | Security mitigations |
| [PRODUCTION.md](PRODUCTION.md) | Deployment checklist |