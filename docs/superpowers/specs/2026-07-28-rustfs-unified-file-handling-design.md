# Design: Unificación de ficheros vía ResourceManager + Storage + Resource (MCP compliant)

**Fecha**: 2026-07-28  
**Autor**: opencode  
**Estado**: Revisado — aprobado para implementación  
**Spec MCP**: 2025-06-18  

## Principios rectores

1. Las tools **nunca** conocen RustFS.
2. Las tools **nunca** validan permisos.
3. Las tools **nunca** conocen buckets.
4. Toda la lógica de almacenamiento pertenece al servidor Go.
5. Python únicamente procesa recursos ya resueltos.
6. La API debe permitir sustituir RustFS en el futuro sin modificar ninguna tool.
7. Streaming nativo — sin cargar ficheros completos en memoria.

## 1. Objetivo

Unificar el manejo de ficheros en todas las tools que los aceptan como input para análisis, mediante una abstracción **Resource** gestionada por un **ResourceManager** en Go con un backend **Storage** desacoplado. La implementación inicial usa RustFS (S3-compatible) como backend, pero RustFS nunca es visible en la API pública del framework.

## 2. Arquitectura

```
Cliente MCP
    |
    v
+--------------------+
| Go Server (mcp-go) |
|                    |
|  ResourceManager   |  <-- API pública del framework
|    uses Storage    |
|    emits Resource  |
|                    |
|  Storage (iface)   |  <-- abstracción de backend
|    RustFSStorage   |  <-- impl inicial (minio-go)
|                    |
|  /internal/resource/{token}  <-- endpoint streaming a Python
|  /upload                    <-- subida directa a Storage
|  resources/list | read      <-- handlers MCP
+--------------------+
    | stdin JSON (args + tokens)
    v
+--------------------+
| Python tool        |
|                    |
|  context.file(x)   |  <-- obtiene Resource con Reader HTTP
|    -> Resource     |
|    -> reader       |  <-- stream al endpoint interno de Go
|    .mime, .size    |
|                    |
|  procesa bytes     |  <-- OCR, Whisper, PIL, etc.
+--------------------+
```

## 3. Componentes Go (nuevos)

### 3.1 `internal/resources/resource.go`

```go
package resources

import "io"

type Resource struct {
    URI      string    // opaco: "res://{token}" o futuro scheme
    Name     string
    MIMEType string
    Size     int64
    SHA256   string
    Reader   io.ReadCloser
}

func (r *Resource) Close() error {
    if r.Reader != nil {
        return r.Reader.Close()
    }
    return nil
}
```

### 3.2 `internal/resources/storage.go`

```go
package resources

import (
    "context"
    "io"
    "time"
)

type Storage interface {
    Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) (ObjectInfo, error)
    Open(ctx context.Context, bucket, key string) (io.ReadCloser, error)
    Stat(ctx context.Context, bucket, key string) (ObjectInfo, error)
    List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error)
    Delete(ctx context.Context, bucket, key string) error
}

type ObjectInfo struct {
    Key          string
    Size         int64
    ContentType  string
    ETag         string
    LastModified time.Time
}
```

### 3.3 `internal/resources/rustfs_storage.go`

```go
package resources

import "github.com/minio/minio-go/v7"

type RustFSStorage struct {
    client    *minio.Client
    publicURL string
}

func NewRustFSStorage() (*RustFSStorage, error) {
    // lee RUSTFS_ENDPOINT, RUSTFS_ACCESS_KEY_ID, RUSTFS_SECRET_ACCESS_KEY, RUSTFS_USE_SSL, RUSTFS_PUBLIC_URL
}
// implementa Storage interface
```

### 3.4 `internal/resources/manager.go`

```go
package resources

import (
    "context"
    "io"
    "time"

    "github.com/sudebaker/mcp-go/internal/session"
)

type ResourceManager struct {
    storage     Storage
    bucket      string        // "users"
    sessions    *session.Store
    tokenStore  *TokenStore
}

func (m *ResourceManager) ResolveForTool(ctx context.Context, sessionID, rawArg string) (Resource, error)
func (m *ResourceManager) ResolveManyForTool(ctx context.Context, sessionID string, rawArgs []string) ([]Resource, error)
func (m *ResourceManager) ListForUser(ctx context.Context, sessionID, prefix string) ([]Resource, error)
func (m *ResourceManager) ReadForUser(ctx context.Context, sessionID, uri string) (Resource, error)
func (m *ResourceManager) PutForUser(ctx context.Context, sessionID string, key string, r io.Reader, size int64, ct string) (string, error)
```

Responsabilidades:
- parsear URI, `__files__`, paths legacy
- obtener `user_id` del session store
- validar permisos (`{user_id}/` prefix)
- abrir Storage → Reader
- generar token efímero para Python
- devolver `Resource` con URI opaca

**Bucket strategy**: un único bucket `users`, key `{user_id}/{random}.{ext}`. Aislamiento por prefijo validado en el ResourceManager.

### 3.5 `internal/resources/token.go`

```go
package resources

import (
    "sync"
    "time"
)

type TokenStore struct {
    mu     sync.RWMutex
    tokens map[string]*tokenEntry
}

type tokenEntry struct {
    bucket    string
    key       string
    userID    string
    sessionID string
    expiresAt time.Time
    used      bool  // one-shot
}

func (t *TokenStore) Issue(bucket, key, userID, sessionID string, ttl time.Duration) string
func (t *TokenStore) Validate(token string) (*tokenEntry, error)  // marca used
func (t *TokenStore) Cleanup()  // goroutine periódica
```

TTL por defecto: 60 segundos. Token de un solo uso.

### 3.6 `internal/resources/handler.go` (MCP Resources)

Implementa `resources/list` y `resources/read`. Se declara capability:

```json
{"capabilities": {"resources": {}}}
```

**Posponemos `resources/subscribe`** hasta que exista un cliente que lo requiera. Sin polling en la primera versión.

### 3.7 `internal/transport/resource_handler.go` (NUEVO)

Endpoint interno `GET /internal/resource/{token}`:
1. Valida token (one-shot, 60s).
2. Llama `Storage.Open(bucket, key)`.
3. `io.Copy(w, reader)` — streaming directo a Python.
4. Headers: `Content-Type`, `Content-Length`, `X-Resource-Name`, `X-SHA256`.
5. Invalida token tras uso.

**Auth**: solo localhost / red interna Docker. No expuesto públicamente.

### 3.8 `internal/transport/upload_handler.go` (modificado)

Flujo nuevo:
1. Validar auth, MIME, magic bytes (sin cambios).
2. Obtener `user_id` del session store.
3. Calcular SHA256 del contenido durante upload (tee reader).
4. `manager.PutForUser(ctx, sessionID, key, reader, size, contentType)`.
5. Response:

```json
{
  "success": true,
  "uri": "res://abc123token",
  "sha256": "9f2c...",
  "size": 45678,
  "content_type": "image/jpeg",
  "name": "photo.jpg"
}
```

**Eliminado**: `uploadDir`, `.meta` sidecar, `cleanExpiredUploads`.

## 4. Componentes Python (nuevos)

### 4.1 `tools/common/resources/resource.py`

```python
import io
import os
import urllib.request

INTERNAL_HOST = os.getenv("MCP_INTERNAL_HOST", "localhost:8080")

class Resource:
    def __init__(self, uri, name, mime, size, sha256):
        self.uri = uri
        self.name = name
        self.mime = mime
        self.size = size
        self.sha256 = sha256
        self._reader = None

    @property
    def reader(self):
        """Abre stream HTTP al endpoint interno de Go. Lazy."""
        if self._reader is None:
            token = self.uri.replace("res://", "", 1)
            url = f"http://{INTERNAL_HOST}/internal/resource/{token}"
            self._reader = urllib.request.urlopen(url)
        return self._reader

    def read_bytes(self):
        """Para ficheros pequeños. Cierra tras leer."""
        with self:
            return self.reader.read()

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()

    def close(self):
        if self._reader:
            self._reader.close()
            self._reader = None
```

### 4.2 `tools/common/resources/manager.py`

```python
class ToolContext:
    def __init__(self, request: dict):
        self._request = request
        self._resources = {}

    def file(self, arg_name: str) -> Resource:
        if arg_name not in self._resources:
            meta = self._request.get("_resources", {}).get(arg_name)
            if not meta:
                raise KeyError(f"No resource bound to arg '{arg_name}'")
            self._resources[arg_name] = Resource(**meta)
        return self._resources[arg_name]

    def files(self, arg_name: str) -> list[Resource]:
        if arg_name not in self._resources:
            metas = self._request.get("_resources", {}).get(arg_name)
            if not metas:
                raise KeyError(f"No resources bound to arg '{arg_name}'")
            self._resources[arg_name] = [Resource(**m) for m in metas]
        return self._resources[arg_name]
```

### 4.3 API ideal para una tool

```python
from common.resources import ToolContext

ctx = ToolContext(request)
resource = ctx.file("document")

# Streaming
for chunk in iter(lambda: resource.reader.read(8192), b""):
    process(chunk)
resource.close()

# O para ficheros pequeños
data = resource.read_bytes()
```

La tool no sabe: dónde está almacenado, qué bucket, si es RustFS, si es disco, ni si el backend cambia en el futuro.

## 5. Formato del stdin JSON a Python (Go → Python)

Go enriquece el request antes de pasarlo al subprocess:

```json
{
  "request_id": "...",
  "tool_name": "vision_ocr",
  "arguments": {
    "file_uri": "res://abc123token"
  },
  "context": {
    "user_id": "abc"
  },
  "_resources": {
    "file_uri": {
      "uri": "res://abc123token",
      "name": "photo.jpg",
      "mime": "image/jpeg",
      "size": 45678,
      "sha256": "9f2c..."
    }
  }
}
```

## 6. Declaración de argumentos de fichero en tool.yaml

Go debe saber qué argumentos son recursos para resolverlos. Declaración en `tool.yaml`:

```yaml
inputSchema:
  type: object
  properties:
    file_uri:
      type: string
      format: resource-uri
      description: "URI del recurso (res://...)"
  required: [file_uri]
```

Para arrays:

```yaml
properties:
  file_uris:
    type: array
    items:
      type: string
      format: resource-uri
```

Go lee el schema, detecta `format: resource-uri`, y resuelve automáticamente esos argumentos antes de invocar a Python.

## 7. Compatibilidad temporal

Durante una versión, las tools aceptan:
- `file_uri` (nuevo, URI opaco `res://...`)
- `__files__` (legacy, array `{url, name}`)
- `file_path` (legacy, path absoluto)

**Go ResourceManager** convierte internamente cualquier formato a `Resource`. Las tools legacy que aún lean `file_path` directamente seguirán funcionando en esta versión, pero se migran progresivamente a `context.file()`.

Eliminación de soporte legacy: próxima major version.

## 8. Tools afectadas (12 análisis + 2 output + 1 storage)

### 8.1 Tools de análisis — migrar a `context.file()` / `context.files()`

| # | Tool | Input nuevo | Streaming? | Notas |
|---|------|-------------|-----------|-------|
| 1 | `batch_summarize` | `file_uris[]` | Sí (textos) | Usa `extract_text_from_buffer` |
| 2 | `regulation_diff` | `file_uri_1`, `file_uri_2` | No | Dos recursos |
| 3 | `document_classifier` | `file_uris[]` | Sí | |
| 4 | `config_auditor` | `file_uris[]` | Sí | Implementar lectura real |
| 5 | `vision_ocr` | `file_uri` | No (imagen) | `cv2.imdecode` desde bytes |
| 6 | `transcribe` | `file_uri` | **Sí** (audio) | Whisper lee del stream |
| 7 | `metadata_extractor` | `file_uri` | No | `magic.from_buffer`, `PIL.Image.open(io.BytesIO)` |
| 8 | `stego_detector` | `file_uri` | No | |
| 9 | `document_fingerprint` | `file_uri_1`, `file_uri_2` | No | |
| 10 | `api_diff` | `file_uri_old`, `file_uri_new` | No | |
| 11 | `data_analysis` | `file_uri` | Sí (CSV/Excel) | pandas read desde stream |
| 12 | `case_evidence` | `file_uri` | Sí | Descarga + re-subida vía manager |

### 8.2 Tools de output

| # | Tool | Cambio |
|---|------|--------|
| 13 | `canvas_diagram` | Subida vía `ResourceManager.PutForUser` (Go expone helper). |
| 14 | `pdf_reports` | Opcional: subida a Storage, devolver `resource_link`. |

### 8.3 Tool de storage

| # | Tool | Cambio |
|---|------|--------|
| 15 | `rustfs_storage` | **Tool deprecada**. Operaciones absorbidas por ResourceManager. Mantener en Fase 1 con warning. Eliminar en próxima major. |

## 9. Upload: metadatos calculados

Durante `/upload`, Go calcula:
- **SHA256**: `hash.Hash` tee reader durante escritura a Storage.
- **MIME definitivo**: ya detectado por magic bytes.
- **Tamaño**: contado durante `io.Copy`.

Estos metadatos se guardan como tags de objeto S3 (`x-amz-meta-sha256`, etc.) y se devuelven en la respuesta.

## 10. Organización de paquetes

```
internal/resources/
    resource.go           # tipo Resource
    storage.go            # interfaz Storage + ObjectInfo
    rustfs_storage.go     # impl RustFS (minio-go)
    manager.go            # ResourceManager (API pública)
    token.go              # TokenStore efímero
    handler.go            # handlers MCP resources/list|read
    manager_test.go
    rustfs_storage_test.go
    handler_test.go
    token_test.go

internal/transport/
    resource_handler.go   # GET /internal/resource/{token}
    upload_handler.go     # modificado -> Storage

tools/common/resources/
    __init__.py
    resource.py           # Resource con Reader HTTP lazy
    manager.py            # ToolContext
    resource_test.py
    manager_test.py
```

## 11. Separación de responsabilidades

| Capa | Go | Python |
|------|-----|--------|
| Sesiones | ✓ | |
| Auth | ✓ | |
| Autorización | ✓ | |
| ResourceManager | ✓ | |
| Storage | ✓ | |
| Resources MCP | ✓ | |
| Upload | ✓ | |
| Validaciones seguridad | ✓ | |
| TokenStore | ✓ | |
| OCR / Whisper | | ✓ |
| OpenCV / PIL | | ✓ |
| Extracción texto | | ✓ |
| IA / análisis | | ✓ |

## 12. Fases de implementación

### Fase 1 — Framework (sin breaking changes)
1. `internal/resources/storage.go`
2. `internal/resources/resource.go`
3. `internal/resources/rustfs_storage.go`
4. `internal/resources/token.go`
5. `internal/resources/manager.go`
6. `internal/transport/resource_handler.go`
7. `tools/common/resources/resource.py`
8. `tools/common/resources/manager.py`
9. Tests unitarios

### Fase 2 — MCP Resources + Upload
10. `internal/resources/handler.go` — resources/list, resources/read
11. Registrar capability `resources` en server
12. Modificar `/upload` para usar Storage + calcular SHA256
13. Tests de integración

### Fase 3 — Migración de tools (compat dual)
14. Grupo A: `batch_summarize`, `regulation_diff`, `document_classifier`, `config_auditor`
15. Grupo B: `vision_ocr`, `transcribe`, `metadata_extractor`, `stego_detector`, `document_fingerprint`, `api_diff`
16. Grupo C: `data_analysis`, `case_evidence`
17. Grupo D: `canvas_diagram`, `pdf_reports`
18. Actualizar `configs/config.yaml` schemas (`format: resource-uri`)
19. Actualizar tests

### Fase 4 — Deprecación y documentación
20. Marcar `rustfs_storage` tool como deprecated
21. Documentación: `API.md`, `DEVELOPMENT.md`, `SECURITY.md`
22. `session-notes.md`
23. Eliminar soporte legacy en próxima major version

## 13. Manejo de errores

| Error | Código | Comportamiento |
|-------|--------|----------------|
| URI inválida | `INVALID_URI` | Tool result `{success: false, error}` |
| Token inválido/expirado | `INVALID_TOKEN` | 401 en endpoint interno |
| Recurso no autorizado | `UNAUTHORIZED_RESOURCE` | ResourceManager rechaza |
| Objeto no existe | `OBJECT_NOT_FOUND` | JSON-RPC `-32002` para resources; tool error para tools |
| Storage no disponible | `STORAGE_UNAVAILABLE` | log critical, error 503 |
| File demasiado grande | `FILE_TOO_LARGE` | `/upload` 413 |
| MIME no soportado | `UNSUPPORTED_MIME` | Tool valida antes de procesar |

## 14. Testing

### 14.1 Go
- `internal/resources/manager_test.go`
- `internal/resources/rustfs_storage_test.go`
- `internal/resources/handler_test.go`
- `internal/resources/token_test.go`
- `internal/transport/resource_handler_test.go`
- `internal/transport/upload_handler_test.go` (actualizado)

### 14.2 Python
- `tests/tools/common/resources/test_resource.py`
- `tests/tools/common/resources/test_manager.py`
- Tests existentes de cada tool actualizados con mocks de `ToolContext`

### 14.3 Verificación
```bash
go build -o bin/mcp-server ./cmd/server
go fmt ./... && go vet ./... && go test ./...
python -m pytest tests/tools/common/resources/ -v
python -m pytest tests/test_security_mitigations.py -v
```

## 15. Out of scope

- `resources/subscribe` — posponer hasta tener cliente demandante
- Lifecycle policies de RustFS — configurar en rustfs directamente
- Presigned URLs para descarga directa por cliente
- Migrar tools de codebase-scan (`codebase_scan`, `dependency_audit`, `security_lint`) — leen filesystem del proyecto, no ficheros subidos

## 16. Riesgos

| Riesgo | Mitigación |
|--------|-----------|
| Token interceptado en red interna | Docker network aislada; token one-shot 60s |
| Latencia extra por proxy Go | Misma red Docker; streaming sin materializar |
| Memory pressure en Go | Reader streaming con `io.Copy` |
| `rustfs_storage` tool deprecada | Warning en Fase 1, eliminar en major |
| Cambio de backend en el futuro | Storage interface + ResourceManager encapsulan el backend |

## 17. Decisiones clave tomadas

| Tema | Decisión |
|------|----------|
| Abstracción pública | `Resource` + `ResourceManager` |
| Backend Storage | `Storage` interface, impl `RustFSStorage` |
| URI scheme interno | `res://{token}` (opaco) |
| Bucket | Único bucket `users`, key `{user_id}/{random}.{ext}` |
| Cross-process Go→Python | Endpoint interno `/internal/resource/{token}` con streaming |
| MCP Resources Fase 1 | `resources/list` + `resources/read` |
| `resources/subscribe` | Posponer |
| Compatibilidad | Dual `file_uri` + `__files__` + `file_path` durante Fase 3 |
| `rustfs_storage` tool | Deprecar, eliminar en próxima major |
