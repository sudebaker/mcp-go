# AGENTS.md - MCP-Go

Compact instructions for working in this MCP Orchestrator repo.

## How We Work Here

Read this before touching the code. This repo has a live deployment behind it.

**Branch from a fresh `main`, one worktree per task.**

```bash
git fetch origin && git checkout -B main origin/main
git worktree add ../mcp-go-<task> -b <role>/<task> origin/main
```

Worktrees live next to the repo, inside `~/Proyectos/`. Do not clone the repo into
scratch or temp directories: clones outside the project root get purged and rot.
Remove the worktree (`git worktree remove <dir>`) once its PR is merged.

**Everything lands through a pull request.** Never push to `main`, never force-push a
shared branch. State in the description what you verified and what you did not.

**Stacked work: declare the base branch.** If your task builds on another open PR, open
your PR against that branch (not `main`) and say "merges bottom-up" in the description.
A stack is always merged bottom-up; retarget the upper PRs to `main` with
`PATCH /repos/{owner}/{repo}/pulls/{n}` (`base=main`) once the lower one lands —
`gh pr edit --base` needs the `read:org` scope and fails with our token.

**This repo is deployed; the server tree is production.** The running orchestrator is
the checkout on epicteto (`/home/amphora/dockers/mcp-go`). Never edit it, never
`git pull` it, never restart the service without being asked. Changes reach production
only after the PR is merged, by whoever owns the deployment.

**No secrets in the repo.** Tokens and keys (`MCP_AUTH_KEY`, `MCP_UPLOAD_API_KEY`, ...)
come from the environment. Never commit a real value, never write one into an example
file, never echo one in a PR, an issue or a commit message.

**Run the checks before opening the PR.** `go fmt ./... && go vet ./... && go test ./...`,
plus `python -m pytest tests/test_security_mitigations.py -v` if you touched the Python
tools. CI must be green before merge.

**Report facts, not impressions.** Exact paths, commit hashes, command output. "Should
work" is not a result. If something fails, say it in the same message as the rest of the
report — never save a failure for a later turn.

## Essential Commands

```bash
# Build & test Go
go build -o bin/mcp-server ./cmd/server
go fmt ./... && go vet ./... && go test ./...

# Run Go tests
go test ./...                          # all
go test -run TestName ./package -v     # specific
go test -race ./...                    # with race detector

# Python security tests
python -m pytest tests/test_security_mitigations.py -v

# MCP test client (requires services running)
python tests/mcp_test_client.py  # needs DATABASE_URL env var
python tests/mcp_test_client.py --skip-external

# Docker services
cd deployments
docker-compose up -d
docker logs -f mcp-orchestrator
```

## Go Code Style

**Imports (3 groups, alphabetical):**
```go
import (
    "context"
    "encoding/json"

    "github.com/google/uuid"
    "github.com/rs/zerolog/log"

    "github.com/sudebaker/mcp-go/internal/config"
)
```

**Error handling:** Always wrap errors with context:
```go
if err != nil {
    return nil, fmt.Errorf("operation failed: %w", err)
}
```

**Logging:** Use zerolog with structured fields:
```go
log.Info().Str("tool", name).Msg("Executing")
log.Error().Err(err).Str("file", path).Msg("Failed")
```

## Project Structure

```
mcp-go/
├── cmd/server/main.go        # Entry point
├── internal/
│   ├── config/               # YAML config loading
│   ├── executor/             # Tool subprocess execution
│   ├── mcp/                  # MCP types (SubprocessRequest/Response)
│   ├── session/              # Session store for user_id lookup
│   ├── transport/            # HTTP/SSE handlers
│   └── ...
├── tools/                    # Python tools (stdin/stdout JSON)
├── configs/config.yaml       # Tool definitions
└── deployments/              # Docker Compose
```

## Python Tools Protocol

Tools communicate via JSON over stdin/stdout:
```python
import json, sys

request = json.loads(sys.stdin.read())
# request = {"request_id": "...", "tool_name": "...", "arguments": {...}, "context": {...}}

response = {"success": True, "content": [{"type": "text", "text": "..."}]}
print(json.dumps(response, default=str))
```

**Error response:**
```python
{"success": False, "error": {"code": "ERROR_CODE", "message": str(e)}}
```

## Key Patterns

### Adding a new tool
1. Create `tools/new_tool/main.py` with JSON stdin/stdout protocol and `tool.yaml` manifest
2. Add the tool name to the appropriate toolset in `configs/toolsets.yaml`
3. Restart container: `docker-compose restart mcp-server`

### User Isolation (KB tools)
KB tools (`kb_ingest`, `kb_search`) use `context.user_id` for data isolation:
- User identity comes from the bearer token verified by the transport (`internal/auth`), which
  resolves `sha256(token) → user_id` from the keyring
- `capabilities.experimental.user_id` is client-asserted and is IGNORED (logged only)
- Go server stores `session_id → user_id` mapping in `internal/session/store.go`
- Python KB tool receives `user_id` in the `context` object of the request
- All queries filter by `user_id` - users only see their own documents
- A session that initializes without an authenticated identity stays unbound
- `/upload` resolves the destination namespace from `X-Session-ID` through the session store, so it
  verifies that the session belongs to the token identity (`resources.OwnerOf`) and answers `403`
  when it does not: a token holder cannot write into another user's namespace by naming their
  session id. With `MCP_AUTH_MODE=off` there is no identity to compare and the header is trusted
  (local development only)

**Performance:** KB tools use a persistent process pool (5 processes per tool) to avoid reloading embedding models and database connections on each call. Latency drops from ~7s (cold) to <1s (warm).

### Authentication (MCP endpoints)
See [docs/AUTH.md](docs/AUTH.md) for the full contract. In short:
- `MCP_AUTH_MODE` (`required` by default, `off` for isolated local dev) is the ONLY auth switch
- The keyring is declared in `configs/config.yaml` as `auth.keys: [{user_id, key_env}]`; the token
  value is read at startup from the env var named by `key_env` (`internal/auth/keyring.go`)
- Fail-closed: unknown mode, empty keyring in required mode, unset `key_env`, duplicate `user_id`
  or duplicate token abort startup. Never reintroduce an "on empty → skip" behaviour
- `/mcp`, `/sse`, `/message` and `/upload` require `Authorization: Bearer <token>`. `/health`,
  `/metrics`, `/files/` and `/internal/resource/{token}` stay open (container healthcheck,
  network-isolated internal streaming)
- Middleware order for the MCP endpoints is body limit → CORS → auth → rate limit, so clients
  receive 401/503 from auth, not a CORS error. For that to hold in a browser, `CORSMiddleware`
  allows `Authorization` in `Access-Control-Allow-Headers` and exposes `Mcp-Session-Id` in
  `Access-Control-Expose-Headers` — without the first the preflight fails before auth runs, without
  the second a JS client cannot read the session id it must send back (see `internal/transport/cors.go`)

### mcp-go Library Hooks
Uses `github.com/mark3labs/mcp-go` server hooks:
```go
hooks := &server.Hooks{}
hooks.AddAfterInitialize(func(ctx context.Context, id any, msg *mcp.InitializeRequest, result *mcp.InitializeResult) {
    if sess := server.ClientSessionFromContext(ctx); sess != nil {
        // Identity comes from the bearer token in ctx, never from msg.
        auth.BindSessionUserID(ctx, "", sessionStore, sess.SessionID())
    }
})
hooks.AddOnUnregisterSession(func(ctx context.Context, sess server.ClientSession) {
    sessionStore.Delete(sess.SessionID())
})
```

## External Services

| Service | Internal URL | Purpose |
|---------|--------------|---------|
| PostgreSQL | `postgres:5432` | KB storage (pgvector) |
| RustFS | `rustfs:9000` | S3-compatible storage |
| Ollama | `ollama:11434` | LLM inference |
| SearXNG | `searxng:8080` | Private web search |
| crawl4ai | `crawl4ai:8000` | Web scraping (LLM-optimized) |

## Important Paths

- **Config:** `configs/config.yaml` - tool definitions
- **Templates:** `templates/` - PDF report templates
- **Data:** `/data/` inside container - read/write workspace

## Security Mitigations

- **SSRF**: URL validation blocks cloud metadata/internal networks
- **SSTI**: Jinja2 `SandboxedEnvironment` prevents template injection
- **ReDoS**: Regex patterns pre-compiled and cached at startup
- **Prompt injection**: Content sanitization strips injection patterns

See [SECURITY.md](SECURITY.md) for details.

## Related Docs

| Doc | Purpose |
|-----|---------|
| [API.md](API.md) | MCP endpoints and tool reference |
| [DEVELOPMENT.md](DEVELOPMENT.md) | Building and adding tools |
| [SECURITY.md](SECURITY.md) | Security mitigations |
| [PRODUCTION.md](PRODUCTION.md) | Deployment checklist |