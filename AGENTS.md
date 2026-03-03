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

# View logs
docker logs -f mcp-orchestrator

# Restart after changes
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

    "github.com/amphora/mcp-go/internal/config"
    mcptypes "github.com/amphora/mcp-go/internal/mcp"
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
    Name        string        `yaml:"name"`
    Description string        `yaml:"description"`
    Command     string        `yaml:"command"`
    Args        []string      `yaml:"args"`
    Timeout     time.Duration `yaml:"timeout"`
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

### Python Tools

**Protocol:** JSON over stdin/stdout
```python
def read_request() -> dict:
    return json.loads(sys.stdin.read())

def write_response(response: dict) -> None:
    print(json.dumps(response, default=str))

# Error response
{"success": False, "error": {"code": "ERROR_CODE", "message": str(e)}}
```

## Project Structure
```
mcp-go/
├── cmd/server/          # Main entry point
├── internal/
│   ├── config/          # Configuration
│   ├── executor/        # Subprocess execution
│   ├── mcp/            # MCP types
│   ├── metrics/        # Prometheus metrics
│   ├── health/         # Health check endpoints
│   ├── transport/      # HTTP/SSE transport
│   └── tracing/        # Distributed tracing
├── tools/              # Python tools (10 tools)
├── configs/            # YAML configs
├── tests/              # Integration tests
└── deployments/        # Docker Compose
```

## Adding New Tools

1. Create `tools/new_tool/main.py`
2. Follow JSON stdin/stdout protocol
3. Add config to `configs/config.yaml`:
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
4. Restart: `docker-compose restart mcp-server`

## Key Patterns

1. Tools communicate via JSON over stdin/stdout
2. Track request IDs for debugging
3. Respect configured timeouts
4. Handle context cancellation gracefully

## Dependencies

- Go 1.23+
- `github.com/mark3labs/mcp-go` - MCP protocol
- `github.com/rs/zerolog` - Logging
- `gopkg.in/yaml.v3` - YAML parsing
- `github.com/google/uuid` - UUID generation
- `github.com/prometheus/client_golang` - Metrics
- `github.com/redis/go-redis/v9` - Redis client

## Related Docs

- [API Reference](docs/API.md) - Complete API docs
- [Development Guide](docs/DEVELOPMENT.md) - Dev instructions
- [Logging](docs/LOGGING.md) - HTTP logging details
