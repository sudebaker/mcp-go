# MCP Orchestrator API Reference

This document describes the HTTP API and MCP protocol interface for the MCP Orchestrator server.

## Server Overview

| Property | Value |
|----------|-------|
| Protocol | MCP (Model Context Protocol) |
| Transport | Streamable HTTP |
| Default Port | 8080 |
| Bind Host | `127.0.0.1` (override with `MCP_BIND_HOST`; `0.0.0.0` exposes it on all interfaces) |

---

## Authentication

`MCP_AUTH_MODE` is the single switch that controls authentication:

| Value | Behaviour |
|-------|-----------|
| `required` (default when unset) | `/mcp`, `/sse`, `/message` and `/upload` require `Authorization: Bearer <token>` |
| `off` | No authentication. Only for isolated local development |
| anything else | The server refuses to start |

Tokens are **never** stored in a config file. `configs/config.yaml` declares who exists:

```yaml
auth:
  keys:
    - user_id: admin
      key_env: MCP_AUTH_KEY
```

The variable name is arbitrary; the invariant is that every `key_env` is unique and is actually set
in the container. With a single entry it is `MCP_AUTH_KEY`, the name
`deployments/.env.example` ships and `deployments/docker-compose.yml` forwards.

The token value is read at startup from the named environment variable, and the server keeps only
`sha256(token) → user_id`. Generate one with `openssl rand -hex 32` (8–256 chars of
`[A-Za-z0-9._-]`).

**Fail-closed at startup:** an unknown mode, an empty `auth.keys` list in `required` mode, an unset
or empty `key_env`, a malformed token, a duplicate `user_id` or two entries sharing a token all
abort the process. Nothing is skipped.

**Response codes:**

| Situation | Response |
|-----------|----------|
| Authentication disabled (`off`) | Request continues without an identity |
| No `Authorization` header / wrong scheme / malformed token | `401` |
| `/upload` with an `X-Session-ID` that is unknown or belongs to another identity | `403` |
| Token not in the keyring | `401` |
| `required` with no usable keys (misconfiguration) | `503` |

Unauthenticated endpoints: `GET /health`, `GET /health/detailed`, `GET /metrics`, `GET /files/...`
and `GET /internal/resource/{token}` (token-addressed, reachable only on the internal Docker
network). Full contract and rotation procedure: [AUTH.md](AUTH.md).

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

**Status codes:**

| Code | When |
|------|------|
| `200` | `healthy` or `degraded` (degraded still serves traffic) |
| `503` | `unhealthy` — a check reports that the process cannot serve |

A `503` is what the container `HEALTHCHECK` reads as "replace me", so only
checks that mean exactly that are allowed to report `unhealthy`. The `memory`
check never does: it reports memory usage as a fraction of the detected memory
budget (container cgroup limit, else `GOMEMLIMIT`) and caps at `degraded`.
Usage is the kernel's own view — `memory.current` of the process's cgroup on v2,
`memory.usage_in_bytes` on v1 — so runtime metadata, goroutine stacks outside the
heap, cgo allocations and mmaps all count. When no cgroup usage file is readable
the check falls back to the Go heap (`HeapAlloc`). The message names the source
it scored (`cgroup` or `heap`).

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

Memory-related series (alert on the ratio, not on `/health`):

| Metric | Meaning |
|--------|---------|
| `mcp_health_heap_alloc_bytes` | Current Go heap allocation |
| `mcp_health_memory_budget_bytes` | Detected budget: container cgroup limit, else `GOMEMLIMIT`, else `0` |
| `mcp_health_memory_current_bytes` | Memory charged to the process's cgroup (`memory.current` on v2, `memory.usage_in_bytes` on v1); `0` when unavailable |
| `mcp_health_memory_current_ratio` | Cgroup memory usage as a fraction of the budget; `0` when either side is unknown |
| `mcp_health_memory_used_ratio` | `HeapAlloc` as a fraction of the budget, kept for heap-only dashboards; `0` when unknown |

`mcp_health_memory_current_ratio` is the one to alert on: it is what the kernel
compares with `memory.max` before the OOM killer fires. It includes the cgroup's
page cache, which the kernel reclaims under pressure, so a short-lived spike is
not by itself a leak. `mcp_health_memory_used_ratio` only sees the Go heap and
under-reports the container's real footprint.

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

Initializes the MCP session. Requires `Authorization: Bearer <token>` (see
[Authentication](#authentication)); the token determines the session identity.

**User Identity:** the server resolves the bearer token to a `user_id` through the keyring
declared in `configs/config.yaml` (`auth.keys[].key_env`). The `capabilities.experimental.user_id`
field of the request is **client-asserted and ignored** — it is only logged, so a client cannot
choose the account its tools run against:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "protocolVersion": "2025-03-26",
    "capabilities": {},
    "clientInfo": {
      "name": "my-mcp-client",
      "version": "1.0.0"
    }
  }
}
```

With `Authorization: Bearer <token>` (the token is bound to `user_id=admin`):

```
POST /mcp
Content-Type: application/json
Authorization: Bearer <token>
Mcp-Session-Id: <returned by the initialize response>
```

**User Isolation:**
- The `user_id` resolved from the token is stored in a session store mapped to the session ID
- For KB tools (`kb_ingest`, `kb_search`), the `user_id` is injected into the subprocess context
- All KB queries are filtered by `user_id`, ensuring complete data isolation
- A session that initializes without an authenticated token is left unbound (no `user_id`)
- Session is cleaned up on disconnect via `OnUnregisterSession` hook

### ping

Simple ping/pong for connectivity check.

### tools/list

Lists all available tools registered with the MCP server.

### tools/call

Executes a specific tool with provided arguments.

---

## Available Tools

### File arguments (`__files__`)

Tools that take documents (`batch_summarize`, `document_classifier`,
`regulation_diff`, `config_auditor`, ...) receive them as `__files__`, an array
of objects:

```json
{"__files__": [{"url": "file:///data/uploads/contrato.pdf", "name": "contrato.pdf"}]}
```

Each `url` may be:

| Form | Use |
|------|-----|
| `file:///data/...` | File already present inside the container under `/data` (a previous tool output, a mounted file, or one an operator placed there). Resolved locally, no network call. Paths outside `/data`, and symlinks that resolve outside it, are rejected. |
| `res://<token>` | Opaque handle returned by `POST /upload` and `resources/list`. Multi-use until expiry (1-hour TTL); fetched from the internal `/internal/resource/{token}` endpoint. |
| `http(s)://...` | Public download link or a **complete** S3 presigned URL. Private, loopback, link-local and cloud-metadata addresses are blocked by the SSRF guard, so a presigned URL pointing at a LAN address must be fetched through `res://` (or `file://` if the file is inside `/data`) instead. |

A presigned URL only works while its `X-Amz-Signature` query parameter is
intact: copy the whole URL, never a shortened form.

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
| image_path | string | Yes | Image to analyze: `res://` upload token, HTTP/HTTPS URL, or a local path such as `/data/uploads/image.jpg` |
| task | string | Yes | `ocr`, `describe`, `extract_entities`, `answer` |
| question | string | Conditional | Required for `answer` task |
| provider | string | No | `ollama` (default), `vision`, `openrouter`, `openai`, `anthropic`, `deepseek`, or a full URL |
| model | string | No | Vision model name (defaults from context/env, e.g. `llava`) |

A `res://` token is resolved through `GET /internal/resource/{token}` on each
call that carries it; the token is **multi-use** and stays valid until it
expires (**1-hour TTL** — see [Resource resolution flow](#resource-resolution-flow)).
In-container clients resolve it with `Authorization: Bearer <SANDBOX_API_TOKEN>`;
the endpoint refuses every request without that header (fail-closed Bearer
guard, sidecar phase 2). If resolution fails the tool answers `success: false`
with `RESOURCE_ERROR` instead of a silent empty analysis.

---

### kb_ingest

Stores content in the knowledge base (PostgreSQL + pgvector). **User isolation:** Each user can only access their own documents. User identity is established by the bearer token of the MCP session (see [Authentication](#authentication)); the `capabilities.experimental.user_id` field is ignored.

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
| __files__ | array | Yes | Exactly 2 entries: `[0]` = old version, `[1]` = new version. Each is `{url: "res://<token>", http(s) URL (SSRF-validated) or file:///data/... (file inside /data), name: "file.ext"}`; a plain uri string is also tolerated |
| focus | string | No | Optional focus area |
| output_format | string | No | `markdown` (default) or `structured` |

Server-side `res://` tokens are multi-use with a 1-hour TTL: the same uri may
be passed on every call until the token expires (a repeated attachment — the
same uri in both slots — still resolves once internally). An expired or unknown
token makes the tool fail with an error asking for a re-upload
(see [Resource resolution flow](#resource-resolution-flow)).

---

### config_auditor

Audits configuration files for security problems. It reads the file and derives
every finding from its content — no LLM call. Formats are picked from the
filename extension: `.yaml`/`.yml`, `.json`, `.toml`, `.ini`/`.cfg`/`.conf`,
`.env`; anything else is scanned as plain text (line rules only, structural rules
skipped). A YAML/JSON/TOML/INI file that fails to parse is still audited line by
line and the failure is reported in `notes`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| __files__ | array | Yes | `{url, name}` entries. `url` is a `res://<token>` from `POST /upload`, an http(s) URL (SSRF-validated) or `file:///data/...` (file inside `/data`); `name` must carry the extension |
| rules | array | No | `secrets`, `empty_required`, `dangerous_ports`, `debug_mode`, `hardcoded_ips` (default: all) |
| severity_filter | string | No | Minimum severity reported: `all` (default), `critical`, `high` (critical + high), `medium`. It only trims the reported list — the score always reflects the whole audit |

**Rules**

| Rule | Severity | Flags |
|------|----------|-------|
| `secrets` | critical | `password`/`secret`/`token`/`api_key`/`private_key`/`client_secret`… with a literal value. Environment references (`${VAR}`), empty strings, `<placeholders>` and commented-out lines are not reported; the value is redacted in the output |
| `dangerous_ports` | high | Ports 22, 2375/2376, 3389, 6379, 27017, 9200, 11211 bound or dialled |
| `debug_mode` | medium | `debug: true` / `DEBUG=1`, from the parsed tree or a line match |
| `empty_required` | medium | Keys declared with no value (`timeout:` in YAML, `TIMEOUT=` in `.env`) |
| `hardcoded_ips` | medium | Literal IPv4 addresses; `0.0.0.0`, `127.0.0.1` and `255.255.255.255` are treated as bind targets, not findings |

**Output:** `structured_content.findings` (each with `file`, `rule`, `severity`,
`field`, `line` — `null` for structural findings —, `current_value`,
`description`, `recommendation`), plus `score`, `summary`, `total_findings`,
`findings_by_severity`, per-file reports in `files`, and `errors` when a file
could not be read. `score` starts at 100 and subtracts 25 per `critical`, 15 per
`high` and 5 per `medium`, floored at 0.

Example (abridged, from a real `POST /upload` → `res://` audit):

```json
{
  "findings": [
    {
      "file": "ca-bad-config.yaml",
      "rule": "secrets",
      "severity": "critical",
      "field": "password",
      "line": 8,
      "current_value": "<redacted, 12 chars>"
    },
    {
      "file": "ca-bad-config.yaml",
      "rule": "empty_required",
      "severity": "medium",
      "field": "database.timeout",
      "line": null,
      "current_value": ""
    }
  ],
  "score": 40,
  "summary": "Audited 1 file(s). 6 finding(s): 1 critical, 1 high, 4 medium. security score 40/100.",
  "total_findings": 6,
  "findings_by_severity": {"critical": 1, "high": 1, "medium": 4}
}
```

A `res://` token is multi-use: it stays valid until it expires (**1-hour TTL**),
so repeated audits over the same upload work without re-uploading (the same is
true for every tool that resolves `res://`).

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
| save_path | string | No | Optional custom path for .canvas file. Must be inside the sandbox output dir (`OUTPUT_DIR`, e.g. `/tmp/output`); when RustFS is unavailable the diagram is saved there and the path returned in `structured_content.local_path` |

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

The presigned URL is returned in `structured_content.presigned_url` and, in
full, in the human-readable `text` of the response — never shortened. S3
authenticates the URL through its `X-Amz-*` query parameters, so any
truncation drops `X-Amz-Signature` and storage answers `403`.

---

### POST /upload

Uploads a file to the server. The bytes go to RustFS (bucket `users`, under a
per-user prefix derived from the authenticated identity), not to `/data/uploads`,
and the response carries a `res://` token that tools resolve server-side.

**Request:**
```
POST /upload
Content-Type: multipart/form-data
Authorization: Bearer ***          (required when MCP_AUTH_MODE=required)
X-Session-ID: <session id>             (required) - decides the namespace; it must belong to the token identity

FormData:
  - file: (required) binary file
```

The upload lands in the namespace of the MCP session named by `X-Session-ID`. The server resolves
that session through the same store the KB uses and rejects the request with `403` when the session
belongs to another identity, so a valid token cannot write into someone else's namespace. A client
must therefore initialize with the token it uploads with (`MCP_UPLOAD_API_KEY` initializes a session
of its own, `user_id: legacy-upload`).

**Response (200 OK):**
```json
{
  "success": true,
  "uri": "res://4d1f9a2b8c3e5d70a9b1c2d3e4f50617",
  "sha256": "9a0364b9e99bb480dd25e1f0284c8c5a4c1f0a1b2c3d4e5f60718293a4b5c6d7",
  "size": 81087,
  "content_type": "image/jpeg",
  "name": "screenshot.jpg"
}
```

The `uri` is **not** a storage path and not an HTTP URL: it is an opaque download
token for the uploaded file — multi-use, valid for **1 hour** (3600 s) after
issuance. The token lifetime and the uploaded file's `ttl` form field are
separate things today (the form field governs the stored file). See
[Resource resolution flow](#resource-resolution-flow).

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
- Content-Type validation against whitelist (declared MIME must also match the
  file's magic bytes: `audio/wav` is detected as `application/octet-stream` and
  therefore answers `415` — send audio to `transcribe` via its `audio_base64`
  argument instead of uploading the file)
- Size limit enforcement
- Unique filename generation (UUID-based)

**Response (401 - missing or invalid identity):**
```
Missing X-Session-ID            (plaintext, when the header is absent)
```
With `MCP_AUTH_MODE=required` the bearer token is validated by the auth
middleware before the handler runs. A valid bearer token is **not enough**:
the `X-Session-ID` header (the `Mcp-Session-Id` of a previous `initialize` over
`/mcp`) is mandatory too.

**Response (403 - session of another identity):**
```json
{"success": false, "error": "X-Session-ID does not belong to the authenticated user"}
```

### Resource resolution flow

`res://` uris are **upload tokens**, issued only by `POST /upload`. They are
multi-use (repeated reads over the same upload) and expire **1 hour** (3600 s)
after issuance (`internal/resources/token.go`, `internal/resources/manager.go`
`tokenTTL`, anchored by tests). The GET is **not** bound to the owner yet: it
only requires the shared `SANDBOX_API_TOKEN` Bearer secret and a valid, live
token — caller binding (only the upload's owner can read) is phase 2. Tools
resolve tokens server-side through `GET /internal/resource/{token}`, an endpoint
reachable only on the internal Docker network (never published) and guarded by
`Authorization: Bearer <SANDBOX_API_TOKEN>`; a request without it gets 401.

The full flow a client must follow:

```
1. POST /mcp  {"method": "initialize", ...}  (Bearer token required)
   → response header Mcp-Session-Id: <session id>
2. POST /upload  (multipart, field "file")
   → MUST carry BOTH headers:
       Authorization: Bearer <MCP_AUTH_KEY>
       X-Session-ID: <session id>
   → 200 {"success": true, "uri": "res://<token>", ...}
   - without X-Session-ID → 401 even with a valid bearer token
   - audio/wav → 415 (magic-byte check); use transcribe's audio_base64 instead
3. Call the tool passing the res:// uri as the file argument
   (e.g. `__files__` entries with `url` in regulation_diff and file-bundle
   tools, `image_path` in analyze_image) — once per upload, then reuse the
   same uri for follow-up questions on the same file.
4. Each call, the tool fetches  GET /internal/resource/<token>  and processes
   bytes. Every in-container client sends `Authorization: Bearer <SANDBOX_API_TOKEN>`;
   a request without the header answers 401. Repeated GETs over the same live
   token each answer 200 with the file bytes.
5. A token older than 1 hour, or an unknown token, answers 401 — re-upload the
   file and use the fresh uri from the new response.
```

> **Note (battery finding, 2026-10):** the two-header requirement from step 2,
> plus the strict token lifecycle of the time (single-use, 5-minute TTL;
> multi-use with a 1-hour TTL since), is why `batch_summarize`,
> `document_classifier` and `regulation_diff` had been kept "non-verifiable"
> in the production battery for weeks: a successful upload alone proves
> nothing, and an invalid or expired token fails with plain
> `HTTP Error 401: Unauthorized` unless the tool surfaces the reason.

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
| LLM_API_URL | http://localhost:11434 | LLM API base URL. With `LLM_API_FORMAT=openai` it must already end in `/v1`, because the code appends `/chat/completions` |
| LLM_MODEL | llama3 | LLM model |
| LLM_API_FORMAT | ollama | Wire format. `ollama` → `{LLM_API_URL}/api/chat` (native); `openai` → `{LLM_API_URL}/chat/completions` |
| LLM_REQUEST_TIMEOUT | 60 | Seconds `canvas_diagram` waits for its own `{LLM_API_URL}/api/generate` call |

The five tools that use an LLM (`batch_summarize`, `data_analysis`, `document_classifier`,
`regulation_diff`, `vision_ocr`) read the answer from `message.content`. On a *thinking* model
(`qwen3.5`, `gpt-oss`, `lfm2.5`, ...) Ollama puts the whole answer in `message.thinking` and
leaves `message.content` empty, which the tools surface as `Could not parse LLM response` while
`/health` and the deploy preflight stay green. `tools/common/retry.py` therefore sends
`think: false` on the native endpoint (Ollama ignores the field on models without that
capability) and, if a model answers only in the reasoning channel, falls back to
`message.thinking` instead of returning an empty string.

`canvas_diagram` does **not** go through `tools/common/retry.py`: it calls `/api/generate`
directly, always in Ollama's native format (it does not read `LLM_API_FORMAT`). It sends
`think: false` too, and reports `structured_content.llm_fallback: true` — plus a note in the
response text — when an LLM was configured but returned no usable DSL, instead of claiming
`used_llm: true` for a map built from the description.

"Usable" is narrower than "non-empty": an answer with no connection (`->` / `→`) is only
accepted when it is a short label (≤ 8 words), so a refusal in prose ("I'm sorry, but I can
only convert…") is discarded and counted as a fallback instead of becoming a canvas of junk
nodes with no edges. A markdown fence around the answer is stripped. Measured against
`qwen3.5:9b` with a 6-line description: 30 s → timeout → description fallback (3 nodes,
0 edges) before, 4 s → LLM DSL (5 nodes, 8 edges) after.

### Database Configuration
| Variable | Default | Description |
|----------|---------|-------------|
| DATABASE_URL | built by the compose from `POSTGRES_*` | PostgreSQL connection (a value in `.env` is overridden) |
| REDIS_URL | redis://localhost:6379/0 | Redis connection for the LLM cache; **not forwarded** by the compose and there is no redis service, so it stays unset in containers |

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
In the compose stack the RustFS credentials are **required** (`${VAR:?}`): the
`rustfsadmin` defaults only apply when the server runs outside it, and leaving
them means every client on the network can reach the bucket.

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
