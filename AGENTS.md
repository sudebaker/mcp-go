# AGENTS.md - MCP-Go Project Guide

This document provides guidelines for agentic coding agents working on this MCP (Model Context Protocol) Orchestrator project in Go.

## Build / Lint / Test Commands

### Go Commands
```bash
# Build the main server binary
go build -o bin/mcp-server ./cmd/server

# Run all tests
go test ./...

# Run specific test file
go test ./tests/server_test.go -v

# Run tests with coverage
go test -cover ./...

# Format Go code
go fmt ./...

# Vet for suspicious constructs
go vet ./...
```

### Docker Commands
```bash
# Start all services
cd deployments && docker-compose up -d

# Stop all services
docker-compose down

# View MCP server logs
docker logs -f mcp-orchestrator

# Restart MCP server after code changes
docker-compose restart mcp-server

# Access container shell
docker exec -it mcp-orchestrator bash
```

### Integration Tests
```bash
# Run Excel analysis test suite
./tests/test_excel_analysis.sh

# Run quick test suite (all services)
./tests/test_quick.sh

# Test HTTP request logging
./tests/test_logging.sh

# List available tools
docker exec mcp-orchestrator python3 -c "import json; print(json.dumps({'test': True}))" | python3 /app/tools/echo/main.py
```

## Code Style Guidelines

### Go Code Style

#### Imports
Organize imports in three groups separated by blank lines:
1. Standard library (alphabetically)
2. Third-party packages (alphabetically)
3. Local/internal packages (alphabetically)

```go
import (
    "context"
    "os"
    "time"

    "github.com/rs/zerolog/log"
    "gopkg.in/yaml.v3"

    "github.com/amphora/mcp-go/internal/config"
    "github.com/amphora/mcp-go/internal/executor"
)
```

#### Naming Conventions
- **Packages**: lowercase, single word preferred (e.g., `config`, `executor`, `mcp`)
- **Exported functions/types**: PascalCase (e.g., `NewExecutor`, `ExecuteResult`)
- **Private functions/variables**: camelCase (e.g., `buildInputSchema`, `timeout`)
- **Constants**: PascalCase for exported, camelCase for private
- **Interface names**: typically `-er` suffix (e.g., `Executor`, `Handler`)
- **Error variables**: `Err` prefix for exported (e.g., `ErrConfigNotFound`)

#### Structs and JSON Tags
Use PascalCase for field names, with `json` tags in snake_case:
```go
type ToolConfig struct {
    Name        string                 `yaml:"name"`
    Description string                 `yaml:"description"`
    Command     string                 `yaml:"command"`
    Timeout     time.Duration          `yaml:"timeout"`
    InputSchema map[string]interface{} `yaml:"input_schema"`
}
```

#### Error Handling
- Always check and handle errors explicitly
- Use fmt.Errorf with %w for wrapping errors
- Define custom error types when needed
- Return errors as the last return value

```go
result, err := someOperation()
if err != nil {
    return nil, fmt.Errorf("failed to execute: %w", err)
}
```

#### Logging
Use zerolog for structured logging:
```go
log.Info().
    Str("tool", toolName).
    Str("request_id", requestID).
    Msg("Executing subprocess")

log.Error().
    Err(err).
    Str("file", path).
    Msg("Failed to load file")
```

#### Context Usage
- Always accept `context.Context` as the first parameter in functions that may block
- Use `context.WithTimeout` for operations with time limits
- Propagate context through calls consistently

#### Comments
- Package comments: describe package purpose
- Exported functions: brief description, no need to mention parameter names in description
- No comments for obvious code

### Python Tool Style

#### Structure
Python tools follow a consistent stdin/stdout JSON protocol:
```python
#!/usr/bin/env python3
import json
import sys

def read_request() -> dict:
    input_data = sys.stdin.read()
    return json.loads(input_data)

def write_response(response: dict) -> None:
    print(json.dumps(response, default=str))

def main() -> None:
    request = read_request()
    # Process request
    write_response({"success": True, ...})

if __name__ == "__main__":
    main()
```

#### Type Hints
Always use type hints for function signatures:
```python
def load_data(file_path: str) -> pd.DataFrame:
    ...

def format_result(result: Any, output_format: str) -> str:
    ...
```

#### Error Handling
Use specific exceptions and return JSON error responses:
```python
try:
    result = process_data(data)
except FileNotFoundError as e:
    write_response({
        "success": False,
        "error": {"code": "FILE_NOT_FOUND", "message": str(e)}
    })
    return
```

#### Constants
Use UPPER_SNAKE_CASE for module-level constants:
```python
SAFE_BUILTINS = {
    'abs': abs,
    'len': len,
    # ...
}
```

## Project Structure

```
mcp-go/
├── cmd/server/          # Main application entry point
├── internal/
│   ├── config/          # Configuration loading and management
│   ├── executor/        # Subprocess execution for tools
│   ├── mcp/            # MCP protocol types
│   └── transport/      # HTTP/SSE transport layer
├── tools/              # Python tools (data_analysis, vision_ocr, etc.)
├── configs/            # YAML configuration files
├── tests/              # Test files and test scripts
└── deployments/        # Docker compose files
```

## Adding New Tools

1. Create Python tool in `tools/new_tool/main.py`
2. Follow the JSON stdin/stdout protocol
3. Add tool config to `configs/config.yaml`:
   ```yaml
   tools:
     - name: "new_tool"
       description: "Tool description"
       command: "python3"
       args: ["/app/tools/new_tool/main.py"]
       timeout: "60s"
       input_schema:
         type: object
         properties:
           param_name:
             type: string
   ```

## Configuration

- Main config: `configs/config.yaml`
- Environment variables use `${VAR:-default}` syntax
- Config hot-reload is enabled via fsnotify
- Server reads config on startup and reloads on changes

## Dependencies

- Go 1.23+ required
- Key dependencies:
  - `github.com/mark3labs/mcp-go` - MCP protocol implementation
  - `github.com/rs/zerolog` - Structured logging
  - `gopkg.in/yaml.v3` - YAML parsing
- Python tools use pandas, numpy, requests

## Testing Notes

- Unit tests: Use standard `go test`
- Integration tests: Bash scripts in `tests/`
- Test files: `*_test.go` in appropriate packages
- For running a single test: `go test -run TestSpecificName ./path/to/package`

## Important Patterns

1. **Subprocess Communication**: All tools communicate via JSON over stdin/stdout
2. **Request IDs**: Always track request IDs for debugging
3. **Timeouts**: Respect configured timeouts for tool execution
4. **Context Cancellation**: Always handle context cancellation gracefully
5. **Structured Responses**: Use consistent error codes in JSON responses

## HTTP Request Logging

The MCP server includes automatic HTTP request logging via middleware. Every HTTP request generates two log entries:

### Log Format

**Request Received:**
```json
{
  "level": "info",
  "method": "GET",
  "path": "/health",
  "remote_addr": "172.20.0.1:12345",
  "user_agent": "curl/8.0.0",
  "message": "Request received"
}
```

**Request Completed:**
```json
{
  "level": "info",
  "method": "GET",
  "path": "/health",
  "status": 200,
  "bytes": 161,
  "duration_ms": 0.035,
  "message": "Request completed"
}
```

### Viewing Logs

```bash
# Real-time logs
docker logs -f mcp-orchestrator

# Filter HTTP requests only
docker logs mcp-orchestrator | grep "Request"

# View last 20 requests
docker logs mcp-orchestrator --tail 40 | grep "Request"

# Test logging
./tests/test_logging.sh
```

### Implementation

- **Middleware**: `internal/transport/logging.go`
- **Integration**: Applied in `internal/transport/sse.go`
- **Documentation**: See `docs/LOGGING.md` for detailed usage

### Analyzing Logs

```bash
# Find slow requests (>100ms)
docker logs mcp-orchestrator | grep "Request completed" | \
  jq 'select(.duration_ms > 100)'

# Count by endpoint
docker logs mcp-orchestrator | grep "Request completed" | \
  jq -r '.path' | sort | uniq -c

# View errors (4xx, 5xx)
docker logs mcp-orchestrator | grep "Request completed" | \
  jq 'select(.status >= 400)'
```

