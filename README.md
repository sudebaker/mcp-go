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
| Utilities | `weather_forecast`, `web_scraper`, `rss_reader`, `canvas_diagram`, `rustfs_storage`, `server_status`, `transcribe`, `searxng_search`, `browser_scraper` |

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

## Toolkit Filtering

The server can expose different subsets of tools without code changes, using **toolkits** defined in `configs/toolkits/`.

### Available Toolkits

| Toolkit | Tools | Use Case |
|---------|-------|----------|
| `default` | 14 general-purpose tools (echo, weather, kb, web, etc.) | End users, general assistance |
| `development` | 10 dev tools + core (opencode_context, codebase_scan, git_inspector, etc.) | Developers, coding agents |
| *(unset)* | All available tools | Backward compatible, everything exposed |

### Usage

Set the `MCP_TOOLKIT` environment variable in your `.env` file:

```bash
# .env — only expose general-purpose tools
MCP_TOOLKIT=default

# .env — expose development tools
MCP_TOOLKIT=development

# .env — omit to expose all tools (default behavior)
# (no MCP_TOOLKIT set)
```

Or with Docker directly:

```bash
# General toolkit
docker run -e MCP_TOOLKIT=default -p 8080:8080 sudebaker/mcp-go

# Development toolkit
docker run -e MCP_TOOLKIT=development -p 8080:8080 sudebaker/mcp-go

# All tools (backward compatible)
docker run -p 8080:8080 sudebaker/mcp-go
```

### How It Works

1. The server loads all available tools from `configs/config.yaml` and auto-discovery.
2. If `MCP_TOOLKIT` is set, it loads the corresponding toolkit YAML from `configs/toolkits/<name>.yaml`.
3. The server filters the loaded tools to only those listed in the toolkit.
4. The client (Hermes, Claude Desktop, etc.) receives only the filtered tools via `tools/list`.

The client **does not need to know** about toolkits. The filtering happens entirely on the server side.

### Adding Custom Toolkits

Create a new file in `configs/toolkits/`:

```yaml
# configs/toolkits/my-team.yaml
name: "my-team"
description: "Custom toolkit for my team"
tools:
  - echo
  - datetime
  - kb_search
```

Then run with:

```bash
MCP_TOOLKIT=my-team docker compose up -d mcp-server
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

### Toolkits

Toolkit files under `configs/toolkits/` are curated lists of tools for specific use cases. They serve as reference when customizing your deployment.

| Toolkit | File | Purpose |
|---------|------|---------|
| `default` | `configs/toolkits/default.yaml` | General productivity (PDF reports, data analysis, KB, web) |
| `development` | `configs/toolkits/development.yaml` | Software development (code scan, test runner, git, docs) |

To use a toolkit as your active toolset, replace the `tools:` section in `configs/config.yaml` with the tools from the toolkit file, then restart:

```bash
docker compose restart mcp-server
```

### Adding a Tool

1. Create `tools/my_tool/main.py` implementing the JSON stdin/stdout protocol.
2. Register the tool in `configs/config.yaml` under the `tools:` section.
3. Restart:

```bash
docker compose restart mcp-server
```

### Manifest Discovery (Dev Tools)

Tools can also be auto-discovered from a directory of tool manifests. Each subdirectory must contain a `tool.yaml` manifest:

```
tools/dev-tools/
├── test_runner/
│   ├── main.py
│   └── tool.yaml
└── git_inspector/
    ├── main.py
    └── tool.yaml
```

Enable discovery by setting environment variables:

| Variable | Value | Description |
|----------|-------|-------------|
| `TOOLS_DISCOVERY` | `manifest` | Enables manifest-based discovery |
| `TOOLS_DIR` | `/app/tools/dev-tools` | Directory to scan for tool manifests |
| `TOOLS_APPEND` | `true` | Keep config tools and append discovered ones (`false` = discovered override config) |

Example for development toolkit:

```yaml
# docker-compose.yml
environment:
  TOOLS_DISCOVERY: "manifest"
  TOOLS_DIR: "/app/tools/dev-tools"
  TOOLS_APPEND: "true"
```

### Building a Custom Toolkit

1. List the tool names you want in a new YAML file under `configs/toolkits/`:

```yaml
# configs/toolkits/my_stack.yaml
name: "my_stack"
description: "My custom tool stack"
tools:
  - echo
  - datetime
  - test_runner
  - changelog_generator
```

2. Copy the relevant tool entries from `configs/toolkits/` into `configs/config.yaml`.

### Disabling a Tool

Remove its entry from the `tools:` section in `configs/config.yaml` and restart.

## License

See [LICENSE](LICENSE).