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
    "GET/DELETE /admin/kb/*": "Admin KB management (requires ADMIN_API_KEY)"
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
    "total": 20,
    "registered": ["echo", "generate_report", "analyze_data", "analyze_image", "kb_ingest", "kb_search", ...]
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

Initializes the MCP session. The server accepts experimental capabilities for user identity.

**User Identity via `capabilities.experimental.user_id`:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "protocolVersion": "2024-11-05",
    "capabilities": {
      "experimental": {
        "user_id": "user_abc123"
      }
    },
    "clientInfo": {
      "name": "my-mcp-client",
      "version": "1.0.0"
    }
  }
}
```

**User Isolation:**
- The `user_id` is stored in a session store mapped to the session ID
- For KB tools (`kb_ingest`, `kb_search`), the `user_id` is injected into the subprocess context
- All KB queries are filtered by `user_id`, ensuring complete data isolation
- Session is cleaned up on disconnect via `OnUnregisterSession` hook

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

Generates PDF reports from templates.

|| Parameter | Type | Required | Description |
||-----------|------|----------|-------------|
|| report_type | string | Yes | `incident`, `meeting`, `audit`, `executive_summary`, `formal_report`, `corporate_email`, `llm_response` |
|| data | object | Yes | Report data object |
|| output_path | string | No | Optional output path |

**Report Types:**
- `formal_report`: Supports a `content` field (markdown) that is rendered as the report body. Falls back to structured `sections`, `recommendations`, etc. if `content` is not provided.
- `llm_response`: Renders markdown content with corporate styling.
- Others: Incident, meeting, audit, executive summary, corporate email.

**Output:**
- `pdf_base64`: PDF content encoded in base64 (MCP standard)
- `output_path`: Path to the generated PDF file
- `file_size`: Size of the generated PDF in bytes

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

Analyzes images using OCR and vision models. Supports local paths (e.g., `/data/uploads/image.jpg`), HTTP/HTTPS URLs, and PDF files (converted to image).

**Security:** HTTP/HTTPS URLs are validated against `SSRF_BLOCKED_NETWORKS` before download. Cloud metadata endpoints (169.254.x.x) and loopback addresses are always blocked.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| image_path | string | Yes | Local path, HTTP/HTTPS URL, or path to PDF file |
| task | string | Yes | `ocr`, `describe`, `extract_entities`, `answer` |
| question | string | Conditional | Required for `answer` task |

---

### kb_ingest

Stores content in the knowledge base (PostgreSQL + pgvector). **User isolation:** Each user can only access their own documents. User identity is established via the `capabilities.experimental.user_id` field in the MCP `initialize` request.

**Performance:** Uses a persistent process pool (5 processes per tool) to avoid reloading the embedding model and database connections on each call. Expected latency: <1s vs ~7s for cold starts.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| content | string | Yes | Text content to store |
| collection | string | No | Collection name (default: `default`) |
| metadata | object | No | Additional metadata |

**User Isolation:** The `user_id` is automatically extracted from the MCP session and attached to all ingested documents. Documents are filtered by `user_id` on all queries, ensuring complete data isolation between users.

**Deduplication:** Content is deduplicated using SHA256 hash that includes the `user_id`, allowing the same content to exist for different users in different namespaces.

---

### kb_search

Searches the knowledge base. **User isolation:** Results are automatically filtered to the current user's documents only.

**Performance:** Uses a persistent process pool (5 processes per tool) to avoid reloading the embedding model and database connections on each call. Expected latency: <1s vs ~7s for cold starts.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| query | string | Yes | Search query |
| collection | string | No | Collection name (default: `default`) |
| top_k | integer | No | Number of results (default: 5) |
| search_type | string | No | `semantic`, `keyword`, `hybrid` |

**Search Types:**
- `semantic`: Vector similarity search using pgvector
- `keyword`: PostgreSQL full-text search
- `hybrid`: Combined approach (recommended)

**User Isolation:** Search results are automatically filtered by `user_id` from the session, so users only see their own documents.

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

Extracts content from web pages using Crawl4ai. Returns LLM-optimized markdown, raw HTML, or structured data.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| url | string | Yes | URL to scrape (http:// or https://) |
| selector | string | No | CSS selector to extract specific content |
| extract_type | string | No | `text` (default, markdown), `html`, `links`, `images` |

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

### POST /upload

Uploads a file to the server for temporary storage. Files are stored in `/data/uploads/` with configurable TTL.

**Request:**
```
POST /upload
Content-Type: multipart/form-data

FormData:
  - file: (required) binary file
  - ttl: (optional) seconds until expiration (default: 3600, max: 86400)
  - collection: (optional) subdirectory for organization
```

**Response (200 OK):**
```json
{
  "success": true,
  "path": "/data/uploads/vision/abc123def456.jpg",
  "filename": "screenshot.jpg",
  "size": 81087,
  "content_type": "image/jpeg",
  "expires_at": "2026-05-17T12:00:00Z"
}
```

**Response (413 - File too large):**
```json
{
  "success": false,
  "error": "File exceeds maximum size limit (50MB)"
}
```

**Response (415 - Unsupported type):**
```json
{
  "success": false,
  "error": "Unsupported content type: video/mp4. Allowed: image/jpeg, image/png, ..."
}
```

**Allowed MIME types:**
- Images: `image/jpeg`, `image/png`, `image/webp`, `image/gif`
- Documents: `application/pdf`, `text/csv`, `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, `application/vnd.ms-excel`
- Audio: `audio/mpeg`, `audio/wav`, `audio/ogg`, `audio/webm`, `audio/flac`

**Configuration** (in `configs/config.yaml`):
```yaml
upload:
  enabled: true
  max_size_mb: 50
  default_ttl_seconds: 3600
  max_ttl_seconds: 86400
  upload_dir: /data/uploads
  allowed_types:
    - image/jpeg
    - image/png
    # ...
```

**Security:**
- Filename sanitization (removes path traversal attempts)
- Content-Type validation against whitelist
- Size limit enforcement
- Unique filename generation (UUID-based)

---

## Admin Endpoints

Admin endpoints allow management and cleanup of the Knowledge Base. All admin endpoints are protected by `ADMIN_API_KEY` (set via environment variable). If `ADMIN_API_KEY` is not configured, all admin endpoints return `503 Service Unavailable`.

**Authentication:**
```
Authorization: Bearer <ADMIN_API_KEY>
```

**Rate Limiting:**
| Method | Rate |
|--------|------|
| GET | 60 requests/minute |
| DELETE | 10 requests/minute |

**Response Header:** All admin responses include `X-Request-ID` for tracing. If the client sends an `X-Request-ID` header, it is respected (must be alphanumeric + `_-`, max 255 chars). Otherwise a UUID is generated.

---

### GET /admin/kb/users

Lists all users with document counts and storage usage.

**Response (200 OK):**
```json
[
  {
    "user_id": "user_abc123",
    "doc_count": 25,
    "bytes": 128000
  },
  {
    "user_id": "other_user",
    "doc_count": 3,
    "bytes": 15000
  }
]
```

---

### GET /admin/kb/users/{user_id}

Returns detail for a specific user, grouped by collection.

**Response (200 OK):**
```json
{
  "user_id": "user_abc123",
  "collections": [
    {
      "collection": "default",
      "doc_count": 20,
      "bytes": 100000
    },
    {
      "collection": "research",
      "doc_count": 5,
      "bytes": 28000
    }
  ]
}
```

---

### DELETE /admin/kb/users/{user_id}

Hard-deletes **all documents** for a user from `kb_documents` and cascades to `kb_chunks`. Records the action in `admin_audit_log`.

**Response (200 OK):**
```json
{
  "deleted": true,
  "user_id": "user_abc123",
  "docs_deleted": 25,
  "docs_bytes_freed": 128000
}
```

**Note:** `docs_bytes_freed` only counts `kb_documents.content`, not the associated `kb_chunks` data (content + embeddings). Actual storage freed is typically 2-3x higher.

---

### DELETE /admin/kb/users/{user_id}/collections/{collection}

Hard-deletes a specific collection for a user.

**Response (200 OK):**
```json
{
  "deleted": true,
  "user_id": "user_abc123",
  "collection": "research",
  "docs_deleted": 5,
  "docs_bytes_freed": 28000
}
```

---

### DELETE /admin/kb/collections/{collection}

Hard-deletes a collection **across all users**. This is a global operation affecting every user.

**Response (200 OK):**
```json
{
  "deleted": true,
  "collection": "temp-data",
  "docs_deleted": 100,
  "docs_bytes_freed": 500000
}
```

---

### GET /admin/kb/users/{user_id}/export

Exports a user's documents as JSON (metadata only, no content). Paginated.

**Query Parameters:**
| Parameter | Type | Default | Max | Description |
|-----------|------|---------|-----|-------------|
| limit | integer | 100 | 1000 | Items per page |
| offset | integer | 0 | — | Pagination offset |

**Response Headers:** `X-Total-Count` — total number of documents for this user.

**Response (200 OK):**
```json
{
  "user_id": "user_abc123",
  "total": 25,
  "limit": 100,
  "offset": 0,
  "docs": [
    {
      "id": 1,
      "doc_hash": "abc123...",
      "file_path": "/data/doc1.txt",
      "collection": "default",
      "metadata": {"key": "value"},
      "created_at": "2026-01-01T00:00:00Z"
    }
  ]
}
```

---

### GET /admin/audit

Returns the admin audit log (all delete operations). Paginated.

**Query Parameters:**
| Parameter | Type | Default | Max | Description |
|-----------|------|---------|-----|-------------|
| limit | integer | 50 | 500 | Items per page |
| offset | integer | 0 | — | Pagination offset |

**Response (200 OK):**
```json
{
  "entries": [
    {
      "action": "delete_user",
      "target_user_id": "user_abc123",
      "target_collection": null,
      "docs_deleted": 25,
      "bytes_freed": 128000,
      "request_id": "abc-def-123",
      "created_at": "2026-07-21T00:00:00Z"
    }
  ],
  "limit": 50,
  "offset": 0
}
```

**Audit Action Types:**
| Action | Description |
|--------|-------------|
| `delete_user` | All documents for a user deleted |
| `delete_user_collection` | Specific collection for a user deleted |
| `delete_global_collection` | Collection deleted across all users |

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

### Admin Configuration
| Variable | Default | Description |
|----------|---------|-------------|
| ADMIN_API_KEY | *(not set)* | API key for admin endpoints (`/admin/kb/*`). If not set, all admin endpoints return 503. |

### RustFS/S3 Configuration
| Variable | Default | Description |
|----------|---------|-------------|
| RUSTFS_ENDPOINT | rustfs:9000 | S3-compatible storage endpoint (internal Docker network) |
| RUSTFS_PUBLIC_URL | **required** | Public URL for external agents (e.g., http://192.168.1.100:9000) |
| RUSTFS_ACCESS_KEY_ID | rustfsadmin | Access key |
| RUSTFS_SECRET_ACCESS_KEY | rustfsadmin | Secret key |
| SSRF_ALLOWLIST | rustfs | Comma-separated list of allowed internal hosts/CIDR ranges (whitelist) |
| SSRF_BLOCKED_NETWORKS | 169.254.0.0/16,127.0.0.0/8 | Comma-separated CIDR ranges to block (blacklist) |
| S3_OPERATION_TIMEOUT_SECONDS | 30 | Timeout for S3 read operations (seconds) |
| RUSTFS_PRESIGNED_TTL_SECONDS | 3600 | Presigned URL validity window (seconds) |
| DOWNLOAD_URL_EXPIRY_HOURS | 24 | Download URL validity window (hours) |

**Security Notes:**
- `SSRF_ALLOWLIST`: Controls which internal hosts can be accessed via `file_url` parameter. Default allows only `rustfs`.
- `SSRF_BLOCKED_NETWORKS`: Additional CIDR ranges to block (e.g., "192.168.1.0/24"). Default blocks only link-local (169.254.x.x) and loopback ranges. Internal network ranges (10.x, 172.16-31.x, 192.168.x) are allowed by default.
- `S3_OPERATION_TIMEOUT_SECONDS`: Prevents indefinite blocking on slow S3 operations.
- `RUSTFS_PRESIGNED_TTL_SECONDS`: Controls how long uploaded file URLs remain valid.

**Note:** `RUSTFS_PUBLIC_URL` is required for tools that generate presigned URLs (rustfs_storage, canvas_diagram). The server uses `RUSTFS_ENDPOINT` for internal communication and rewrites URLs to `RUSTFS_PUBLIC_URL` before returning them to external agents.

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

- [ARCHITECTURE.md](ARCHITECTURE.md) - System architecture and components
- [DEVELOPMENT.md](DEVELOPMENT.md) - Building and testing
- [SECURITY.md](SECURITY.md) - Security hardening and mitigations
- [LOGGING.md](LOGGING.md) - HTTP request logging
- [PRODUCTION.md](PRODUCTION.md) - Production deployment and status
