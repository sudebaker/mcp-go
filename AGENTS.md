# AGENTS.md - MCP-Go Project Guide

Guidelines for agentic coding agents working on this MCP (Model Context Protocol) Orchestrator.

## Build / Lint / Test Commands

### Go Commands
```bash
# Build the main server binary
go build -o bin/mcp-server ./cmd/server

# Run all tests
go test ./...

# Run a single test
go test -run TestSpecificName ./path/to/package -v

# Run tests with coverage
go test -cover ./...

# Format and vet
go fmt ./... && go vet ./... && go test ./...
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
```

## Code Style

### Go Imports (3 groups, alphabetical)
```go
import (
    "context"
    "os"

    "github.com/rs/zerolog/log"
    "gopkg.in/yaml.v3"

    "github.com/amphora/mcp-go/internal/config"
)
```

### Go Naming
- Packages: lowercase (e.g., `config`)
- Exported: PascalCase (e.g., `NewExecutor`)
- Private: camelCase (e.g., `buildInputSchema`)
- Interfaces: `-er` suffix (e.g., `Executor`)
- Errors: `Err` prefix (e.g., `ErrConfigNotFound`)

### Go Structs
```go
type ToolConfig struct {
    Name    string `yaml:"name"`
    Command string `yaml:"command"`
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

## Available Tools

| Tool | Description | Timeout |
|------|-------------|---------|
| `echo` | Text echo for testing | 10s |
| `generate_report` | PDF reports (incident, meeting, audit, etc.) | 120s |
| `analyze_data` | Excel/CSV analysis with Pandas | 180s |
| `analyze_image` | OCR and vision analysis | 120s |
| `kb_ingest` | Store content in knowledge base | 300s |
| `kb_search` | Search knowledge base | 60s |
| `batch_summarize` | Summarize multiple documents | 300s |
| `regulation_diff` | Compare document versions | 180s |
| `config_auditor` | Audit config files for security | 120s |
| `document_classifier` | Classify documents | 180s |

## Project Structure
```
mcp-go/
├── cmd/server/          # Main entry point
├── internal/
│   ├── config/          # Configuration
│   ├── executor/        # Subprocess execution
│   ├── mcp/            # MCP types
│   └── transport/      # HTTP/SSE transport
├── tools/              # Python tools (10 tools)
├── configs/            # YAML configs
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
5. Use structured error codes in JSON responses

## Dependencies

- Go 1.21+
- `github.com/mark3labs/mcp-go` - MCP protocol
- `github.com/rs/zerolog` - Logging
- `gopkg.in/yaml.v3` - YAML parsing

## Related Docs

- [API Reference](docs/API.md) - Complete API docs
- [Development Guide](docs/DEVELOPMENT.md) - Dev instructions
- [Logging](docs/LOGGING.md) - HTTP logging details
