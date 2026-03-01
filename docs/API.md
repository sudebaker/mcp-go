# MCP Orchestrator API Reference

This document describes the HTTP API and MCP protocol interface for the MCP Orchestrator server.

## Server Overview

| Property | Value |
|----------|-------|
| Protocol | MCP (Model Context Protocol) |
| Transport | Streamable HTTP |
| Default Port | 8080 |
| Host | 0.0.0.0 |

---

## HTTP Endpoints

### GET /

Returns server information and available endpoints.

**Response:**
```json
{
  "name": "mcp-orchestrator",
  "version": "0.1.0",
  "protocol": "MCP (Model Context Protocol)",
  "transport": "Streamable HTTP",
  "description": "MCP server that orchestrates Python tools via subprocess execution",
  "endpoints": {
    "GET /": "This info page",
    "GET /health": "Health check endpoint",
    "GET /health/detailed": "Detailed health check",
    "GET /metrics": "Prometheus metrics",
    "POST /mcp": "MCP Streamable HTTP endpoint",
    "GET /openapi.json": "OpenAPI specification"
  }
}
```

---

### GET /health

Basic health check endpoint.

**Response:**
```json
{
  "status": "healthy",
  "service": "mcp-orchestrator",
  "version": "0.1.0",
  "protocol": "mcp",
  "transport": "streamable-http"
}
```

---

### GET /health/detailed

Detailed health check with system information.

**Response:**
```json
{
  "status": "healthy",
  "service": "mcp-orchestrator",
  "version": "0.1.0",
  "uptime": "2m30s",
  "tools": {
    "total": 10,
    "registered": ["echo", "generate_report", "analyze_data", ...]
  },
  "system": {
    "go_version": "1.21",
    "num_cpu": 4,
    "num_goroutine": 15
  }
}
```

---

### GET /metrics

Prometheus metrics endpoint for monitoring.

**Format:** Prometheus text/plain

---

### POST /mcp

MCP Streamable HTTP endpoint. Handles all MCP protocol operations.

**Headers:**
```
Content-Type: application/json
Accept: application/json, text/event-stream
```

---

### GET /openapi.json

OpenAPI 3.0 specification for the MCP server.

---

### GET /docs/

Interactive API documentation (Swagger UI).

---

## MCP Methods

### initialize

Initializes the MCP session.

### ping

Simple ping/pong for connectivity check.

### tools/list

Lists all available tools registered with the MCP server.

### tools/call

Executes a specific tool with provided arguments.

---

## Available Tools

### echo

Simple text echo for testing.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| text | string | Yes | Text to echo back |
| debug | boolean | No | Include context info in response |

---

### generate_report

Generates PDF reports from templates. Supports uploading to RustFS/S3 storage.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| report_type | string | Yes | `incident`, `meeting`, `audit`, `executive_summary`, `formal_report`, `corporate_email`, `llm_response` |
| data | object | Yes | Report data object |
| output_path | string | No | Optional output path |

**Output:** PDF base64 + optional presigned URL (RustFS)

---

### analyze_data

Analyzes Excel/CSV files using Pandas and LLM-generated code.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| file_url | string | No | HTTP URL to data file (presigned S3 URL) |
| file_name | string | No | Original filename with extension |
| question | string | Yes | Natural language question |
| output_format | string | No | `text`, `json`, `markdown`, `png` |
| __files__ | array | No | Attached files (OpenWebUI) |

---

### analyze_image

Analyzes images using OCR and vision models.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| image_path | string | Yes | URL or path to image |
| task | string | Yes | `ocr`, `describe`, `extract_entities`, `answer` |
| question | string | Conditional | Required for `answer` task |

---

### kb_ingest

Stores content in the knowledge base (PostgreSQL + pgvector).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| content | string | Yes | Text content to store |
| collection | string | No | Collection name (default: `default`) |
| metadata | object | No | Additional metadata |

---

### kb_search

Searches the knowledge base.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| query | string | Yes | Search query |
| collection | string | No | Collection name (default: `default`) |
| top_k | integer | No | Number of results (default: 5) |
| search_type | string | No | `semantic`, `keyword`, `hybrid` |

---

### batch_summarize

Summarizes multiple documents at once.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| __files__ | array | Yes | Files to summarize |
| summary_type | string | No | `individual`, `master`, `both` (default: `both`) |
| focus | string | No | Optional focus area |
| max_length | integer | No | Max summary length (default: 500) |

---

### regulation_diff

Compares two versions of a document or regulation.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| __files__ | array | Yes | Exactly 2 files: [0]=old, [1]=new |
| focus | string | No | Optional focus area |
| output_format | string | No | `markdown` or `structured` |

---

### config_auditor

Audits configuration files for security issues.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| __files__ | array | Yes | Configuration files to audit |
| rules | array | No | `secrets`, `empty_required`, `dangerous_ports`, `debug_mode`, `hardcoded_ips` |
| severity_filter | string | No | `all`, `critical`, `high`, `medium` |

**Output:** Findings array + security score (0-100)

---

### document_classifier

Classifies documents into categories.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| __files__ | array | Yes | Documents to classify |
| categories | array | No | Custom categories (default: predefined list) |
| language | string | No | `auto`, `es`, `en` (default: `auto`) |

---

## Error Responses

### Tool Execution Error
```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "error": {
    "code": -32603,
    "message": "Error message from tool execution"
  }
}
```

### Invalid Request
```json
{
  "jsonrpc": "2.0",
  "id": null,
  "error": {
    "code": -32600,
    "message": "Invalid Request"
  }
}
```

---

## Rate Limiting

| Setting | Default |
|---------|---------|
| RPS | 10 |
| Burst | 20 |

---

## Environment Variables

### LLM Configuration
| Variable | Default | Description |
|----------|---------|-------------|
| LLM_API_URL | http://localhost:11434 | LLM API (Ollama) |
| LLM_MODEL | llama3 | LLM model |

### Database Configuration
| Variable | Default | Description |
|----------|---------|-------------|
| DATABASE_URL | postgresql://mcp:mcp@localhost:5432/knowledge | PostgreSQL connection |
| REDIS_URL | redis://localhost:6379/0 | Redis connection |

### RustFS/S3 Configuration
| Variable | Default | Description |
|----------|---------|-------------|
| RUSTFS_ENDPOINT | rustfs:9000 | S3-compatible storage endpoint |
| RUSTFS_ACCESS_KEY_ID | rustfsadmin | Access key |
| RUSTFS_SECRET_ACCESS_KEY | rustfsadmin | Secret key |
| S3_BUCKET_NAME | openwebui | Bucket name |

---

## Architecture

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│  OpenWebUI  │────▶│  MCPO Proxy  │────▶│ MCP Server  │
│  (port 3000)│     │  (port 8001) │     │ (port 8080) │
└─────────────┘     └──────────────┘     └──────┬──────┘
                                                 │
                    ┌────────────┐              │
                    │  Ollama    │◀─────────────┘
                    │ (GPU/LLM)  │
                    └────────────┘
                                                 │
┌─────────────┐     ┌──────────────┐              │
│  RustFS     │◀────│ Python Tools │◀─────────────┘
│ (S3/MinIO)  │     │              │
└─────────────┘     └──────────────┘
                              │
                    ┌────────┴────────┐
                    │ PostgreSQL      │
                    │ (pgvector)      │
                    └─────────────────┘
```

---

## Related Documentation

- [Logging](LOGGING.md) - HTTP request logging
- [Development Guide](DEVELOPMENT.md) - Building and testing
- [Usage Guide](../USAGE.md) - User-facing documentation
