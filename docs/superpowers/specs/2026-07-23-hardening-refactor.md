# Spec: Hardening y Refactor mcp-go

**Date**: 2026-07-23
**Status**: Spec v2 (post-review)
**Author**: AI + Jesús

---

## Goal

Endurecer la seguridad, reducir deuda técnica, y mejorar observabilidad del proyecto mcp-go para prepararlo para publicación pública. Se identificaron 4 mejoras críticas de seguridad, 4 refactors de alta prioridad, 4 mejoras operacionales, y 3 nice-to-haves.

## Resumen de hallazgos

Revisión completa del código fuente (~35 archivos Go, ~12 tools Python) usando análisis estático con codebase-memory (58 funciones Go analizadas, métricas de complejidad, cobertura de tests, detección de código duplicado) más revisión manual de seguridad, arquitectura y calidad de código.

Hallazgos principales:
- **Security**: Comparación no constante de API keys, sin límite de body en endpoints MCP, goroutine de uploads sin protección de panic, CORS permissive para admin endpoints
- **Refactor**: `config.Load()` con complejidad ciclomática 23, `sse.go:Start()` con 193 líneas de registro de rutas inline, 3 delete handlers en admin con ~90% código duplicado, auth middleware duplicado en upload y admin
- **Operacional**: Sin validación Go-side de inputs de tools, scripts shell con código duplicado (jaccard 1.0), imagen Docker incluye `.git/` completo (~18MB), límite de conexiones DB no documentado
- **Nice-to-have**: Sin correlación de logs Go↔Python, admin endpoints sin métricas Prometheus, TLS no documentado para entornos no air-gapped

---

## T1 — Security Critical

### T1.1 — Time-constant API key comparison

**Problema**: `authMiddleware` (sse.go:92) y `admin.Middleware` (handler.go:53) comparan `token != key` con comparación directa de strings, vulnerable a timing attacks. En air-gapped el riesgo es teórico, pero la corrección es trivial y elimina un hallazgo de seguridad.

Hay dos keys independientes: `MCP_UPLOAD_API_KEY` (protege `/upload`) y `ADMIN_API_KEY` (protege `/admin/`). Cada una necesita su propio pre-hash.

**Solución**: Pre-hashear cada server key una vez en startup (no por request) y comparar con `subtle.ConstantTimeCompare`:

```go
// En startup (MCPServer constructor):
s.uploadKeyHash = sha256.Sum256([]byte(uploadKey))
s.adminKeyHash  = sha256.Sum256([]byte(adminKey))
```

El middleware recibe `keyHash [32]byte` (ver T2.4) — no el raw string. Esto fuerza el uso de `ConstantTimeCompare` en el punto de uso y previene comparaciones directas accidentales.

**Riesgo mitigado**: `sha256.Sum256` es O(n) — inputs enormes causarían DoS antes de la comparación. El límite `validToken` regex `{8,256}` (ver T2.4) previene esto — tokens >256 chars son rechazados antes de llegar al hash.

**Nota**: `ConstantTimeCompare` requiere slices de igual longitud — al hashear ambos inputs a 32 bytes, se cumple sin padding.

**Archivos afectados**:
- `internal/transport/sse.go` — reemplazar `uploadAPIKey string` por `uploadKeyHash [32]byte` en MCPServer
- `internal/admin/handler.go` — `Middleware` reemplazado por `auth.BearerAuth` (ver T2.4)
- `internal/auth/middleware.go` — `BearerAuth` recibe `keyHash [32]byte`

**Tests**:
- `TestMiddleware_TokenLengthLimit`: token de 1025 chars → 401 sin procesar hash
- `TestMiddleware_ConstantTimeCompare_Timing`: benchmark 10,000 iteraciones, t-test entre grupos correcto/incorrecto. p-value < 0.05 → FAIL.
- `TestMiddleware_PreHashedKey`: sha256.Sum256 se llama una vez en startup, no por request.

**Esfuerzo**: ~15 min

---

### T1.2 — Body size limits en endpoints MCP

**Problema**: Los handlers `/mcp`, `/sse`, `/message` no limitan el tamaño del request body. Un atacante puede enviar JSONs de varios GB y agotar RAM del servidor (DoS). El upload handler sí tiene `MaxSizeMB` en configuración pero los endpoints MCP no.

**Solución**: Middleware único `MaxBodyMiddleware` aplicado a los 3 endpoints MCP (`/mcp`, `/sse`, `/message`). Tras investigar `github.com/mark3labs/mcp-go v0.43.2`, ningún endpoint lee el body en streaming — son single-read o no-read. `http.MaxBytesReader` es seguro en todos.

```go
func MaxBodyMiddleware(maxSize int64) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            r.Body = http.MaxBytesReader(w, r.Body, maxSize)
            next.ServeHTTP(w, r)
        })
    }
}
```

**Comportamiento de `http.MaxBytesReader`**: no escribe 413 automáticamente. Cuando se excede el límite, retorna `*http.MaxBytesError` en la siguiente llamada a `Read()`. Además, la conexión se marca para cierre. El handler de mcp-go recibe el error al decodificar el body y responde con parse error (400). Esto es aceptable: el body grande se rechaza. Si se desea un 413 explícito, habría que interceptar el error en un wrapper del handler.

**Además**: `http.Server.ReadTimeout` global para cerrar conexiones abandonadas.

**Configuración**: Constante `DefaultMaxMCPBodySize` (10 MB) sobreescribible con env var `MCP_MAX_BODY_SIZE_MB`.

**Configuración**: Constante `DefaultMaxMCPBodySize` (10 MB) sobreescribible con env var `MCP_MAX_BODY_SIZE_MB`.

**Archivos afectados**:
- `internal/transport/sse.go` — middleware `maxBodyMiddleware` aplicado a `/mcp`, `/message`, `/sse`
- `cmd/server/main.go` — lectura opcional de `MCP_MAX_BODY_SIZE_MB`
- `internal/config/config.go` — campo `MaxMCPBodySizeMB`

**Tests**:
- `TestMaxBody_LargePayload_mcp`: POST a `/mcp` con body >10MB → 413
- `TestMaxBody_LargePayload_message`: POST a `/message` con body >10MB → 413
- `TestMaxBody_NormalPayload`: POST con body normal → pasa al handler (sin regresión)
- `TestMaxBody_Configurable`: con env var menor → límite adaptado
- `TestMaxBody_SSE_get`: GET a `/sse` con body (válido) → no se corta conexión

**Esfuerzo**: ~15 min

---

### T1.3 — Recuperación de panic en startUploadCleanup

**Problema**: `go s.startUploadCleanup()` lanza una goroutine sin protección de recover. Si `cleanExpiredUploads()` paniquea (filesystem error, nil pointer, directorio inexistente), la goroutine muere silenciosamente y los uploads expirados nunca se limpian.

Si el error es persistente (ej. filesystem corruption), el re-spawn simple causa un loop infinito de crash/restart consumiendo CPU.

**Solución**: Linear delay + restart limit + disable flag (delay lineal es suficiente para un cleanup loop — no necesita exponential backoff como un network retry):

```go
var (
    cleanupRestartCount atomic.Int32
    cleanupDisabled     atomic.Bool
)

func (s *MCPServer) startUploadCleanup() {
    defer func() {
        if r := recover(); r != nil {
            count := cleanupRestartCount.Add(1)
            if count > 10 {
                log.Error().
                    Int32("restart_count", count).
                    Msg("upload cleanup failed 10+ times, disabling permanently")
                cleanupDisabled.Store(true)
                return
            }
            delay := time.Duration(min(5*count, 300)) * time.Second // 5s, 10s, 15s, ..., max 5min
            log.Error().
                Interface("panic", r).
                Int32("restart_count", count).
                Dur("delay", delay).
                Msg("upload cleanup panicked, restarting")
            time.Sleep(delay)
            go s.startUploadCleanup()
        }
    }()

    if cleanupDisabled.Load() {
        return
    }
    // ... existing cleanup loop ...
}
```

**Archivos afectados**:
- `internal/transport/upload_handler.go` — función `startUploadCleanup`

**Tests**:
- `TestUploadCleanup_PanicRecovery_Delay`: inducir panic 3 veces, verificar delay creciente (5s, 10s, 15s)
- `TestUploadCleanup_PanicRecovery_MaxRetries`: inducir panic 11 veces, verificar que `cleanupDisabled` se activa y no se reintenta más
- `TestUploadCleanup_NormalFlow`: sin panic, la goroutine ejecuta limpieza normalmente
- `TestUploadCleanup_DisabledFlag`: con `cleanupDisabled=true`, la goroutine retorna inmediatamente

**Esfuerzo**: ~15 min

---

### T1.4 — CORS hardening para admin endpoints

**Problema**: Los endpoints admin (`/admin/`) heredan el middleware CORS del mux principal. Si `allowed_origins` está vacío o es `*`, cualquier sitio web puede hacer requests desde el navegador de un admin autenticado. Bearer tokens mitigan CSRF (los navegadores no auto-adjuntan `Authorization: Bearer` en requests cross-site), pero un sitio malicioso podría aprovechar un token almacenado en `localStorage` accesible desde JS.

**Solución**: Validación estricta de Origin mediante middleware dedicado. No bloquear todo (rompe herramientas legítimas como CLI, Postman, curl):

```go
func adminCORSMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        origin := r.Header.Get("Origin")

        // Si no hay Origin, es request directo (CLI, curl, Postman) → permitir
        if origin != "" {
            allowed := false
            for _, ao := range allowedOrigins {
                if ao == "*" || ao == origin {
                    allowed = true
                    break
                }
            }
            if !allowed {
                http.Error(w, `{"error":"origin not allowed"}`, http.StatusForbidden)
                return
            }
            w.Header().Set("Access-Control-Allow-Origin", origin)
        }

        w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
        w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
        w.Header().Set("Access-Control-Allow-Credentials", "false")

        if r.Method == "OPTIONS" {
            w.WriteHeader(204)
            return
        }

        next.ServeHTTP(w, r)
    })
}
```

No se necesita header anti-CSRF adicional: `Authorization: Bearer` no se adjunta automáticamente en requests cross-site (no es cookie), por lo que CSRF no es aplicable. La protección real es la validación de Origin + Bearer token.

**Log warning**: si admin endpoints están habilitados Y `allowed_origins` está vacío:

```go
if s.adminKey != "" && len(cfg.Server.AllowedOrigins) == 0 {
    log.Warn().Msg("ADMIN_API_KEY is set but CORS allowed_origins is empty — admin endpoints accessible from any origin")
}
```

**Nota de dependencia**: `adminCORSMiddleware` wrappea `auth.BearerAuth` (T2.4). Implementar T2.4 primero para que la cadena de middleware quede: `adminCORSMiddleware → rate limiter → auth.BearerAuth → handler`.

**Archivos afectados**:
- `internal/transport/sse.go` — añadir `adminCORSMiddleware` + log warning

**Tests**:
- `TestAdminCORS_NoOrigin`: request sin Origin → 200 (CLI/curl)
- `TestAdminCORS_OriginAllowed`: Origin en lista → 200 con ACA-Origin matching
- `TestAdminCORS_OriginDenied`: Origin no en lista → 403
- `TestAdminCORS_OriginWildcard`: allowed_origins=`*` → todos los origins permitidos
- `TestAdminCORS_Options`: OPTIONS → 204 sin next handler
- `TestAdminCORS_WarningLog`: admin habilitado sin allowed_origins → log warning

**Esfuerzo**: ~20 min

---

## T2 — Refactor High Priority

### T2.1 — Split config.Load() (complejidad 23 → <10)

**Problema**: `Load()` en `config.go:246-338` (94 líneas) realiza 7 operaciones distintas secuencialmente sin documentación de invariantes entre pasos. Si `applyDefaults` modifica un campo que `expandEnvMap` necesita, el bug es difícil de rastrear. No hay validación post-merge de config.

**Solución**: Extraer funciones puras bien tipadas + función `Validate()` con contractos documentados:

```go
// Invariantes del pipeline:
// 1. readConfigFile → raw bytes (no hay estado previo)
// 2. expandAndUnmarshal → Config con valores string expandidos, tools/servers pueden faltar
// 3. applyDefaults → Config con todos los campos zero-value seteados a default
//    POST: cfg.Server.Port != 0, cfg.Server.ReadTimeout != 0, etc.
// 4. expandEnvMap → Config con ${VAR} en string values reemplazados
//    PRE: solo se aplica sobre campos que ya tienen default (applyDefaults debe correr antes)
//    POST: ningún valor contiene ${...}
// 5. applyDiscovery → Config con tools descubiertos en filesystem mergeados
//    PRE: cfg.Tools tiene tools declarados en YAML (si los hay)
//    POST: cfg.Tools incluye todos los tools encontrados en tools/*/tool.yaml
// 6. applyToolsets → Tools finales mergeados según toolsets.yaml
//    PRE: cfg.Tools tiene los tools base (applyDiscovery ya corrió)
//    POST: cfg.Tools es la lista final y completa

func readConfigFile(path string) ([]byte, error)
func expandAndUnmarshal(raw []byte) (*Config, error)
func applyDefaults(cfg *Config)
func expandEnvMap(cfg *Config)
func setToolDefaults(cfg *Config)
func applyDiscovery(cfg *Config, configDir string) error
func applyToolsets(cfg *Config, configDir string) error
// Validate ya existe en validation.go; se extiende, no se crea desde cero.

func Load(path string) (*Config, error) {
    raw, err := readConfigFile(path)
    if err != nil {
        return nil, err
    }
    cfg, err := expandAndUnmarshal(raw)
    if err != nil {
        return nil, err
    }
    applyDefaults(cfg)
    expandEnvMap(cfg)
    setToolDefaults(cfg)
    if err := applyDiscovery(cfg, filepath.Dir(path)); err != nil {
        return nil, err
    }
    if err := applyToolsets(cfg, filepath.Dir(path)); err != nil {
        return nil, err
    }
    return cfg, nil
}
```

**Validate()** debe detectar configuraciones contradictorias:
- `tls.enabled=true` pero `tls.cert_path=""` → error
- `rate_limit.enabled=true` pero `rate_limit.rps <= 0` → error
- `tools` con nombres duplicados → error
- `allowed_origins` con `*` + admin endpoints habilitados → warning (ver T1.4)

**Archivos afectados**:
- `internal/config/config.go`

**Nota de testabilidad**: Las funciones extraídas son unexported (privadas al paquete `config`). No se pueden testear directamente desde `config_test` (paquete externo). Opciones:
A) Declarar tests en el mismo paquete `package config` (no `config_test`) — permite acceso directo pero mezcla tests con código
B) Tests de integración via `Load()` — cada pipeline stage se ejercita indirectamente mediante configs de test con inputs específicos
Se recomienda **Opción B** por coherencia con el style actual del proyecto.

**Tests**:
- Tests existentes en `config_test.go` deben seguir pasando sin cambios
- Tests de integración por stage:
  - `TestLoad_ExpandAndUnmarshalOnly`: input YAML minimal, verificar que defaults se aplican
  - `TestLoad_ExpandEnvMap`: YAML con `${VAR}` en valores, verificar reemplazo
  - `TestLoad_ApplyDiscovery`: con/sin `tools/` directorio, verificar merge
  - `TestLoad_ApplyToolsets`: con toolsets.yaml, verificar merge order
- `TestValidate_TLSEnabledNoCert`: `tls.enabled=true` sin `tls.cert_path` → error
- `TestValidate_RateLimitInvalid`: `rate_limit.enabled=true` con `rps=0` → error
- `TestValidate_DuplicateToolNames`: dos tools con mismo nombre → error
- `TestValidate_ValidConfig`: config correcto → nil
- `go test ./internal/config/... -cover -count=1` → cobertura no disminuye

**Esfuerzo**: ~2h (incluye Validate + tests de integración)

---

### T2.2 — Extraer route registration de sse.go:Start()

**Problema**: `Start()` construye inline TODO el árbol de rutas: health, docs (OpenAPI spec), rate limiting, middleware auth de upload, handler de upload, handler de files, admin (3 capas de middleware + 8 endpoints), SSE endpoint, MCP stream handler, message handler. Total: ~193 líneas (251-443).

**Solución**: Extraer a métodos privados del struct `MCPServer`:

```go
func (s *MCPServer) Start() error {
    mux := http.NewServeMux()
    s.registerHealthRoute(mux)
    s.registerDocsRoute(mux)
    if s.uploadDir != "" {
        s.registerUploadRoutes(mux)
    }
    if s.adminKey != "" && s.db != nil {
        s.registerAdminRoutes(mux)
    }
    s.registerMCPRoutes(mux)

    handler := s.buildMiddlewareChain(mux)
    // ... http.Server setup ...
    return s.listenAndServe(handler)
}

func (s *MCPServer) registerAdminRoutes(mux *http.ServeMux) { ... }
func (s *MCPServer) registerMCPRoutes(mux *http.ServeMux) { ... }
func (s *MCPServer) buildMiddlewareChain(handler http.Handler) http.Handler { ... }
func (s *MCPServer) listenAndServe(handler http.Handler) error { ... }
```

Cada método es extracción directa sin cambios de lógica.

**Archivos afectados**:
- `internal/transport/sse.go` — refactor interno de `Start()`

**Tests**:
- Tests de integración existentes deben pasar sin cambios
- No se requieren tests nuevos (refactor puro, sin cambio de comportamiento)

**Esfuerzo**: ~45 min

---

### T2.3 — Unificar delete handlers en admin

**Problema**: `DeleteUserData` (~80 líneas), `DeleteUserCollection` (~84 líneas), `DeleteGlobalCollection` (~77 líneas) comparten ~90% de código duplicado. Actualmente hacen `SELECT COUNT(*)` + `DELETE` en dos consultas separadas dentro de una transacción. Esto es correcto pero ineficiente: requiere dos round trips a Postgres y la transacción mantiene locks page-level entre ambas.

**Solución**: Usar `DELETE RETURNING` en vez de `SELECT COUNT(*)` + `DELETE` separados. Esto elimina un round trip y garantiza atomicidad sin lock prolongado:

```sql
-- Enfoque nuevo (una sola consulta):
DELETE FROM kb_documents WHERE user_id = $1 RETURNING COALESCE(pg_column_size(metadata), 0)
```

Esto retorna `COALESCE(pg_column_size(metadata), 0)` de cada fila eliminada (misma métrica que el código actual, y segura contra metadata NULL). Go suma los valores y cuenta filas. `DELETE` adquiere lock de fila automáticamente sin necesidad de `FOR UPDATE` explícito.

```go
type deleteQuery struct {
    DeleteSQL   string
    DeleteArgs  []any
    AuditAction string
    AuditArgsFn func(count int, bytes int64, reqID string) []any
    LogExtraFn  func(e *zerolog.Event, count int, bytes int64) *zerolog.Event
}

func (h *Handler) executeDelete(ctx context.Context, q deleteQuery, txTimeout time.Duration) (deleteResponse, error) {
    ctx, cancel := context.WithTimeout(ctx, txTimeout)
    defer cancel()

    tx, err := h.db.BeginTx(ctx, nil)
    if err != nil { return deleteResponse{}, fmt.Errorf("begin tx: %w", err) }
    defer tx.Rollback()

    reqID := middleware.GetRequestID(ctx)

    rows, err := tx.QueryContext(ctx, q.DeleteSQL, q.DeleteArgs...)
    if err != nil { return deleteResponse{}, fmt.Errorf("delete: %w", err) }
    defer rows.Close()

    var count int
    var bytesFreed int64
    for rows.Next() {
        var b int64
        if err := rows.Scan(&b); err != nil {
            return deleteResponse{}, fmt.Errorf("scan: %w", err)
        }
        bytesFreed += b
        count++
    }
    if err := rows.Err(); err != nil {
        return deleteResponse{}, fmt.Errorf("rows iter: %w", err)
    }

    if count == 0 {
        tx.Commit()
        log.Info().Str("request_id", reqID).Msg("no documents found, nothing to delete")
        return deleteResponse{Deleted: false}, nil
    }

    _, err = tx.ExecContext(ctx,
        `INSERT INTO admin_audit_log(admin_action, target_user_id, target_collection, docs_deleted, bytes_freed, request_id)
         VALUES ($1, $2, $3, $4, $5, $6)`,
        q.AuditArgsFn(count, bytesFreed, reqID)...,
    )
    if err != nil { return deleteResponse{}, fmt.Errorf("audit log: %w", err) }

    if err := tx.Commit(); err != nil {
        return deleteResponse{}, fmt.Errorf("commit: %w", err)
    }

    e := log.Info().Int("count", count).Int64("bytes_freed", bytesFreed)
    if q.LogExtraFn != nil { q.LogExtraFn(e, count, bytesFreed) }
    e.Msg("admin delete completed")

    return deleteResponse{Deleted: true, DocsDeleted: count, BytesFreed: bytesFreed}, nil
}
```

**Timeout de transacción**: `txTimeout` default 30s. Si el DELETE excede este límite, Postgres cancela la operación vía `statement_timeout` y Go detecta `context.DeadlineExceeded`.

Cada handler público queda en ~15 líneas:

```go
func (h *Handler) DeleteUserData(w http.ResponseWriter, r *http.Request) {
    userID := r.PathValue("user_id")
    if !h.validateUserID(userID) {
        http.Error(w, `{"error":"invalid user_id"}`, 400)
        return
    }
    resp, err := h.executeDelete(r.Context(), deleteQuery{
        DeleteSQL:   `DELETE FROM kb_documents WHERE user_id = $1 RETURNING COALESCE(pg_column_size(metadata), 0)`,
        DeleteArgs:  []any{userID},
        AuditAction: "delete_user",
        AuditArgsFn: func(count int, bytes int64, reqID string) []any {
            return []any{"delete_user", userID, nil, count, bytes, reqID}
        },
    }, 30*time.Second)
    // error handling + writeJSON
}
```

**Nota de migración**: La función SQL cambia de `SELECT COUNT(*), COALESCE(SUM(pg_column_size(metadata)), 0)` + `DELETE` separado a `DELETE RETURNING COALESCE(pg_column_size(metadata), 0)` en un solo viaje. Tests existentes que mockean SQL deben actualizarse. Los valores de `docs_bytes_freed` son equivalentes (misma función `pg_column_size(metadata)`), con protección adicional para metadata NULL.

**Archivos afectados**:
- `internal/admin/handler.go` — `executeDelete`, refactor 3 handlers
- `internal/admin/handler_test.go` — actualizar mocks SQL

**Tests**:
- Tests existentes de handler (67 tests) actualizados con nuevo SQL
- Tests parametrizados para `executeDelete`: tx begin error, query error, scan error, commit error, timeout
- Test específico: `TestDeleteUserData_TransactionTimeout` — contexto expira → error timeout
- Mantener tests específicos de validación para cada tipo de delete (user_id vs collection validation son diferentes)

**Esfuerzo**: ~2h (incluye actualización de tests existentes)

---

### T2.4 — Unificar auth middleware (upload + admin)

**Problema**: Dos implementaciones de Bearer token auth con ~50 líneas de código duplicado:
- `authMiddleware` en `sse.go:70-103` — upload API key. Skip if key empty.
- `admin.Middleware` en `handler.go:34-71` — admin. 503 si key empty.

Ambas leen `Authorization: Bearer <token>`, comparan con la key, y retornan 401 si no coincide. Ninguna valida el formato del token.

Además, `admin.Middleware` inyecta `X-Request-ID` en el contexto (para trazabilidad en audit log). Esta funcionalidad debe preservarse en el middleware unificado.

**Solución**: Nuevo paquete `internal/auth` con validación de formato de token + request ID injection:

```go
// internal/auth/middleware.go
package auth

import (
    "context"
    "crypto/sha256"
    "crypto/subtle"
    "regexp"
    "net/http"

    "github.com/google/uuid"
)

var validToken = regexp.MustCompile(`^[a-zA-Z0-9._-]{8,256}$`)
var requestIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,255}$`)

type contextKey string
const requestIDKey contextKey = "auth_request_id"

type OnEmptyBehavior int

const (
    OnEmptySkip OnEmptyBehavior = iota
    OnEmpty503
    OnEmpty401
)

func GetRequestID(ctx context.Context) string {
    if id, ok := ctx.Value(requestIDKey).(string); ok {
        return id
    }
    return ""
}

func BearerAuth(keyHash [32]byte, onEmpty OnEmptyBehavior, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if keyHash == [32]byte{} { // zero value = no key configured
            switch onEmpty {
            case OnEmptySkip: next.ServeHTTP(w, r); return
            case OnEmpty503: http.Error(w, ...503...); return
            case OnEmpty401: http.Error(w, ...401...); return
            }
        }

        authHeader := r.Header.Get("Authorization")
        if !strings.HasPrefix(authHeader, "Bearer ") {
            http.Error(w, `{"error":"invalid authorization header"}`, 401)
            return
        }
        token := strings.TrimSpace(authHeader[7:])

        // Validación de formato — previene null bytes, unicode weird, DoS
        if !validToken.MatchString(token) {
            http.Error(w, `{"error":"invalid token format"}`, 401)
            return
        }

        tokenHash := sha256.Sum256([]byte(token))
        if subtle.ConstantTimeCompare(keyHash[:], tokenHash[:]) != 1 {
            http.Error(w, `{"error":"invalid api key"}`, 401)
            return
        }

        // Inyectar request_id para trazabilidad
        requestID := r.Header.Get("X-Request-ID")
        if requestID == "" || !requestIDPattern.MatchString(requestID) {
            requestID = uuid.New().String()
        }
        if len(requestID) > 255 {
            requestID = requestID[:255]
        }
        w.Header().Set("X-Request-ID", requestID)
        ctx := context.WithValue(r.Context(), requestIDKey, requestID)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

El `keyHash` se computa una vez en startup (ver T1.1) y se pasa como `[32]byte` — no como string — eliminando la tentación de comparar directamente.

Nota: `GetRequestID` reemplaza `admin.getRequestID` y `auth.requestIDKey` reemplaza `admin.requestIDKey`. El paquete `admin` importará `auth.GetRequestID`.

**Archivos afectados**:
- Nuevos: `internal/auth/middleware.go`, `internal/auth/context.go`, `internal/auth/middleware_test.go`
- Modificados: `internal/transport/sse.go` (authMiddleware → `auth.BearerAuth`), `internal/admin/handler.go` (eliminar `Middleware`, usar `auth.BearerAuth` + `auth.GetRequestID`)

**Tests**:
- `TestBearerAuth_ValidToken` → 200
- `TestBearerAuth_InvalidToken` → 401
- `TestBearerAuth_TokenTooShort` (< 8 chars) → 401
- `TestBearerAuth_TokenTooLong` (> 256 chars) → 401
- `TestBearerAuth_TokenNullByte` → 401
- `TestBearerAuth_TokenUnicodeWeird` → 401
- `TestBearerAuth_OnEmptySkip` → pasa al handler sin key
- `TestBearerAuth_OnEmpty503` → 503
- `TestBearerAuth_OnEmpty401` → 401
- `TestBearerAuth_NoAuthHeader` → 401
- `TestBearerAuth_WrongScheme` (Basic en vez de Bearer) → 401
- `TestBearerAuth_ConstantTimeCompare_Timing`: benchmark estadístico (ver T1.1)
- `TestBearerAuth_RespectsClientRequestID`: `X-Request-ID: custom-id` → contexto y header matching
- `TestBearerAuth_GenerateRequestID`: sin X-Request-ID → UUID generado
- `TestBearerAuth_InvalidRequestID`: X-Request-ID con chars inválidos → reemplazado por UUID

**Esfuerzo**: ~1.5h

---

## T3 — Operational Improvements

### T3.1 — Extender validation Go-side (recursivo + constraints)

**Problema**: Ya existe `validateInputArguments()` en `internal/executor/subprocess.go:537` que verifica required fields, tipos básicos (string/number/boolean), y enum constraints. Sin embargo, tiene limitaciones:
- No soporta tipos anidados (objects con properties, arrays de objetos)
- No valida `minLength`, `maxLength`, `pattern`, `format`, `minimum`, `maximum`
- No hay tests unitarios para la validación (solo se prueba indirectamente via tests de executor)

**Solución**: Extender la validación existente con soporte recursivo y constraints completos de JSON Schema. Los patrones existentes (`validateType`, `validateEnum`) se mantienen y amplían:

```go
// internal/executor/validator.go
package executor

func ValidateArguments(schema map[string]ToolProperty, required []string, args map[string]interface{}) error {
    // Required fields
    for _, prop := range required {
        if _, ok := args[prop]; !ok {
            return fmt.Errorf("missing required argument: %s", prop)
        }
    }

    for name, value := range args {
        propSchema, ok := schema[name]
        if !ok {
            continue // extra args permitted
        }
        if err := validateValue(value, propSchema, name); err != nil {
            return err
        }
    }
    return nil
}

func validateValue(value interface{}, schema ToolProperty, path string) error {
    // Type check
    if err := checkType(value, schema.Type, path); err != nil {
        return err
    }

    // String constraints
    if str, ok := value.(string); ok {
        if schema.MinLength != nil && len(str) < *schema.MinLength {
            return fmt.Errorf("%s: minLength=%d, got len=%d", path, *schema.MinLength, len(str))
        }
        if schema.MaxLength != nil && len(str) > *schema.MaxLength {
            return fmt.Errorf("%s: maxLength=%d, got len=%d", path, *schema.MaxLength, len(str))
        }
        if schema.Pattern != nil {
            if !schema.PatternCompiled.MatchString(str) {
                return fmt.Errorf("%s: does not match pattern %s", path, *schema.Pattern)
            }
        }
        if schema.Format != "" {
            if err := validateFormat(str, schema.Format, path); err != nil {
                return err
            }
        }
    }

    // Number constraints
    if num, ok := value.(float64); ok {
        if schema.Minimum != nil && num < *schema.Minimum {
            return fmt.Errorf("%s: minimum=%v, got %v", path, *schema.Minimum, num)
        }
        if schema.Maximum != nil && num > *schema.Maximum {
            return fmt.Errorf("%s: maximum=%v, got %v", path, *schema.Maximum, num)
        }
    }

    // Enum
    if len(schema.Enum) > 0 {
        found := false
        for _, e := range schema.Enum { if value == e { found = true; break } }
        if !found { return fmt.Errorf("%s: must be one of %v", path, schema.Enum) }
    }

    // Nested object
    if schema.Type == "object" && len(schema.Properties) > 0 {
        obj, _ := value.(map[string]interface{})
        if err := ValidateArguments(schema.Properties, schema.Required, obj); err != nil {
            return fmt.Errorf("%s.%s", path, err) // recursive
        }
    }

    // Array items
    if schema.Type == "array" && schema.Items != nil {
        arr, _ := value.([]interface{})
        for i, item := range arr {
            if err := validateValue(item, *schema.Items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
                return err
            }
        }
    }

    return nil
}

func validateFormat(str, format, path string) error {
    switch format {
    case "email": /* regex RFC 5322 */
    case "uri":   /* url.ParseRequestURI */
    case "date-time": /* time.Parse(RFC3339) */
    }
}
```

**Edge cases cubiertos**:
- **Coerción de tipos con `json.Number`**: Por defecto `json.Unmarshal` decodifica todos los números como `float64`. Si el código usa `json.Decoder` con `UseNumber()`, los números llegan como `json.Number` (string wrapper). El validador debe manejar ambos casos: `float64` vía type assertion directa, y `json.Number` vía conversión `Float64()`. El array de items en `schema.Enum` también puede ser `json.Number` — comparar con `fmt.Sprint` o convertir ambos lados a `float64` para compatibilidad.
- Nil vs missing: `null` en JSON se deserializa como `nil`. Si el campo es requerido y es `nil` → error.
- Nil vs missing: `null` en JSON se deserializa como `nil`. Si el campo es requerido y es `nil` → error.
- Unicode: JSON Schema `minLength`/`maxLength` se miden en **caracteres** (code points), no bytes. Usar `utf8.RuneCountInString()` — consistente con el estándar JSON Schema. `octet_length` (bytes) es semántica de base de datos, no de validación de input.
- Unicode edge case: caracteres compuestos (ej. `é` como un code point vs `e` + combining accent) se cuentan como 1 o 2 respectivamente, que es el comportamiento esperado para `minLength`/`maxLength`.
- Longitud extrema: strings de 1GB serían rechazadas por `MaxLength` si el tool lo define. Si no lo define, se acepta (pero el límite de body en T1.2 protege contra esto).

**Archivos afectados**:
- Nuevo: `internal/executor/validator.go`, `internal/executor/validator_test.go`
- Modificado: `internal/executor/subprocess.go` — extender `validateInputArguments()` existente con soporte recursivo y constraints
- `internal/mcp/types.go` — exponer `ToolProperty` con campos `MinLength`, `Pattern`, etc. si no existen

**Tests**:
- `TestValidator_MissingRequired`
- `TestValidator_WrongType`
- `TestValidator_EnumConstraint`
- `TestValidator_NestedObject`: object con properties anidadas + required en nested
- `TestValidator_ArrayOfObjects`: array con items de tipo object
- `TestValidator_MinMaxLength`: string < minLength, > maxLength
- `TestValidator_Pattern`: regex match y mismatch
- `TestValidator_Format`: email/uri/date-time válidos e inválidos
- `TestValidator_MinimumMaximum`: number fuera de rango
- `TestValidator_Coercion_IntAsFloat`: `json.RawMessage("1")` → aceptado como number
- `TestValidator_NilRequired`: campo requerido con valor `nil` (JSON `null`) → error
- `TestValidator_UnicodeLength`: string con emojis, `RuneCountInString` vs `len`
- `TestValidator_HugeString`: string de 100MB → aceptado (body size limit lo maneja en nivel HTTP)

**Esfuerzo**: ~4h (significativamente más que las 2h estimadas originalmente por la complejidad recursiva y edge cases)

---

### T3.2 — Centralizar check_docker_container en tests shell

(Sin cambios respecto a v1 — el feedback no identificó issues aquí.)

**Problema**: 3 scripts shell definen la misma función `check_docker_container()` con jaccard 1.0.

**Solución**: Extraer a `tests/common/helpers.sh` y sourcear.

**Archivos afectados**:
- Nuevo: `tests/common/helpers.sh`
- Modificados: `tests/test_data_analysis_image.sh`, `tests/test_excel_analysis.sh`, `tests/test_quick.sh`

**Esfuerzo**: ~15 min

---

### T3.3 — No copiar .git/ completo en Dockerfile

**Problema original**: `COPY .git/ /app/.git/` infla la imagen ~20%.

**Problema adicional**: `git log --oneline --all` falla en shallow clones (CI/CD común con `git fetch --depth=1`). `--all` puede no existir.

**Solución**: Generar changelog con fallback para shallow clones:

```dockerfile
# En go-builder stage:
RUN git log --oneline --all > /tmp/git-log.txt 2>/dev/null || \
    git log --oneline -100 > /tmp/git-log.txt 2>/dev/null || \
    echo "no git history" > /tmp/git-log.txt

# En final stage:
COPY --from=go-builder /tmp/git-log.txt /app/.git-log.txt
```

**Mejor aún**: Generar changelog en CI antes de docker build y pasar como build arg:

```bash
# CI step:
GIT_LOG=$(git log --oneline --all 2>/dev/null | head -100 || echo "no git history")
docker build --build-arg GIT_LOG="$GIT_LOG" -t mcp-server .

# Dockerfile:
ARG GIT_LOG
RUN echo "$GIT_LOG" > /app/.git-log.txt
```

El tool `changelog_generator` (Python) lee de `/app/.git-log.txt` si existe, fallback a `git log`.

**Archivos afectados**:
- `deployments/Dockerfile`
- `tools/changelog_generator/main.py`
- `deployments/.dockerignore` — añadir `.git/`

**Tests**: `docker build --build-arg GIT_LOG="test message"` → imagen sin `.git/`. `changelog_generator` output incluye "test message".

**Esfuerzo**: ~45 min

---

### T3.4 — DB pool coordination entre Go y Python KB tools

**Problema original**: Go tiene 2 pools (health: 2 conns, admin: 10 conns). Python KB tools abren conexiones por request. Puede agotar `max_connections` de Postgres.

**Problema adicional**: Solo implementar pool en Python no sincroniza con los pools de Go. Si ambos suman más que `max_connections`, igual hay problemas.

**Solución**: Connection limits por role en PostgreSQL como capa de seguridad, además del pool en Python:

```sql
-- Tarea DBA (requiere superuser, no se ejecuta en startup de la app):
ALTER ROLE mcp_app WITH CONNECTION LIMIT 20;
ALTER ROLE mcp_admin WITH CONNECTION LIMIT 10;
ALTER ROLE mcp_health WITH CONNECTION LIMIT 2;
```

**Pool en Python** (si no existe ya):

```python
# tools/knowledge_base/main.py
from psycopg2.pool import ThreadedConnectionPool
_pool = ThreadedConnectionPool(1, 10, DATABASE_URL) if DATABASE_URL else None
```

**Documentación** en `PRODUCTION.md`:

```
## Connection Budget

max_connections >= 20 (mcp_app) + 10 (mcp_admin) + 2 (mcp_health) + 20% margin = ~39

Ajustar postgresql.conf: max_connections = 50
```

**Archivos afectados**:
- `tools/knowledge_base/main.py` — pool si no existe
- `docs/PRODUCTION.md` — connection budget
- SQL migration script (si existe) — `ALTER ROLE ... CONNECTION LIMIT`

**Tests**: 20 requests concurrentes a KB search no fallan por `too many connections`.

**Esfuerzo**: ~30 min

---

## T4 — Nice-to-have

### T4.1 — Log correlation Go ↔ Python

**Problema**: Los `request_id` generados en Go no se pasan a los tools Python. Sin correlación entre logs.

**Solución**: Pasar `request_id` en el campo `context` del `SubprocessRequest`:

```go
// internal/mcp/types.go — añadir campo RequestID a SubprocessContext existente
type SubprocessContext struct {
    LLMAPIURL   string `json:"llm_api_url,omitempty"`
    LLMModel    string `json:"llm_model,omitempty"`
    DatabaseURL string `json:"database_url,omitempty"`
    WorkingDir  string `json:"working_dir,omitempty"`
    UserID      string `json:"user_id,omitempty"`
    RequestID   string `json:"request_id,omitempty"`  // NUEVO
}
```

En Python, cada tool usa `request.get("context", {}).get("request_id", "unknown")`.

**Backward compatibility**: El campo `request_id` es opcional (`omitempty`). Tools Python que no lo lean siguen funcionando sin cambios. Tools que quieran usarlo lo leen del context. Migration guide en commit message, no se requiere cambio en todos los tools simultáneamente.

**Tests**:
- `TestSubprocessRequest_ContextIncludesRequestID`: verificar que el JSON enviado incluye `request_id`
- `TestSubprocessRequest_ContextBackwardCompat`: si `request_id` está vacío, el campo no aparece en JSON (`omitempty`)

**Esfuerzo**: ~45 min

---

### T4.2 — Métricas para admin endpoints

(Sin cambios respecto a v1 — el feedback no identificó issues adicionales.)

**Solución**: Añadir contadores Prometheus en `internal/metrics/metrics.go` e incrementar en cada handler admin.

**Archivos afectados**:
- `internal/metrics/metrics.go`
- `internal/admin/handler.go`

**Tests**: `TestAdminMetrics_DeleteIncrementsCounter` — mock prometheus registry.

**Esfuerzo**: ~30 min

---

### T4.3 — TLS documentation para entornos no air-gapped

(Sin cambios respecto a v1.)

**Solución**: Documentar en `PRODUCTION.md` cómo usar Caddy o nginx como reverse proxy con TLS termination.

**Archivos afectados**:
- `docs/PRODUCTION.md`

**Esfuerzo**: ~15 min

---

## Tabla de dependencias entre tareas

| Tarea | Depende de | Razón |
|-------|-----------|-------|
| T2.4 (auth unify) | T1.1 (time-const) | T2.4 hereda lógica de pre-hash + time-constant |
| T3.1 (validator) | T2.1 (config split) | Necesita schema types bien definidos y estables |
| T4.1 (log correl) | T3.1 (validator) | Logs de error del validator necesitan request_id para correlación |
| T2.3 (delete refactor) | — | Independiente, pero tests comparten mocks con admin |
| T1.3 (panic recover) | — | Independiente |
| T1.4 (CORS admin) | T2.4 (auth unify) | `adminCORSMiddleware` wrappea `auth.BearerAuth` |
| T1.2 (body limit) | — | Independiente |
| T2.2 (route extract) | — | Independiente (refactor puro) |

**Orden recomendado**:
1. T1.1 → T2.4 (auth, incluye time-constant)
2. T1.2, T1.3, T1.4 (resto de seguridad — paralelizable)
3. T2.1 (config split, desbloquea T3.1)
4. T2.2, T2.3 (refactors — paralelizable con T2.1)
5. T3.1 (validator, requiere T2.1)
6. T3.2, T3.3, T3.4 (operacional — paralelizable)
7. T4.1 (log correlation, requiere T3.1)
8. T4.2, T4.3 (nice-to-have)

---

## Archivos afectados (resumen)

| Archivo | T1 | T2 | T3 | T4 |
|---------|:--:|:--:|:--:|:--:|
| `internal/transport/sse.go` | ✓ | ✓ | — | — |
| `internal/admin/handler.go` | ✓ | ✓ | — | ✓ |
| `internal/admin/handler_test.go` | — | ✓ | — | — |
| `internal/config/config.go` | ✓ | ✓ | — | — |
| `internal/transport/upload_handler.go` | ✓ | — | — | — |
| `cmd/server/main.go` | ✓ | — | ✓ | — |
| `internal/metrics/metrics.go` | — | — | — | ✓ |
| `internal/executor/subprocess.go` | — | — | — | ✓ |
| `internal/executor/validator.go` | — | — | ✓ | — |
| `internal/executor/validator_test.go` | — | — | ✓ | — |
| `internal/auth/middleware.go` | — | ✓ | — | — |
| `internal/auth/context.go` | — | ✓ | — | — |
| `internal/auth/middleware_test.go` | — | ✓ | — | — |
| `internal/mcp/types.go` | — | — | — | ✓ |
| `deployments/Dockerfile` | — | — | ✓ | — |
| `deployments/.dockerignore` | — | — | ✓ | — |
| `tests/common/helpers.sh` | — | — | ✓ | — |
| `tests/test_data_analysis_image.sh` | — | — | ✓ | — |
| `tests/test_excel_analysis.sh` | — | — | ✓ | — |
| `tests/test_quick.sh` | — | — | ✓ | — |
| `tools/knowledge_base/main.py` | — | — | ✓ | — |
| `tools/changelog_generator/main.py` | — | — | ✓ | — |
| `docs/PRODUCTION.md` | — | — | ✓ | ✓ |

---

## Riesgos aceptados (actualizado)

- **Regresión en tests T2.x**: mitigado con `go test ./... -count=1` antes/después de cada cambio.
- **SSE streaming vs MaxBytesReader (T1.2)**: requiere verificación de la librería mcp-go. Si hay streaming real, aplicar límite solo en `/message`. Marcar como **pendiente de investigación** antes de implementar.
- **DELETE RETURNING (T2.3)**: en tablas con millones de docs, el `DELETE` + `RETURNING` puede generar un resultado grande en memoria. Mitig: paginación implícita vía `context.Timeout` de 30s.
- **Validación recursiva (T3.1)**: schemas con `$ref` o `allOf/oneOf/anyOf` no están soportados. Se documenta como limitación — MCP specs actuales no usan composición de schemas.
- **Token validation regex (T2.4)**: el regex `^[a-zA-Z0-9._-]{8,256}$` puede necesitar ajuste si alguna herramienta externa usa tokens con otros caracteres. Mitig: validación configurable (allowlist vs denylist).
- **Config Validate() (T2.1)**: puede rechazar configs que actualmente funcionan con valores por defecto implícitos. Mitig: implementar Validate() como opt-in al principio, luego hacerlo mandatory tras un período de transición.
- **Log correlation backward compat (T4.1)**: `omitempty` asegura que tools antiguos no reciban campos inesperados. Sin embargo, si un tool Python itera sobre `context.keys()` y espera solo `user_id`, podría fallar. Mitig: documentar en commit que es additive change.
