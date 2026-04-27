# Development Guide

This guide covers how to build, test, and develop the MCP Orchestrator and its Python utilities.

## Prerequisites

- Go 1.25+
- Python 3.10+
- Docker & Docker Compose
- (Optional) Ollama for local LLM inference

---

## Building the MCP Server

### Build from Source

```bash
# Navigate to project
cd mcp-go

# Build the Go binary
go build -o bin/mcp-server ./cmd/server

# Build with debug symbols
go build -gcflags=all=-d=warf=1 -o bin/mcp-server-debug ./cmd/server
```

### Run Locally (without Docker)

```bash
# Set environment variables
export LLM_API_URL=http://localhost:11434
export LLM_MODEL=llama3
export DATABASE_URL=postgresql://mcp:mcp@localhost:5432/knowledge

# Run the server
./bin/mcp-server -config configs/config.yaml
```

### Run Tests

```bash
# Run all tests
go test ./...

# Run specific test
go test -run TestToolExecutor ./internal/executor/...

# Run with verbose output
go test -v ./...

# Run with coverage
go test -cover ./...

# Run fmt, vet, and tests
go fmt ./... && go vet ./... && go test ./...
```

---

## Python Tools Development

### Available Tools

| Tool | Description | Timeout |
|------|-------------|---------|
| `echo` | Simple text echo for testing | 10s |
| `generate_report` | PDF report generation | 120s |
| `analyze_data` | Excel/CSV analysis with Pandas | 180s |
| `analyze_image` | OCR and vision analysis | 120s |
| `kb_ingest` | Store content in knowledge base | 300s |
| `kb_search` | Search knowledge base | 60s |
| `batch_summarize` | Summarize multiple documents | 300s |
| `regulation_diff` | Compare document versions | 180s |
| `config_auditor` | Audit config files for security | 120s |
| `document_classifier` | Classify documents into categories | 180s |

---

### Tool Structure

```
tools/
├── echo/
│   └── main.py
├── pdf_reports/
│   └── main.py
├── data_analysis/
│   └── main.py
├── vision_ocr/
│   └── main.py
├── knowledge_base/
│   └── main.py
├── batch_summarize/
│   └── main.py
├── regulation_diff/
│   └── main.py
├── config_auditor/
│   └── main.py
└── document_classifier/
    └── main.py
```

---

### Communication Protocol

Tools communicate via JSON over stdin/stdout:

**Input (from Go server):**
```json
{
  "request_id": "uuid-string",
  "arguments": {
    "param1": "value1",
    "param2": "value2"
  },
  "context": {
    "llm_api_url": "http://localhost:11434",
    "llm_model": "llama3",
    "working_dir": "/data",
    "database_url": "postgresql://...",
    "redis_url": "redis://..."
  }
}
```

**Output (from Python tool):**
```json
{
  "success": true,
  "request_id": "uuid-string",
  "content": [
    {
      "type": "text",
      "text": "Result text here"
    }
  ],
  "structured_content": {
    "key": "value"
  }
}
```

**Error Response:**
```json
{
  "success": false,
  "request_id": "uuid-string",
  "error": {
    "code": "ERROR_CODE",
    "message": "Human readable error message"
  }
}
```

---

### Creating a New Tool

1. **Create directory:**
   ```bash
   mkdir -p tools/my_tool
   ```

2. **Create main.py:**
   ```python
   #!/usr/bin/env python3
   import json
   import sys
   from typing import Any

   def read_request() -> dict[str, Any]:
       input_data = sys.stdin.read()
       return json.loads(input_data)

   def write_response(response: dict[str, Any]) -> None:
       print(json.dumps(response, default=str))

   def main() -> None:
       try:
           request = read_request()
           request_id = request.get("request_id", "")
           arguments = request.get("arguments", {})
           context = request.get("context", {})

           # Your tool logic here
           result = process_data(arguments)

           write_response({
               "success": True,
               "request_id": request_id,
               "content": [{"type": "text", "text": result}]
           })
       except Exception as e:
           write_response({
               "success": False,
               "request_id": request.get("request_id", ""),
               "error": {"code": "EXECUTION_FAILED", "message": str(e)}
           })

   if __name__ == "__main__":
       main()
   ```

3. **Add to config.yaml:**
   ```yaml
   tools:
     - name: "my_tool"
       description: "Description for the LLM"
       command: "python3"
       args: ["/app/tools/my_tool/main.py"]
       timeout: "60s"
       input_schema:
         type: object
         properties:
           param1:
             type: string
             description: "Parameter description"
         required:
           - param1
   ```

4. **Restart server:**
   ```bash
   docker-compose restart mcp-server
   ```

---

### Testing Tools Manually

```bash
# Test echo tool directly
echo '{"request_id": "test-123", "arguments": {"text": "Hello"}, "context": {}}' | \
  python3 tools/echo/main.py

# Test with curl via MCP
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "echo",
      "arguments": {"text": "Hello"}
    }
  }'
```

---

## Docker Development

### Start Services

```bash
cd deployments
docker-compose up -d
```

### View Logs

```bash
# All logs
docker logs -f mcp-orchestrator

# HTTP request logs only
docker logs mcp-orchestrator | grep "Request"

# Filter by endpoint
docker logs mcp-orchestrator | grep "Request completed" | jq '.path'
```

### Restart After Changes

```bash
# Restart MCP server
docker-compose restart mcp-server

# Rebuild and restart
docker-compose build mcp-server
docker-compose up -d mcp-server

# Access container
docker exec -it mcp-orchestrator bash
```

### Volume Mounts

| Host | Container | Purpose |
|------|-----------|---------|
| `./configs` | `/app/configs` | Configuration (hot reload) |
| `./tools` | `/app/tools` | Python tools (hot reload) |
| `./templates` | `/app/templates` | Jinja2 templates |
| `./data` | `/data` | Working directory |

---

## HTTP Endpoints

### Download Endpoint

The `/download/` endpoint provides a proxy for downloading files from local storage or RustFS:

```bash
# Download from local storage
GET /download/local/{filename}

# Download from RustFS (redirects to presigned URL)
GET /download/rustfs/{bucket}/{object}
```

**Environment Variables:**
| Variable | Default | Description |
|----------|---------|-------------|
| `OUTPUT_DIR` | `/data/reports` | Local file storage directory |
| `DOWNLOAD_URL_EXPIRY_HOURS` | `24` | Download link validity in hours |
| `BASE_URL` | `http://localhost:8080` | Public URL for generating links |

---

## Running Tests

### Test Scripts

```bash
# Test Excel analysis
./tests/test_excel_analysis.sh

# Quick test (all services)
./tests/test_quick.sh

# Test logging
./tests/test_logging.sh
```

### Python Unit Tests

```bash
# Run Python tests
docker exec mcp-orchestrator python3 -m pytest tests/

# Specific test
docker exec mcp-orchestrator python3 tests/test_config_auditor.py
```

---

## Configuration

### Server (configs/config.yaml)

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  name: "mcp-orchestrator"
  rate_limit_rps: 10
  rate_limit_burst: 20

execution:
  default_timeout: "60s"
  working_dir: "/data"
  environment:
    LLM_API_URL: "${LLM_API_URL:-http://localhost:11434}"
    LLM_MODEL: "${LLM_MODEL:-llama3}"
    DATABASE_URL: "${DATABASE_URL:-postgresql://mcp:mcp@localhost:5432/knowledge}"
```

### Tool Timeout

```yaml
tools:
  - name: "my_tool"
    timeout: "120s"  # 2 minutes
```

---

## Code Style

Follow guidelines in `AGENTS.md`:

- **Go**: Standard library → Third-party → Internal imports
- **Python**: Type hints required, JSON stdin/stdout protocol
- **Go logging**: Use zerolog
- **Python errors**: Use structured JSON error responses

---

## Common Issues

| Issue | Solution |
|-------|----------|
| Tool not found | Check config.yaml, restart server |
| Timeout errors | Increase timeout in tool config |
| LLM not responding | Verify Ollama is running |
| Permission errors | Check volume mounts |
| Import errors | Ensure Python dependencies installed |

---

## Related Documentation

- [API Reference](API.md) - Complete API documentation
- [Logging](LOGGING.md) - HTTP request logging
- [Usage Guide](../USAGE.md) - User-facing documentation
