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
    "GET /openapi.json": "OpenAPI specification",
    "GET /download/{type}/{path}": "File download proxy (local|rustfs)"
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

### GET /download/{type}/{path}

Download files through the MCP server proxy. Supports two storage types:

| Type | Path Format | Description |
|------|-------------|--------------|
| `local` | `/download/local/{filename}` | Serve files from `/data/reports` |
| `rustfs` | `/download/rustfs/{bucket}/{object}` | Redirect to presigned RustFS URL |

**Parameters:**
- `type`: `local` or `rustfs`
- `path`: filename (local) or `bucket/object` (rustfs)

**Response:**
- `307 Temporary Redirect` → Redirects to actual file URL
- `400 Bad Request` → Invalid path format
- `404 Not Found` → File not found (local only)
- `410 Gone` → Download link expired

**Security:**
- Local downloads: Path traversal prevention, 24h link expiry
- RustFS downloads: Generates presigned URL with configured expiry

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

**Output:**
- `pdf_base64`: PDF content encoded in base64 (MCP standard)
- `download_url`: Public URL to download the PDF (valid 24h by default)
- `storage`: Object with `bucket`, `object_name`, `presigned_url`, `download_url` (if RustFS available)

---

### analyze_data

Analyzes Excel/CSV files using Pandas and LLM-generated code.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| file_url | string | No | HTTP URL to data file (presigned S3 URL or rustfs://bucket/key) |
| file_name | string | No | Original filename with extension |
| question | string | Yes | Natural language question |
| output_format | string | No | `text`, `json`, `markdown`, `png` |
| __files__ | array | No | Attached files (base64 or URL). Max size: 100MB per file when using base64 content. |

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

### weather_forecast

Gets weather forecast for specified cities using Open-Meteo API. Automatically geocodes city names to coordinates.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| locations | array | Yes | Array of city names (e.g., ["Madrid", "Barcelona"]) |
| max_days | integer | No | Number of forecast days 1-7 (default: 3) |

---

### web_scraper

Extracts content from web pages. Returns page text, links, images, or raw HTML.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| url | string | Yes | URL to scrape (http:// or https://) |
| selector | string | No | CSS selector to extract specific content |
| extract_type | string | No | `text`, `html`, `links`, `images` (default: `text`) |

---

### server_status

Returns server health report: CPU, RAM, disk usage, uptime, and running Docker containers.

*No parameters required.*

---

### transcribe

Transcribes audio files locally using Whisper AI. 100% on-premise, no data leaves the server.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| file_path | string | No | Absolute path to audio file on server |
| audio_base64 | string | No | Base64-encoded audio (alternative to file_path) |
| filename | string | No | Filename with extension when using audio_base64 |
| language | string | No | Language code (es, en, fr...). Auto-detected if omitted. |

---

### web_search

Searches the web using Brave Search API. Returns real web results with titles, URLs and descriptions.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| query | string | Yes | Search query |
| count | integer | No | Number of results, max 20 (default: 10) |
| country | string | No | Country code for results (default: ES) |
| lang | string | No | Language for results (default: es) |

---

### searxng_search

Searches the web using local self-hosted SearXNG instance. Private, unlimited, no API key.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| query | string | Yes | Search query |
| count | integer | No | Number of results, max 20 (default: 10) |
| language | string | No | Language/locale (e.g., es-ES, en-US) |
| categories | string | No | Comma-separated: general, news, images, science, it, map |
| time_range | string | No | Filter by: `day`, `week`, `month`, `year` |

---

### browser_scraper

Scrapes JavaScript-heavy or Cloudflare-protected pages using headless browser.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| url | string | Yes | URL to scrape |
| selector | string | No | CSS selector to wait for and extract |
| extract_type | string | No | `text` or `html` (default: `text`) |
| wait_ms | integer | No | MS to wait for JS rendering (default: 3000) |
| max_chars | integer | No | Maximum characters to return (default: 5000) |

---

### rss_reader

Reads RSS news feeds and returns latest headlines from multiple sources.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| limit | integer | No | Max items per feed (default: 10) |
| feeds | array | No | Filter by feed names. If omitted, fetches all feeds. |
| extract | string | No | `titles`, `content`, `full` (default: `titles`) |

---

### canvas_diagram

Creates visual diagrams using Obsidian Canvas JSON format from text descriptions.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| description | string | Yes | Text description of the diagram |
| layout | string | No | `horizontal`, `vertical`, `radial`, `auto` (default: `auto`) |
| save_path | string | No | Optional custom path for .canvas file |

---

### rustfs_storage

Interacts with RustFS/S3 storage for file operations.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| operation | string | Yes | `upload`, `download`, `list`, `search`, `delete`, `stat` |
| bucket | string | No | S3 bucket name (default: default) |
| key | string | No | Object key (path) in bucket |
| content | string | No | Base64-encoded content for upload |
| prefix | string | No | Prefix for list/search operations |
| max_keys | integer | No | Max items to return (default: 100) |
| expiry | integer | No | URL expiry in seconds for download (default: 3600) |

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
| RUSTFS_ENDPOINT | rustfs:9000 | S3-compatible storage endpoint (internal Docker network) |
| RUSTFS_PUBLIC_URL | **required** | Public URL for external agents (e.g., http://192.168.1.100:9000) |
| RUSTFS_ACCESS_KEY_ID | rustfsadmin | Access key |
| RUSTFS_SECRET_ACCESS_KEY | rustfsadmin | Secret key |
| S3_BUCKET_NAME | default | Bucket name |
| SSRF_ALLOWLIST | rustfs | Comma-separated list of allowed internal hosts/CIDR ranges |
| S3_OPERATION_TIMEOUT_SECONDS | 30 | Timeout for S3 read operations (seconds) |
| RUSTFS_PRESIGNED_TTL_SECONDS | 3600 | Presigned URL validity window (seconds) |
| DOWNLOAD_URL_EXPIRY_HOURS | 24 | Download URL validity window (hours) |

**Security Notes:**
- `SSRF_ALLOWLIST`: Controls which internal hosts can be accessed via `file_url` parameter. Default allows only `rustfs`.
- `S3_OPERATION_TIMEOUT_SECONDS`: Prevents indefinite blocking on slow S3 operations.
- `RUSTFS_PRESIGNED_TTL_SECONDS`: Controls how long uploaded file URLs remain valid.
- `DOWNLOAD_URL_EXPIRY_HOURS`: Controls how long `/download/` URLs remain valid (24h default).

**Note:** `RUSTFS_PUBLIC_URL` is required for tools that generate presigned URLs (rustfs_storage, canvas_diagram, pdf_reports). The server uses `RUSTFS_ENDPOINT` for internal communication and rewrites URLs to `RUSTFS_PUBLIC_URL` before returning them to external agents.

---

## Architecture

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│  External   │────▶│  MCP Server  │────▶│  Python     │
│  Client     │     │  (port 8080) │     │  Tools      │
└─────────────┘     └──────────────┘     └──────┬──────┘
                                                  │
                     ┌────────────┐              │
                     │  Ollama    │◀─────────────┘
                     │ (GPU/LLM)  │
                     └────────────┘
                                                  │
                     ┌──────────────┐              │
                     │  RustFS     │◀─────────────┘
                     │ (S3/MinIO)  │
                     └──────────────┘
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
