# MCP Endpoint Authentication

How the MCP Orchestrator authenticates callers, what it refuses to do, and how to rotate a token.

## The contract

- **One switch.** `MCP_AUTH_MODE` decides everything. `required` (the default when the variable is
  unset) demands a bearer token on `/mcp`, `/sse`, `/message` and `/upload`. `off` disables
  authentication and is only valid for isolated local development. Any other value aborts startup.
- **The config file lists users, not secrets.** `configs/config.yaml`:

  ```yaml
  auth:
    keys:
      - user_id: amphora
        key_env: MCP_AUTH_KEY_AMPHORA
      - user_id: legacy-upload
        key_env: MCP_UPLOAD_API_KEY
  ```

  Each entry names an environment variable; the token lives there and is read once at startup.
- **Tokens become digests.** The runtime index is `map[sha256(token)]user_id`
  (`internal/auth/keyring.go`). The plaintext token is not kept anywhere, so the index cannot be
  replayed as a credential.
- **Identity is never client-asserted.** `capabilities.experimental.user_id` in the MCP
  `initialize` request is ignored — it is logged and dropped. The session's `user_id` comes from
  the verified token, and it is what reaches the KB tools. A session that initializes without an
  authenticated identity stays unbound.
- **MCP_UPLOAD_API_KEY is an ordinary entry** (`user_id: legacy-upload`) so `/upload` has no
  separate on/off rule of its own.

## Failure modes (all abort startup)

| Condition | Result |
|-----------|--------|
| `MCP_AUTH_MODE` is not `required`/`off` | fatal: `invalid MCP_AUTH_MODE "x": want "required" or "off"` |
| `required` and `auth.keys` is empty | fatal: keyring is refused rather than ignored |
| `key_env` unset or empty in the environment | fatal, naming the entry and the variable |
| token shorter than 8 or longer than 256 chars, or outside `[A-Za-z0-9._-]` | fatal |
| duplicate `user_id` | fatal |
| two entries resolving to the same token | fatal |

Nothing is skipped: the first bad entry stops the process, and a partial keyring is never built.
At request time the middleware is fail-closed as well — an enabled-but-empty keyring answers `503`
instead of letting a request through.

## Request behaviour

| Situation | Response |
|-----------|----------|
| `MCP_AUTH_MODE=off` | passed through, no identity injected |
| no `Authorization` header, non-`Bearer` scheme, malformed token | `401` |
| token not in the keyring | `401` |
| valid token | request continues with `user_id` in its context |

Middleware order for the MCP endpoints is **body limit → CORS → auth → rate limit**:

- CORS is outside auth so a browser preflight (`OPTIONS`) is answered before a token is demanded,
  and a disallowed origin gets `403` before the token is even read;
- auth is outside the rate limiter so unauthenticated traffic cannot consume the budget of
  legitimate clients.

Unauthenticated on purpose: `GET /health`, `GET /health/detailed`, `GET /metrics` (container
healthcheck and operations), `GET /files/...`, and `GET /internal/resource/{token}` (addressed by
an unguessable token and only reachable on the internal Docker network; the Python tools use it).

## Calling the server

```bash
curl -sS -X POST http://localhost:8080/mcp \
  -H "Authorization: Bearer $MCP_AUTH_KEY_AMPHORA" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"1.0"}}}'
```

For local development without auth:

```bash
MCP_AUTH_MODE=off go run ./cmd/server -config configs/config.local.yaml
```

## Rotating a token

1. Generate a new value: `openssl rand -hex 32`.
2. Update the variable in `deployments/.env` on the host (never in a committed file).
3. Recreate the container (`docker compose up -d mcp-server`) — the keyring is read at startup.
4. Verify with a real call (see the curl above) that the new token is accepted and the old one is
   rejected with `401`.
5. Keep the previous value until step 4 is green; then drop it.

## Adding a user

1. Add an entry to `auth.keys` in `configs/config.yaml` (`user_id` + a new `key_env` name).
2. Add the variable to `deployments/.env.example` with a `change_me` placeholder and to the real
   `deployments/.env` with a generated value.
3. Pass it into the `mcp-server` service environment in `deployments/docker-compose.yml` with the
   `${VAR:?}` form so a compose config check fails when it is missing.
4. Recreate the container and verify with a real call.

## Tests

- `internal/auth/keyring_test.go` — mode resolution and the fail-fast parse table.
- `internal/auth/middleware_keyring_test.go` — `401`/`503`, context injection, header spoofing.
- `internal/auth/identity_test.go` — isolation asserted against the real `session.Store`, and the
  regression that a client-asserted `user_id` is never stored.
- `internal/transport/auth_chain_test.go` — CORS outside auth, preflight without a token, the
  shared keyring on `/upload`.
- `tests/auth_endpoint_test.go` — end-to-end over real HTTP: `401` without a token, session bound
  to the token identity, client-asserted identity ignored, two users isolated.
