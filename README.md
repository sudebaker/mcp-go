# MCP Orchestrator

A Go-based MCP (Model Context Protocol) server that orchestrates external tools — primarily Python — for data analysis, OCR, report generation, knowledge base management, and more. Built for air-gapped and restricted-network deployments.

[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![MCP](https://img.shields.io/badge/MCP-Compatible-green?style=flat)](https://modelcontextprotocol.io/)

## Features

- Go-based MCP server with primary endpoint `POST /mcp`
- Legacy SSE transport for backward compatibility (`/sse` and `/message`)
- Configurable tool catalog via `configs/config.yaml`
- PostgreSQL + pgvector knowledge base integration
- RustFS/S3-compatible file storage integration
- Designed for air-gapped and restricted-network deployments

## Requirements

- Go 1.23+
- Docker and Docker Compose
- Optional: Ollama on the same Docker network

## Quick Start

```bash
cd deployments
docker compose up -d

docker compose ps
```

Services defined in `deployments/docker-compose.yml`:

| Service | URL | Purpose |
|---------|-----|---------|
| `mcp-server` | `http://localhost:8080` | MCP server |
| `postgres` | `localhost:5432` | KB storage (pgvector) |
| `rustfs` | `http://localhost:9000` | S3-compatible storage |
| `searxng` | `http://localhost:8080` (internal) | Private web search |
| `browserless` | `http://localhost:3000` | Headless browser (JS rendering) |
| `whisper` | `http://localhost:8000` | Audio transcription |

### Resource Limits

Each service in `docker-compose.yml` has CPU and memory limits to prevent any single container from consuming all host resources:

| Service | CPU Limit | Memory Limit | CPU Reservation | Memory Reservation |
|---------|-----------|--------------|-----------------|--------------------|
| `mcp-server` | 2 CPUs | 2 GB | 0.5 CPUs | 1 GB |
| `postgres` | 1 CPU | 1 GB | 0.5 CPUs | 512 MB |
| `whisper` | 2 CPUs | 2 GB | 0.5 CPUs | 512 MB |
| `browserless` | 1 CPU | 1 GB | 0.25 CPUs | 512 MB |
| `searxng` | 1 CPU | 512 MB | 0.25 CPUs | 128 MB |
| `rustfs` | 1 CPU | 512 MB | 0.25 CPUs | 128 MB |

Limits cap the maximum resources a container can use; reservations guarantee a minimum to the Docker scheduler (effective in Swarm mode). These values balance headroom for the Go MCP server and CPU-intensive Whisper with tighter bounds on lighter services like SearXNG and RustFS.

## MCP Endpoints

| Endpoint | Protocol | Description |
|----------|----------|-------------|
| `/mcp` | Streamable HTTP | Primary endpoint (recommended) |
| `/sse` | SSE | Legacy client compatibility |
| `/message` | SSE Message | Message sender for SSE transport |
| `/health` | HTTP | Server healthcheck |

Supported MCP methods: `initialize`, `ping`, `tools/list`, `tools/call`.

## Included Tools

Defined in `configs/config.yaml`:

> **Note:** The default configuration has tool descriptions in **Spanish**. To switch to **English** descriptions, rename the included alternate config:
> ```bash
> mv configs/config-en.yaml configs/config.yaml
> docker compose restart mcp-server
> ```

| Category | Tools |
|----------|-------|
| Core | `echo` |
| Analysis & Generation | `analyze_data`, `analyze_image`, `generate_report` |
| Knowledge Base | `kb_ingest`, `kb_search` |
| Document Processing | `batch_summarize`, `regulation_diff`, `config_auditor`, `document_classifier` |
| Utilities | `weather_forecast`, `web_scraper`, `rss_reader`, `canvas_diagram`, `rustfs_storage`, `transcribe`, `searxng_search`, `browser_scraper` |

## Vision Tool: Provider Override

The `analyze_image` tool supports optional `provider` and `model` parameters to route vision requests to specific LLM backends. This enables private vision analysis without sending images to third-party APIs.

### Supported Providers

| Provider | Routes to | Default Model | API Key Required |
|----------|-----------|---------------|------------------|
| `ollama` / `local` | `LLM_API_URL` env var (default `http://localhost:11434`) | `LLM_MODEL` or `llava` | No |
| `remote-ollama` | `REMOTE_OLLAMA_URL` env var | `qwen3.5:9b` (multimodal) | No |
| `openrouter` | `https://openrouter.ai/api/v1` | `google/gemini-2.0-flash-001` | `OPENROUTER_API_KEY` |
| `openai` | `https://api.openai.com/v1` | `gpt-4o-mini` | `OPENAI_API_KEY` |
| Custom URL | Used directly as-is | `LLM_MODEL` or `llava` | Varies |

### Configuration

Set the `REMOTE_OLLAMA_URL` environment variable in your `.env` file (gitignored) to point to a remote Ollama instance (VPN, Tailscale, LAN, etc.):

```bash
# Example: Ollama running on a remote machine via VPN/Tailscale
REMOTE_OLLAMA_URL=http://<your-remote-ollama-host>:11434
```

### Usage Examples

```json
// Route to remote Ollama (private, no third-party)
analyze_image(image_path="/data/tmp/photo.jpg", task="describe", provider="remote-ollama")

// Specify model explicitly
analyze_image(image_path="/data/tmp/photo.jpg", task="ocr", provider="remote-ollama", model="gemma4:34b")

// Use OpenRouter (cloud-based)
analyze_image(image_path="/data/tmp/photo.jpg", task="answer", provider="openrouter", model="google/gemini-2.0-flash-001")
```

## Testing

```bash
# Go unit + integration tests
go test ./...

# Quick environment tests
./tests/test_quick.sh

# Integration test suite
./tests/test_excel_analysis.sh
```

## Docker Sandbox

The Docker sandbox image (`mcp-python-sandbox:latest`) is used by `data_analysis` to run LLM-generated Pandas code in isolation.

```bash
# Build from the repository root
docker build -f tools/data_analysis/sandbox.Dockerfile -t mcp-python-sandbox:latest .
```

## Toolset Filtering

The server can expose different subsets of tools without code changes, using **toolsets** defined in `configs/toolsets.yaml`.

### Available Toolsets

| Toolset | Tools | Use Case |
|---------|-------|----------|
| `default` | 14 general-purpose tools (echo, weather, kb, web, etc.) | End users, general assistance |
| `development` | 10 dev tools + core (opencode_context, codebase_scan, etc.) | Developers, coding agents |
| `ocu-investigacion` | 17 tools (6 forenses + kb, web scraping, OCR, transcription, reports, storage) | Criminal investigation |
| *(unset)* | All available tools | Backward compatible, everything exposed |

### Usage

Set the `MCP_TOOLSET` environment variable in your `.env` file:

```bash
# .env — only expose general-purpose tools
MCP_TOOLSET=default

# .env — expose development tools
MCP_TOOLSET=development

# .env — combine multiple toolsets
MCP_TOOLSET=default,development

# .env — forensic investigation toolset
MCP_TOOLSET=ocu-investigacion

# .env — all tools + forensics
MCP_TOOLSET=default,development,ocu-investigacion

# .env — omit to expose all tools (default behavior)
# (no MCP_TOOLSET set)
```

Or with Docker directly:

```bash
# General toolset
docker run -e MCP_TOOLSET=default -p 8080:8080 sudebaker/mcp-go

# Development toolset
docker run -e MCP_TOOLSET=development -p 8080:8080 sudebaker/mcp-go

# All tools (backward compatible)
docker run -p 8080:8080 sudebaker/mcp-go
```

### How It Works

1. The server discovers all available tools from `tools/<name>/tool.yaml`.
2. If `MCP_TOOLSET` is set, it loads `configs/toolsets.yaml` and filters tools by the active toolset(s).
3. Multiple toolsets can be combined: `MCP_TOOLSET=default,development` produces the union of both.
4. The client (Hermes, Claude Desktop, etc.) receives only the filtered tools via `tools/list`.

The client **does not need to know** about toolsets. The filtering happens entirely on the server side.

### Adding Custom Toolsets

Add a new entry to `configs/toolsets.yaml`:

```yaml
  my-team:
    description: "Custom toolset for my team"
    tools:
      - echo
      - datetime
      - kb_search
```

Then run with:

```bash
MCP_TOOLSET=my-team docker compose up -d mcp-server
```

## Documentation

| Document | Description |
|----------|-------------|
| `QUICKSTART.md` | Quick start and verification guide |
| `USAGE.md` | Functional usage per tool |
| `DOCUMENTATION_INDEX.md` | Documentation map |
| `docs/DEVELOPMENT.md` | Technical architecture and development guide |
| `docs/API.md` | API reference |
| `AGENTS.md` | Build/test commands and conventions |

## Project Structure

```text
mcp-go/
├── cmd/server/         # Entry point
├── internal/           # Go core (config, executor, transport, etc.)
├── tools/              # Python tools
├── templates/          # HTML/CSS report templates
├── configs/            # YAML configuration
├── deployments/        # Dockerfile + compose
└── tests/              # Go, Python, and shell tests
```

## Tool Management

Tools are defined in `configs/config.yaml` under the `tools:` key. Each tool entry specifies the command, arguments, timeout, and input schema.

### Toolsets

Toolset definitions in `configs/toolsets.yaml` are curated lists of tools for specific use cases. They serve as reference when customizing your deployment.

| Toolset | Tools | Purpose |
|---------|-------|---------|
| `default` | 15 tools | General productivity (echo, datetime, PDF reports, data analysis, KB, web) |
| `development` | 11 tools | Software development (core + codebase_scan, docs) |
| `ocu-investigacion` | 17 tools | Criminal investigation (6 forenses + kb, web scraping, OCR, transcription, reports, storage) |

To use a toolset, set the `MCP_TOOLSET` environment variable (see [Toolset Filtering](#toolset-filtering)).

### Adding a Tool

1. Create `tools/my_tool/main.py` implementing the JSON stdin/stdout protocol.
2. Register the tool in `configs/config.yaml` under the `tools:` section.
3. Restart:

```bash
docker compose restart mcp-server
```

### Manifest Discovery

Tools are auto-discovered from `tools/<tool-name>/`. Each subdirectory must contain a `tool.yaml` manifest:

```
tools/
├── echo/
│   ├── main.py
│   └── tool.yaml
├── codebase_scan/
│   ├── main.py
│   └── tool.yaml
└── ...
```

Discovery is enabled by default via `TOOLS_DISCOVERY=manifest`.

| Variable | Value | Description |
|----------|-------|-------------|
| `TOOLS_DISCOVERY` | `manifest` | Enables manifest-based discovery |
| `TOOLS_DIR` | `/app/tools` | Directory to scan for tool manifests |
| `TOOLS_APPEND` | `true` | Keep config tools and append discovered ones (`false` = discovered override config) |

### Adding a Custom Toolset

Add a new entry to `configs/toolsets.yaml`:

```yaml
  my_stack:
    description: "My custom tool stack"
    tools:
      - echo
      - datetime
      - kb_search
```

Then run with `MCP_TOOLSET=my_stack`. Multiple toolsets can be combined: `MCP_TOOLSET=my_stack,default`.

For the **OCu (criminal investigation) toolset**, see the full deployment guide including the required database stack (Memgraph, OpenSearch):

- [OCu Investigación — Toolset y Stack de Bases de Datos](docs/OCU_INVESTIGACION.md)
- Stack de bases de datos: `deployments/infra/ocu-investigacion/docker-compose.yml`

### Disabling a Tool

Remove its entry from `tools/` or exclude it from the active toolset in `configs/toolsets.yaml`.

## License

See [LICENSE](LICENSE).