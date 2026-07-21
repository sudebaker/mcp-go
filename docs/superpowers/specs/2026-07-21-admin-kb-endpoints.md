# Spec: Admin KB Endpoints

**Date**: 2026-07-21
**Status**: Implementado
**Author**: AI

## Goal

Añadir endpoints administrativos protegidos con `ADMIN_API_KEY` para borrado y limpieza de datos en las herramientas KB (`kb_documents`, `kb_chunks`), sin cambiar el flujo actual de autenticación (`capabilities.experimental.user_id`).

## Contexto

- KB tools usan `context.user_id` para aislamiento de datos (filtro `WHERE user_id = %s`)
- `user_id` viene de `capabilities.experimental.user_id` en el MCP `InitializeRequest` — no autenticado
- No hay forma actual de borrar datos de un usuario o colección
- Se acepta el riesgo de suplantación de `user_id` (no se resuelve en este spec)

## Endpoints

| Método | Path | Auth | Descripción |
|--------|------|------|-------------|
| GET | `/admin/kb/users` | `ADMIN_API_KEY` | Lista users con stats (doc_count, bytes) |
| DELETE | `/admin/kb/users/{user_id}` | `ADMIN_API_KEY` | Hard delete todos los docs del user |
| DELETE | `/admin/kb/users/{user_id}/collections/{collection}` | `ADMIN_API_KEY` | Hard delete de una colección del user |
| DELETE | `/admin/kb/collections/{collection}` | `ADMIN_API_KEY` | Hard delete de una colección global |
| GET | `/admin/kb/users/{user_id}` | `ADMIN_API_KEY` | Detalle de un usuario (doc count, collections) |
| GET | `/admin/kb/users/{user_id}/export` | `ADMIN_API_KEY` | JSON dump de documentos del user |
| GET | `/admin/audit` | `ADMIN_API_KEY` | Audit log paginado |

## Arquitectura

- Go se conecta **directo a Postgres** con un **pool dedicado** (`adminDB` en `cmd/server/main.go`, `SetMaxOpenConns(10)`, `SetMaxIdleConns(5)`). No reusa el pool de health checks (`SetMaxOpenConns(2)`).
- No invoca subprocess Python
- CASCADE natural: `kb_chunks.document_id REFERENCES kb_documents(id) ON DELETE CASCADE` ya existe en `tools/knowledge_base/main.py:286`
- Middleware admin reutiliza el patrón de `authMiddleware` existente (`internal/transport/sse.go:70-101`) pero **no copia** el comportamiento de skip-if-empty-key. Si `ADMIN_API_KEY` está vacío → 503 Service Unavailable.

## Configuración

Env var `ADMIN_API_KEY` — si no está seteada, los endpoints devuelven 503. El server arranca normalmente.

## Middleware

- Header: `Authorization: Bearer <ADMIN_API_KEY>`
- Si `ADMIN_API_KEY` está vacío → 503 `{"error":"admin endpoints disabled: ADMIN_API_KEY not set"}`
- Si key inválida → 401 `{"error":"invalid admin key"}`
- Si key válida → pasa `ADMIN_REQUEST` en el contexto (para handlers identificar al admin)
- Genera `request_id` con `uuid.New()` y lo inyecta en `r.Context()`. Si el cliente envía header `X-Request-ID`, se respeta ese valor. Se loguea en cada respuesta como header `X-Request-ID`.
- **Rate limiting**: se usan dos instancias de `RateLimiter` wrappeadas por método HTTP en un `http.HandlerFunc` intermedio:
  - GET: `NewRateLimiter(1.0, 60)` (1 RPS, burst 60 = 60 req/min sostenido)
  - DELETE: `NewRateLimiter(0.17, 10)` (0.17 RPS, burst 10 = ~10 req/min sostenido)
  - Implementado en `internal/transport/sse.go` en el bloque de montaje de admin routes

## Migración SQL

```sql
CREATE TABLE IF NOT EXISTS admin_audit_log (
    id BIGSERIAL PRIMARY KEY,
    admin_action VARCHAR(50) NOT NULL,
    target_user_id VARCHAR(255),
    target_collection VARCHAR(255),
    docs_deleted INTEGER,
    bytes_freed BIGINT,
    request_id VARCHAR(255),
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_admin_audit_created_at ON admin_audit_log(created_at DESC);

-- Índice compuesto para queries admin de borrado y listado
CREATE INDEX IF NOT EXISTS idx_kb_documents_user_collection
ON kb_documents(user_id, collection);
```

Se ejecuta en startup del server (mismo patrón que las migraciones en `tools/knowledge_base/main.py:250`).

## Handlers

### DeleteUserData — `DELETE /admin/kb/users/{user_id}`

Race condition entre requests concurrentes se mitiga con `FOR UPDATE`.

```sql
BEGIN;
SELECT COUNT(*), COALESCE(SUM(octet_length(content)), 0)
FROM kb_documents WHERE user_id = $1
FOR UPDATE;
DELETE FROM kb_documents WHERE user_id = $1;
INSERT INTO admin_audit_log(admin_action, target_user_id, docs_deleted, bytes_freed, request_id)
VALUES ('delete_user', $1, $2, $3, $4);
COMMIT;
```

Response: `{"deleted":true, "user_id":"...", "docs_deleted":N, "docs_bytes_freed":M}`

**Nota:** `docs_bytes_freed` solo cuenta `kb_documents.content`. El `ON DELETE CASCADE` a `kb_chunks` libera espacio adicional (content + embeddings 384d) que no se refleja aquí.

### DeleteUserCollection — `DELETE /admin/kb/users/{user_id}/collections/{collection}`

```sql
BEGIN;
SELECT COUNT(*), COALESCE(SUM(octet_length(content)), 0)
FROM kb_documents WHERE user_id = $1 AND collection = $2
FOR UPDATE;
DELETE FROM kb_documents WHERE user_id = $1 AND collection = $2;
INSERT INTO admin_audit_log(admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id)
VALUES ('delete_user_collection', $1, $2, $3, $4, $5);
COMMIT;
```

### DeleteGlobalCollection — `DELETE /admin/kb/collections/{collection}`

Esta operación afecta a **todos los usuarios**. Siempre debe loguearse.

```sql
BEGIN;
SELECT COUNT(*), COALESCE(SUM(octet_length(content)), 0)
FROM kb_documents WHERE collection = $1
FOR UPDATE;
DELETE FROM kb_documents WHERE collection = $1;
INSERT INTO admin_audit_log(admin_action, target_collection, docs_deleted, bytes_freed, request_id)
VALUES ('delete_global_collection', $1, $2, $3, $4);
COMMIT;
```

### ListUsers — `GET /admin/kb/users`

```sql
SELECT user_id, COUNT(*) as doc_count, COALESCE(SUM(octet_length(content)), 0) as bytes
FROM kb_documents
GROUP BY user_id
ORDER BY doc_count DESC;
```

### GetUser — `GET /admin/kb/users/{user_id}`

```sql
SELECT collection, COUNT(*) as doc_count, COALESCE(SUM(octet_length(content)), 0) as bytes
FROM kb_documents
WHERE user_id = $1
GROUP BY collection;
```

### ExportUser — `GET /admin/kb/users/{user_id}/export?limit=100&offset=0`

Paginado para evitar consumo excesivo de memoria. Params: `limit` (default 100, max 1000), `offset` (default 0). Header `X-Total-Count` con el total de documentos del usuario. Devuelve solo metadatos (no `content`).

```sql
-- COUNT total (para X-Total-Count)
SELECT COUNT(*) FROM kb_documents WHERE user_id = $1;

-- Página actual
SELECT id, doc_hash, file_path, collection, metadata, created_at
FROM kb_documents
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
```

### AuditLog — `GET /admin/audit?limit=50&offset=0`

```sql
SELECT * FROM admin_audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2;
```

## Validaciones

- `user_id`: regex `^[^/]{1,255}$` (cualquier carácter excepto `/`, 1-255 chars). Tan permisiva porque el Python KB tool no valida `user_id` — el `sanitizePathMiddleware` rechaza `//` y el handler trunca en `/` via `strings.Index`.
- `collection`: regex `^[a-zA-Z0-9_-]{1,100}$` (mismo que `COLLECTION_NAME_PATTERN` en `main.py:73`)
- Path traversal: manejado por `sanitizePathMiddleware` existente
- SQL injection: prepared statements

## Archivos

### Nuevos

| Archivo | Propósito |
|---------|-----------|
| `internal/admin/handler.go` | Handlers + middleware |
| `internal/admin/handler_test.go` | Tests unitarios |

### Modificados

| Archivo | Cambio |
|---------|--------|
| `internal/transport/sse.go` | Añadir `AdminKey` + `DB` a struct/config, montar rutas `/admin/` + middleware + rate limiting por método |
| `cmd/server/main.go` | Leer `ADMIN_API_KEY`, crear `adminDB` pool dedicado (max 10 conns), pasar a MCPConfig |
| `docs/API.md` | Nueva sección "Admin Endpoints" con todos los endpoints, ejemplos, y `ADMIN_API_KEY` en env vars |
| `deployments/.env.example` | Añadir sección `# ADMIN` con `ADMIN_API_KEY=` |
| `go.mod` / `go.sum` | Dep: `github.com/DATA-DOG/go-sqlmock v1.5.2` |

## Tests

### Middleware
- 401 sin header
- 401 con key inválida
- 503 si key no configurada
- 200 con key correcta
- Header `X-Request-ID` presente en response
- Rate limit GET superado → 429
- Rate limit DELETE superado → 429

### Handlers
- 200 delete user → docs borrados + audit log escrito con `request_id`
- 200 delete collection → solo esa colección
- 200 delete collection global → audit log con action `delete_global_collection`
- 200 list users (vacíos + con datos)
- 200 get user detail
- 200 export user paginado (limit=10, offset=0)
- 200 export user con X-Total-Count correcto
- 503 si `ADMIN_API_KEY` no configurada
- 401 si key inválida
- Header `X-Request-ID` presente en response (respeta input del cliente, validado)
- Rate limit GET superado → 429
- Rate limit DELETE superado → 429
- Validación user_id ilegal → 400 (vacíos o con `/`)
- Validación collection ilegal → 400
- Validación X-Request-ID malicioso → reemplazado por UUID

## Riesgos aceptados

- Suplantación de `user_id`: cualquiera puede declarar `capabilities.experimental.user_id` arbitrario. No se resuelve.
- Admin key leak: si `ADMIN_API_KEY` se expone, attacker puede borrar todos los datos. Mitig: rotación manual, no loguear key.
- Sin audit de lecturas: solo se auditan borrados.
- `docs_bytes_freed` en audit log: solo cuenta `kb_documents.content`, no las `kb_chunks` (content + embeddings 384d). El espacio real liberado es mayor.
- Offset sin cota máxima: `DELETE /admin/kb/users/{id}/export?offset=999999999` consume CPU en Postgres. Mitig: el rate limiter (10 req/min en DELETE, 60 en GET) limita el impacto.

## Rollout

1. `ADMIN_API_KEY` opcional — si no se setea, server arranca igual (endpoints devuelven 503)
2. Admin DB pool dedicado (`adminDB`) con `SetMaxOpenConns(10)` — independiente del pool de health checks (2 conns)
3. Migración SQL en startup via `admin.SetupMigrations(ctx)` llamado desde `sse.go:Start()`
4. Documentar en API.md
