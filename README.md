# MCP Orchestrator

A Go-based MCP (Model Context Protocol) server that orchestrates external tools — primarily Python — for data analysis, OCR, report generation, knowledge base management, and more. Built for air-gapped and restricted-network deployments.

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://go.dev/)
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

- Go 1.26+ (see `go.mod`) — only to build from source; the Docker image compiles it
- Docker Engine with the Compose v2 plugin (`docker compose`, not `docker-compose`)
- Disk: the `whisper` image is ~1.9 GB and is built locally; `crawl4ai` is ~4 GB of RAM limit
- Optional: an Ollama (or OpenAI-compatible) endpoint reachable from the `mcp-external` network

## Quick Start

```bash
git clone https://github.com/sudebaker/mcp-go.git
cd mcp-go/deployments

cp .env.example .env
# edit .env: BASE_URL, RUSTFS_PUBLIC_URL and every `change_me` secret
#   (openssl rand -hex 32; MCP_UPLOAD_API_KEY must differ from MCP_AUTH_KEY)

./scripts/preflight-env.sh      # names every missing/placeholder value, starts nothing
docker compose build whisper    # only step that needs internet: model goes into the image
docker compose up -d
curl -s http://127.0.0.1:8080/health
```

`docker compose up -d` **fails immediately** if `.env` is missing or a required
variable is empty: the compose file declares them `${VAR:?}`. Copying
`.env.example` alone is not enough — the `change_me` secrets are rejected at
server startup by design, so the container would restart-loop. The full
variable contract is commented in [`deployments/.env.example`](deployments/.env.example).

`whisper` is built from `deployments/Dockerfile.whisper` and downloads its model
during the build (no network at runtime). `sandbox-sidecar` needs
`/var/run/docker.sock`, and `analyze_data` only executes code after you build
`mcp-python-sandbox:latest` and set `SANDBOX_API_URL` (see [Docker Sandbox](#docker-sandbox)).

Services defined in `deployments/docker-compose.yml`:

| Service | Reachable at | Purpose |
|---------|--------------|---------|
| `mcp-server` | `http://127.0.0.1:8080` (published) | MCP server |
| `postgres` | `postgres:5432` (internal only) | KB storage (pgvector) |
| `rustfs` | `http://127.0.0.1:9000` (published) | S3-compatible storage |
| `searxng` | `searxng:8080` (internal only) | Private web search |
| `crawl4ai` | `http://127.0.0.1:11235` (published) | Web scraping (LLM-optimized markdown) |
| `whisper` | `whisper:8000` (internal only) | Audio transcription |
| `sandbox-sidecar` | `sandbox-sidecar:8080` (internal only) | Isolated code execution; only service with `docker.sock` |
| `memgraph` / `opensearch` | profile `ocu-investigacion` | Graph + full-text search for the forensic toolset |

`*_PUBLISH_HOST` in `.env` controls which host interface a published port binds
to; the default is `127.0.0.1`.

### Resource Limits

Each service in `docker-compose.yml` has CPU and memory limits to prevent any single container from consuming all host resources:

| Service | CPU Limit | Memory Limit | CPU Reservation | Memory Reservation |
|---------|-----------|--------------|-----------------|--------------------|
| `mcp-server` | 2 CPUs | 3 GB | 0.5 CPUs | 1 GB |
| `postgres` | 1 CPU | 1 GB | 0.5 CPUs | 512 MB |
| `whisper` | 2 CPUs | 2 GB | 0.5 CPUs | 512 MB |
| `crawl4ai` | 2 CPUs | 4 GB | 0.5 CPUs | 1 GB |
| `searxng` | 1 CPU | 512 MB | 0.25 CPUs | 128 MB |
| `rustfs` | 1 CPU | 512 MB | 0.25 CPUs | 128 MB |
| `sandbox-sidecar` | 1 CPU | 512 MB | 0.25 CPUs | 128 MB |

The declared limits add up to ~11.5 GB for the base stack. They are ceilings,
not reservations, so a smaller host can run it — but `crawl4ai` (4 GB) and
`whisper` (2 GB) are the heavy ones. The `ocu-investigacion` services declare no
memory limit; OpenSearch runs with a 1 GB heap (`OPENSEARCH_JAVA_OPTS`).
`mcp-server` gets 3 GB because the Go process idles at ~1.1 GB and each Python
tool can add ~600 MB in the same cgroup.

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

> **Note:** The default configuration has tool descriptions in **Spanish**. To switch to **English** descriptions:
> ```bash
> cp configs/config-en.yaml configs/config.yaml   # keep the original in git
> docker compose restart mcp-server
> ```
> `config-en.yaml` ships with `allowed_origins` commented out: set it to your UI
> origin or the `/admin/*` surface will be refused (404) even with `ADMIN_API_KEY`.

| Category | Tools |
|----------|-------|
| Core | `echo`, `datetime` |
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
| `vision` | `LLM_VISION_API_URL` env var | `qwen3.5:9b` (multimodal) | No |
| `openrouter` | `https://openrouter.ai/api/v1` | `google/gemini-2.0-flash-001` | `OPENROUTER_API_KEY` |
| `openai` | `https://api.openai.com/v1` | `gpt-4o-mini` | `OPENAI_API_KEY` |
| Custom URL | Used directly as-is | `LLM_MODEL` or `llava` | Varies |

Only `OPENROUTER_API_KEY` and `OLLAMA_API_KEY` are forwarded into the container
by `deployments/docker-compose.yml`. The other provider keys (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, ...) are read by `configs/config.yaml` but **not** injected
by the compose: add them to `mcp-server.environment` before using those
providers.

Local paths must live under `/data/input` or `/data/uploads`; anything else is
rejected by the resource resolver.

### Configuration

Set the `LLM_VISION_API_URL` environment variable in your `.env` file to point to a remote inference endpoint:

```bash
LLM_VISION_API_URL=http://<your-vision-host>:11434/v1
```

### Usage Examples

```json
// Route to remote vision endpoint (private, no third-party)
analyze_image(image_path="/data/input/photo.jpg", task="describe", provider="vision")

// Specify model explicitly
analyze_image(image_path="/data/input/photo.jpg", task="ocr", provider="vision", model="gemma4:34b")

// Use OpenRouter (cloud-based)
analyze_image(image_path="/data/input/photo.jpg", task="answer", provider="openrouter", model="google/gemini-2.0-flash-001")
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

`data_analysis` runs LLM-generated Pandas code inside a hardened container: non-root user,
read-only root filesystem, no network, and CPU/memory/PID limits.

There are two execution backends, and the shipped compose uses the second one:

1. **Docker from the orchestrator (legacy).** The tool can build and run
   `mcp-python-sandbox:latest` through `docker.from_env()`, but `mcp-server` does not
   mount `/var/run/docker.sock` — that would be root-equivalent — so this path is not
   available in the compose stack.
2. **`sandbox-sidecar` (shipped).** The only service with `docker.sock`; it exposes
   `POST /v1/execute` on the internal network and the tool calls it when
   `SANDBOX_API_URL` is set.

Build the image once on the host; the build context is the **repository root**, not
`tools/data_analysis/`:

```bash
docker build -f tools/data_analysis/sandbox.Dockerfile -t mcp-python-sandbox:latest .
```

Then enable the backend in `.env` and recreate both containers:

```bash
SANDBOX_API_URL=http://sandbox-sidecar:8080
SANDBOX_API_TOKEN=<openssl rand -hex 32>    # same value for orchestrator and sidecar
docker compose up -d sandbox-sidecar mcp-server
```

The sidecar does **not** build the image on demand: `SANDBOX_BUILD_ON_MISSING` defaults to
`false` and the compose never sets it, so a missing image answers `IMAGE_UNAVAILABLE`
(building never happens inside a request, contract §3).

Without any backend, `SANDBOX_REQUIRED` decides the behaviour:

| Variable | Default | Purpose |
|----------|---------|---------|
| `SANDBOX_IMAGE` | `mcp-python-sandbox:latest` | Image the sidecar and the tools run |
| `SANDBOX_REQUIRED` | `true` (compose) | `true` rejects every call with `SANDBOX_UNAVAILABLE`, with `use_sandbox` true or false. `false` enables the restricted in-process `exec()` fallback, which is evadable: local development only |

### Sidecar execution (contract v1, shipped in the compose)

The sidecar is the only component with access to the Docker daemon, so the orchestrator
never mounts the socket. The v1 contract — endpoints, request/response shape, isolation
model, limits and error codes — is frozen in
[docs/SANDBOX_SIDECAR_API.md](docs/SANDBOX_SIDECAR_API.md), with a machine-readable schema
in [docs/sandbox-sidecar.openapi.yaml](docs/sandbox-sidecar.openapi.yaml).

When `SANDBOX_API_URL` is set, `tools/common/sandbox.py` calls `POST /v1/execute` with
`Authorization: Bearer <SANDBOX_API_TOKEN>` instead of `docker.from_env()`, and every
sidecar error is mapped per §6.2 of the contract. With `SANDBOX_API_URL` empty the tool
answers `SANDBOX_UNAVAILABLE` when no isolated execution is available.

## Toolset Filtering

The server can expose different subsets of tools without code changes, using **toolsets** defined in `configs/toolsets.yaml`.

### Available Toolsets

| Toolset | Tools | Use Case |
|---------|-------|----------|
| `default` | 19 general-purpose tools (echo, datetime, weather, kb, web, OCR, media, storage) | End users, general assistance |
| `development` | 14 dev tools + core (opencode_context, codebase_scan, etc.) | Developers, coding agents |
| `ocu-investigacion` | 25 tools (11 shared + 14 forensic) | Criminal investigation |

Counts are the length of each list in `configs/toolsets.yaml`.

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

# .env — all three toolsets
MCP_TOOLSET=default,development,ocu-investigacion

# .env — if MCP_TOOLSET is unset or empty the code applies `default`,
# it does NOT expose every tool. List all three to get everything.
```

> **Note:** The `ocu-investigacion` toolset requires additional services (Memgraph and OpenSearch). When running with Docker Compose, activate the matching profile:
> ```bash
> docker compose --profile ocu-investigacion up -d
> ```

Running outside Compose requires the same environment variables the compose
injects (`BASE_URL`, `MCP_AUTH_KEY`, `MCP_UPLOAD_API_KEY`, `ADMIN_API_KEY`,
`POSTGRES_*`, `RUSTFS_*`, `CRAWL4AI_TOKEN`, `SANDBOX_API_TOKEN`) plus a writable
`/data`. The image entrypoint is `/app/mcp-server`; see
`deployments/docker-compose.yml` for the full list.

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
| [`docs/QUICKSTART.md`](docs/QUICKSTART.md) | Install, verify and first request |
| [`docs/USAGE.md`](docs/USAGE.md) | Functional usage per tool |
| [`deployments/.env.example`](deployments/.env.example) | Every environment variable, commented |
| [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) | Technical architecture and development guide |
| [`docs/PRODUCTION.md`](docs/PRODUCTION.md) | Production checklist and operations |
| [`docs/API.md`](docs/API.md) | API reference |
| [`AGENTS.md`](AGENTS.md) | Build/test commands and conventions |

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

Toolset definitions in `configs/toolsets.yaml` are curated lists of tools for specific use cases. See [Available Toolsets](#available-toolsets) above.

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

For the **OCu (criminal investigation) toolset**, the required database stack
(Memgraph, OpenSearch) ships in the same compose behind the profile:

```bash
docker compose --profile ocu-investigacion up -d
MCP_TOOLSET=ocu-investigacion   # in .env
```

See [`deployments/docker-compose.yml`](deployments/docker-compose.yml) and
[`docs/SANDBOX_SIDECAR_API.md`](docs/SANDBOX_SIDECAR_API.md) for the sidecar contract.

### Disabling a Tool

Remove its entry from `tools/` or exclude it from the active toolset in `configs/toolsets.yaml`.

## License

See [LICENSE](LICENSE).