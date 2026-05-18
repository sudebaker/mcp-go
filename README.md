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

## Adding a New Tool

1. Create `tools/my_tool/main.py` implementing the JSON stdin/stdout protocol.
2. Register the tool in `configs/config.yaml`.
3. Restart the service:

```bash
docker compose restart mcp-server
```

## License

See [LICENSE](LICENSE).