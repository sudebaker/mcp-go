Moved from: ../AGENTS.md 

# AGENTS.md - MCP-Go Project Guide

Guidelines for agentic coding agents working on this MCP (Model Context Protocol) Orchestrator.

## Build / Lint / Test Commands

### Go Commands
```bash
# Build the main server binary
go build -o bin/mcp-server ./cmd/server

# Run all tests
go test ./...

# Run a single test with verbose output
go test -run TestSpecificName ./path/to/package -v

# Run tests with coverage report
go test -cover ./...

# Format, vet, and run tests
go fmt ./... && go vet ./... && go test ./...

# Test a specific test function in a package
go test -run TestLoadConfig ./internal/config -v
```

### Docker Commands
```bash
# Start all services
cd deployments && docker-compose up -d
docker logs -f mcp-orchestrator
docker-compose restart mcp-server
```

### Integration Tests
```bash
./tests/test_excel_analysis.sh
./tests/test_quick.sh
./tests/test_logging.sh
./tests/test_image_format_validation.sh
./tests/test_kb_memory.sh
./tests/test_suite_complete.sh

# Security tests
python -m pytest tests/test_security_mitigations.py -v
```

## Code Style

### Go Imports (3 groups, alphabetical)
```go
import (
    "context"
    "os"
    "time"

    "github.com/google/uuid"
    "github.com/rs/zerolog/log"
    "gopkg.in/yaml.v3"

    "github.com/sudebaker/mcp-go/internal/config"
    mcptypes "github.com/sudebaker/mcp-go/internal/mcp"
)
```
**Groups:** stdlib → external → internal

### Go Naming Conventions
- **Packages**: lowercase, short, singular (e.g., `config`, `executor`, `mcp`)
- **Exported**: PascalCase (e.g., `NewExecutor`, `LoadConfig`)
- **Private**: camelCase (e.g., `buildInputSchema`, `expandEnvVars`)
- **Interfaces**: `-er` suffix (e.g., `Executor`, `Tracer`)
- **Constants/Errors**: UPPERCASE (e.g., `ErrorCodeTimeout`)
- **Variables**: camelCase (e.g., `requestID`, `toolName`)

### Go Structs
```go
type ToolConfig struct {
    Name        string                 `yaml:"name"`
    Description string                 `yaml:"description"`
    Command     string                 `yaml:"command"`
    Args        []string               `yaml:"args"`
    Timeout     time.Duration          `yaml:"timeout"`
    InputSchema map[string]interface{} `yaml:"input_schema"`
}
```

### Go Error Handling
```go
result, err := someOperation()
if err != nil {
    return nil, fmt.Errorf("failed to execute: %w", err)
}
```

### Go Logging (zerolog)
```go
log.Info().Str("tool", name).Msg("Executing")
log.Error().Err(err).Str("file", path).Msg("Failed")
```

## Project Structure
```
mcp-go/
├── cmd/server/          # Main entry point
├── internal/
│   ├── config/          # Configuration loading
│   ├── executor/        # Subprocess execution
│   ├── mcp/             # MCP types
│   ├── metrics/         # Prometheus metrics
│   ├── health/          # Health check endpoints
│   ├── transport/       # HTTP/SSE transport
│   └── tracing/         # Distributed tracing
├── tools/               # Python tools (JSON stdin/stdout)
├── configs/             # YAML configs
└── deployments/         # Docker Compose
```

## Python Tools Protocol
Tools communicate via JSON over stdin/stdout:
```python
import json, sys

def read_request() -> dict:
    return json.loads(sys.stdin.read())

def write_response(response: dict) -> None:
    print(json.dumps(response, default=str))

# Error response format
{"success": False, "error": {"code": "ERROR_CODE", "message": str(e)}}
```

## Adding New Tools

1. Create `tools/new_tool/main.py` following JSON protocol
2. Add config to `configs/config.yaml`:
   ```yaml
   tools:
     - name: "new_tool"
       description: "Description for LLM"
       command: "python3"
       args: ["/app/tools/new_tool/main.py"]
       timeout: "60s"
       input_schema:
         type: object
         properties:
           param:
             type: string
   ```
3. Restart: `docker-compose restart mcp-server`

## Key Patterns

1. Tools communicate via JSON over stdin/stdout
2. Track request IDs for debugging
3. Respect configured timeouts
4. Handle context cancellation gracefully

## Local Storage Architecture (RustFS)

This project uses **RustFS** as a local S3-compatible object storage solution. Data stays on-premise - no cloud services are used.

### URL Architecture

The system uses three distinct URLs for RustFS access:

| URL | Purpose | Who Uses It |
|-----|---------|--------------|
| `RUSTFS_ENDPOINT` | Internal MCP→RustFS communication | Python tools, Go download handler |
| `RUSTFS_PUBLIC_URL` | External agent→RustFS direct download | Clients downloading files directly |
| `BASE_URL` | MCP server download links | Clients accessing `/download/` endpoint |

```
┌─────────────────────────────────────────────────────────────┐
│                    Docker Network                            │
│  ┌──────────────┐         ┌──────────────┐                │
│  │  mcp-server   │────────▶│    rustfs    │:9000           │
│  └──────────────┘         └──────────────┘                │
│         │  RUSTFS_ENDPOINT=http://rustfs:9000                │
└─────────┼────────────────────────────────────────────────────┘
          │
          │ Tool uploads file → generates presigned URL
          │ Tool rewrites URL: rustfs:9000 → RUSTFS_PUBLIC_URL
          │
          ▼
   http://192.168.1.100:9000/bucket/file?signature...
          │
          │ RUSTFS_PUBLIC_URL - external access
          ▼
┌─────────────────────────────────────────────────────────────┐
│              External Agents (outside Docker)              │
│         Download directly from RustFS public URL           │
└─────────────────────────────────────────────────────────────┘

Alternatively, clients download via MCP server proxy:

   Client → GET /download/rustfs/bucket/object → MCP Server → RustFS presigned redirect
                      ↑
                BASE_URL
```

### Configuration Variables

| Variable | Required | Example | Description |
|----------|-----------|---------|-------------|
| `RUSTFS_ENDPOINT` | Yes | `http://rustfs:9000` | Internal endpoint for MCP→RustFS communication |
| `RUSTFS_PUBLIC_URL` | Yes | `http://192.168.1.100:9000` | Public URL for external agents |
| `BASE_URL` | Yes | `http://mcp.example.com:8080` | MCP server public URL for `/download/` links |
| `RUSTFS_ACCESS_KEY_ID` | Yes | `rustfsadmin` | RustFS access key |
| `RUSTFS_SECRET_ACCESS_KEY` | Yes | `rustfsadmin` | RustFS secret key |
| `DOWNLOAD_URL_EXPIRY_HOURS` | No | `24` | Presigned URL validity (default: 24h) |

### Common Scenarios

**Scenario 1: RustFS in same Docker compose**
```bash
RUSTFS_ENDPOINT=http://rustfs:9000          # Docker DNS resolves 'rustfs'
RUSTFS_PUBLIC_URL=http://192.168.1.100:9000  # Host IP for external access
BASE_URL=http://192.168.1.100:8080           # MCP server external URL
```

**Scenario 2: Remote RustFS server**
```bash
RUSTFS_ENDPOINT=http://192.168.1.100:9000   # Remote RustFS server
RUSTFS_PUBLIC_URL=http://192.168.1.100:9000  # Same remote server (public)
BASE_URL=http://mcp.example.com:8080         # MCP server domain
```

**Scenario 3: Remote RustFS with HTTPS**
```bash
RUSTFS_ENDPOINT=https://storage.example.com:9000  # HTTPS remote
RUSTFS_PUBLIC_URL=https://storage.example.com:9000
BASE_URL=https://mcp.example.com:8080
```

### Tools Using RustFS

- **rustfs_storage**: Upload, download, list, search, delete operations
- **canvas_diagram**: Stores generated canvas files
- **pdf_reports**: Stores generated PDF reports

All these tools implement URL rewriting to convert internal URLs to public URLs before returning them to external agents.

### Configuration Notes

- If `RUSTFS_PUBLIC_URL` is not configured, tools will fail with an error
- If `BASE_URL` is not configured, download links will use `http://localhost:8080` (incorrect for external access)
- The presigned URLs contain signatures that remain valid after URL rewriting
- External agents must be able to reach `RUSTFS_PUBLIC_URL` (firewall rules, routing)
- For production, consider HTTPS with a reverse proxy

### Quick Reference

```bash
# .env file for local development
RUSTFS_ENDPOINT=http://rustfs:9000
RUSTFS_PUBLIC_URL=http://localhost:9000
BASE_URL=http://localhost:8080

# .env file for external access (host IP)
RUSTFS_ENDPOINT=http://rustfs:9000
RUSTFS_PUBLIC_URL=http://192.168.1.100:9000
BASE_URL=http://192.168.1.100:8080

# .env file for remote RustFS
RUSTFS_ENDPOINT=http://remote-rustfs:9000
RUSTFS_PUBLIC_URL=http://remote-rustfs:9000
BASE_URL=http://your-mcp-server:8080
```

## Dependencies

- Go 1.23+ | `github.com/mark3labs/mcp-go` | `github.com/minio/minio-go/v7` | `github.com/rs/zerolog` | `gopkg.in/yaml.v3` | `github.com/google/uuid` | `github.com/prometheus/client_golang` | `github.com/redis/go-redis/v9`

## Security Mitigations

- **SSRF**: URL validation prevents cloud metadata/internal network access
- **SSTI**: SandboxedEnvironment blocks template injection
- **ReDoS**: Pre-compiled regex patterns prevent DoS attacks
- **YAML**: Safe deserialization via typed unmarshaling

See [SECURITY_HARDENING.md](SECURITY_HARDENING.md) for details.

## Related Docs

- [API Reference](docs/API.md)
- [Development Guide](docs/DEVELOPMENT.md)
- [Logging](docs/LOGGING.md)
- [Security Hardening](SECURITY_HARDENING.md)
