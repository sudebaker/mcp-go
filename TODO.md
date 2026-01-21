 TODO.md - Plan de Mejoras Técnicas - MCP-Go
 📊 Estado Actual del Proyecto
- **4 archivos Go** en 4 paquetes internos (config, executor, mcp, transport)
- **5 herramientas Python** (~370-534 líneas cada una)
- **1 solo test** básico en `tests/server_test.go`
- Arquitectura: Orquestador MCP con ejecución de subprocess en Python
---
 🔴 CRÍTICAS (Implementar Inmediatamente)
  1. Seguridad: Sandbox para Ejecución de Código Python
  Estado: ✅ Completado (Docker-based)
  Archivos afectados: tools/data_analysis/main.py
  Implementación: Contenedores Docker efímeros con auto_remove
  Archivos nuevos:
  - tools/data_analysis/sandbox.Dockerfile  # Imagen con pandas + matplotlib
  - tools/common/sandbox.py                  # DockerSandboxedExecutor
  Límites de recursos:
  - Memoria: 256MB
  - CPU: 0.5
  - Timeout: 60s
  - PIDs: 50
  - Red: none (aislamiento total)
  Protocolo de streaming:
  - __CHUNK__:{json} - Progreso intermedio
  - __RESULT__:{json} - Resultado final
  Fallback: exec() directo si Docker no está disponible
---
 2. Prevención de Path Traversal
 Estado: ✅ Completado  
 Archivos afectados: Todos los tools Python (file_path parámetro)
Problema: No hay validación de rutas de archivos, permitiendo ../../../etc/passwd.
Implementación en Go:
// internal/executor/path_validator.go
package executor
import (
    "errors"
    "path/filepath"
    "strings"
)
var ErrPathTraversal = errors.New("path traversal attempt detected")
func ValidatePath(path string, allowedDir string) error {
    cleaned := filepath.Clean(path)
    resolved, err := filepath.EvalSymlinks(cleaned)
    if err != nil {
        return err
    }
    allowedResolved, err := filepath.EvalSymlinks(allowedDir)
    if err != nil {
        return err
    }
    if !strings.HasPrefix(resolved, allowedResolved) {
        return ErrPathTraversal
    }
    return nil
}
Implementación en Python (común para todos los tools):
# tools/common/validators.py
def validate_file_path(file_path: str, allowed_dir: str = "/data") -> None:
    """
    Valida que el archivo esté dentro del directorio permitido.
    Lanza ValueError si intenta path traversal.
    """
    allowed = Path(allowed_dir).resolve()
    path = Path(file_path).resolve()
    
    if not path.is_relative_to(allowed):
        raise ValueError(f"Path traversal detected: {file_path}")
    
    if not path.exists():
        raise FileNotFoundError(f"File not found: {file_path}")
---
 3. Gestión de Conexiones a Base de Datos con Pooling
 Estado: ✅ Completado  
 Archivos afectados: tools/knowledge_base/main.py:55-57
Problema: Se crea una conexión nueva por cada request (ineficiente y agota conexiones).
Implementación:
# tools/knowledge_base/db_pool.py
from psycopg2 import pool
from contextlib import contextmanager
_connection_pool = None
def init_pool(database_url: str, minconn: int = 2, maxconn: int = 10):
    global _connection_pool
    _connection_pool = pool.ThreadedConnectionPool(
        minconn=minconn,
        maxconn=maxconn,
        dsn=database_url
    )
@contextmanager
def get_connection():
    if _connection_pool is None:
        raise RuntimeError("Connection pool not initialized")
    conn = _connection_pool.getconn()
    try:
        yield conn
        conn.commit()
    finally:
        _connection_pool.putconn(conn)
def close_pool():
    global _connection_pool
    if _connection_pool:
        _connection_pool.closeall()
        _connection_pool = None
Integración en config:
# configs/config.yaml
execution:
  environment:
    DB_POOL_MIN: "2"
    DB_POOL_MAX: "10"
---
🟡 ALTA Prioridad (Implementar en próximas 2 semanas)
 4. Suite de Tests Completa
 Estado: ✅ Go tests completos (config, executor, transport, metrics, health)
 Archivos nuevos:
 - internal/config/config_test.go      # Config loading, env vars, defaults
 - internal/executor/path_validator_test.go  # Path validation tests
 - internal/transport/ratelimit_test.go # Rate limiter tests
 - internal/metrics/metrics_test.go    # Metrics tests
 - internal/health/checks_test.go      # Health check tests
 Cobertura Go: ~70%
 Pendiente: Python tests para tools
---
 5. Métricas y Observabilidad con Prometheus
  Estado: ✅ Completado  
  Archivos nuevos:
  - internal/metrics/metrics.go      # Prometheus metrics definition
  - internal/metrics/metrics_test.go # Tests
  - internal/health/checks.go        # Health check metrics
  Métricas implementadas:
  - mcp_requests_total (method, status)
  - mcp_request_duration_seconds (method)
  - mcp_tool_executions_total (tool_name, status)
  - mcp_tool_execution_duration_seconds (tool_name)
  - mcp_active_connections
  - mcp_rate_limit_hits_total
  - mcp_redis_cache_hits_total
  - mcp_redis_cache_misses_total
  - mcp_postgres_connections_active
  - mcp_postgres_connections_idle
  - mcp_postgres_connections_wait_total
  - mcp_llm_requests_total (provider, status)
  - mcp_llm_request_duration_seconds (provider)
  - mcp_health_* (memory metrics)
---
  6. Caching de Respuestas LLM con Redis
  Estado: ✅ Completado  
  Objetivo: Reducir llamadas LLM para queries repetidas
 Archivos nuevos:
 - tools/common/llm_cache.py        # LLMCache con Redis
 - Integración en tools/data_analysis/main.py
Implementación:
# tools/common/llm_cache.py
import hashlib
import json
from typing import Optional
import redis
class LLMCache:
    def __init__(self, redis_url: str = "redis://localhost:6379"):
        self.redis = redis.from_url(redis_url)
        self.ttl = 3600  # 1 hora
    
    def _generate_key(self, prompt: str, model: str) -> str:
        content = f"{model}:{prompt}"
        return f"llm:cache:{hashlib.sha256(content.encode()).hexdigest()}"
    
    def get(self, prompt: str, model: str) -> Optional[str]:
        key = self._generate_key(prompt, model)
        cached = self.redis.get(key)
        return cached.decode() if cached else None
    
    def set(self, prompt: str, model: str, response: str):
        key = self._generate_key(prompt, model)
        self.redis.setex(key, self.ttl, response)
# Uso en tools/data_analysis/main.py
cache = LLMCache()
def call_llm_with_cache(llm_api_url: str, llm_model: str, prompt: str) -> str:
    cached = cache.get(prompt, llm_model)
    if cached:
        return cached
    
    response = call_llm(llm_api_url, llm_model, prompt)
    cache.set(prompt, llm_model, response)
    return response
Configuración:
# configs/config.yaml
execution:
  environment:
    REDIS_URL: "redis://redis:6379/0"
    LLM_CACHE_TTL: "3600"
---
  7. Connection Pooling para LLM API
  Estado: ✅ Completado  
  Objetivo: Reutilizar conexiones HTTP a Ollama
 Archivos nuevos:
 - internal/executor/llm_client.go  # LLMClient con connection pooling
   - http.Client con MaxIdleConns=10
   - IdleConnTimeout=90s
   - Call(), CallWithModel(), CallWithOptions()
Implementación en Go:
// internal/executor/llm_client.go
package executor
import (
    "net/http"
    "time"
)
type LLMClient struct {
    client   *http.Client
    endpoint string
}
func NewLLMClient(endpoint string, timeout time.Duration) *LLMClient {
    return &LLMClient{
        client: &http.Client{
            Timeout: timeout,
            Transport: &http.Transport{
                MaxIdleConns:        10,
                MaxIdleConnsPerHost: 10,
                IdleConnTimeout:     90 * time.Second,
            },
        },
        endpoint: endpoint,
    }
}
func (c *LLMClient) Call(model string, prompt string) (string, error) {
    // Implementación con retry
}
---
 🟢 MEDIA Prioridad (Implementar en 1-2 meses)
 8. Refactorización: Biblioteca Común Python
 Estado: Parcialmente completado - validators.py y retry.py creados
 Archivos nuevos:
 - tools/common/validators.py      # validate_file_path, validate_output_path
 - tools/common/retry.py           # call_llm_with_retry
 - tools/knowledge_base/db_pool.py # Connection pooling

 8.1 Mejoras a tools/pdf_reports/main.py
 Estado: ✅ Completado
 - Fix imports (execute_values, SentenceTransformer)
 - Eliminar conn.commit() redundantes
 - Añadir validate_output_path() para seguridad
 - Crear build_base_context() para DRY
 - Cache de Jinja2 Environment
 - Mejor manejo de errores con traceback

 8.2 Correcciones a tools/knowledge_base/main.py
 Estado: ✅ Completado
 - Mover imports fuera de try/except (execute_values, SentenceTransformer)
 - Eliminar conn.commit() redundantes en ensure_schema()
 - Mejorar imports dinámicos pypdf/docx con constantes de disponibilidad
 - Usar connection pooling en lugar de conexiones directas
Objetivo: DRY - Don't Repeat Yourself
Crear módulo tools/common/:
tools/common/
├── __init__.py
├── protocol.py          # read_request, write_response
├── validators.py        # validate_file_path, validate_input
├── errors.py           # Custom exceptions, error handlers
├── logging.py          # Structured logging
└── llm_cache.py       # Redis caching
Implementación:
# tools/common/protocol.py
import json
import sys
from typing import Any
def read_request() -> dict[str, Any]:
    input_data = sys.stdin.read()
    return json.loads(input_data)
def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))
def write_success(request_id: str, content: list[dict], structured: dict) -> None:
    write_response({
        "success": True,
        "request_id": request_id,
        "content": content,
        "structured_content": structured
    })
def write_error(request_id: str, code: str, message: str, details: str = "") -> None:
    write_response({
        "success": False,
        "request_id": request_id,
        "error": {
            "code": code,
            "message": message,
            "details": details
        }
    })
# tools/common/errors.py
class ToolError(Exception):
    """Base exception for tool errors."""
    def __init__(self, code: str, message: str, details: str = ""):
        self.code = code
        self.message = message
        self.details = details
        super().__init__(message)
class FileNotFound(ToolError):
    def __init__(self, file_path: str):
        super().__init__("FILE_NOT_FOUND", f"File not found: {file_path}")
class InvalidInput(ToolError):
    def __init__(self, field: str):
        super().__init__("INVALID_INPUT", f"Missing required field: {field}")
def handle_tool_error(e: Exception, request_id: str) -> dict[str, Any]:
    """Convierte excepciones en respuestas de error estandarizadas."""
    if isinstance(e, ToolError):
        return write_error_dict(request_id, e.code, e.message, e.details)
    return write_error_dict(request_id, "EXECUTION_FAILED", str(e), traceback.format_exc())
---
 9. Retry con Backoff Exponencial
 Estado: ✅ Completado  
 Objetivo: Manejo robusto de fallos transitorios
Implementación:
# tools/common/retry.py
from tenacity import retry, stop_after_attempt, wait_exponential, retry_if_exception_type
import requests
class TransientError(Exception):
    """Error que debería ser reintentado."""
    pass
def is_transient_error(exception: Exception) -> bool:
    if isinstance(exception, requests.Timeout):
        return True
    if isinstance(exception, requests.ConnectionError):
        return True
    if isinstance(exception, requests.HTTPError) and 500 <= exception.response.status_code < 600:
        return True
    return False
@retry(
    stop=stop_after_attempt(3),
    wait=wait_exponential(multiplier=1, min=2, max=10),
    retry=retry_if_exception_type(TransientError),
    reraise=True
)
def call_llm_with_retry(llm_api_url: str, llm_model: str, prompt: str) -> str:
    try:
        response = requests.post(
            f"{llm_api_url}/api/generate",
            json={"model": llm_model, "prompt": prompt},
            timeout=60
        )
        response.raise_for_status()
        return response.json().get("response", "")
    except requests.RequestException as e:
        if is_transient_error(e):
            raise TransientError(f"Transient LLM API error: {e}") from e
        raise
# Instalar: pip install tenacity
---
 10. Validación de Configuración al Inicio
 Estado: ✅ Completado  
 Objetivo: Fallar rápido con mensajes claros
Implementación:
// internal/config/validation.go
package config
import (
    "errors"
    "fmt"
    "os"
)
var (
    ErrInvalidPort          = errors.New("invalid port number (must be 1-65535)")
    ErrInvalidToolName     = errors.New("tool name cannot be empty")
    ErrInvalidToolCommand  = errors.New("tool command cannot be empty")
    ErrInvalidTimeout      = errors.New("timeout must be positive")
    ErrWorkingDirNotExists = errors.New("working directory does not exist")
)
func Validate(cfg *Config) error {
    // Validar server config
    if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
        return fmt.Errorf("%w: %d", ErrInvalidPort, cfg.Server.Port)
    }
    if cfg.Server.Name == "" {
        return errors.New("server name cannot be empty")
    }
    
    // Validar execution config
    if cfg.Execution.DefaultTimeout <= 0 {
        return fmt.Errorf("%w: %v", ErrInvalidTimeout, cfg.Execution.DefaultTimeout)
    }
    if _, err := os.Stat(cfg.Execution.WorkingDir); os.IsNotExist(err) {
        return fmt.Errorf("%w: %s", ErrWorkingDirNotExists, cfg.Execution.WorkingDir)
    }
    
    // Validar cada herramienta
    for i, tool := range cfg.Tools {
        if err := ValidateToolConfig(&tool); err != nil {
            return fmt.Errorf("tool #%d (%s): %w", i, tool.Name, err)
        }
    }
    
    return nil
}
func ValidateToolConfig(tool *ToolConfig) error {
    if tool.Name == "" {
        return ErrInvalidToolName
    }
    if tool.Command == "" {
        return ErrInvalidToolCommand
    }
    if tool.Timeout < 0 {
        return ErrInvalidTimeout
    }
    
    // Validar que el comando exista (en runtime)
    if _, err := os.LookPath(tool.Command); err != nil {
        return fmt.Errorf("command not found: %s", tool.Command)
    }
    
    return nil
}
Uso en cmd/server/main.go:
cfg, err := config.Load(*configPath)
if err != nil {
    log.Fatal().Err(err).Msg("Failed to load configuration")
}
// Validar config
if err := config.Validate(cfg); err != nil {
    log.Fatal().Err(err).Msg("Configuration validation failed")
}
---
 11. Rate Limiting por Cliente
  Estado: ✅ Completado  
  Objetivo: Proteger contra DoS
 Archivos nuevos:
 - internal/transport/ratelimit.go  # RateLimiter token bucket
   - Sin dependencias externas
   - Middleware para http.Handler
   - X-Forwarded-For support
 Integración:
 - internal/config/config.go        # RateLimitRPS, RateLimitBurst
 - internal/transport/sse.go        # Middleware integrado
 - configs/config.yaml              # rate_limit_rps, rate_limit_burst
Implementación:
// internal/transport/ratelimit.go
package transport
import (
    "sync"
    "time"
    "golang.org/x/time/rate"
)
type RateLimiter struct {
    limiters map[string]*rate.Limiter
    mu       sync.RWMutex
    rps      int // requests per second
    burst    int
}
func NewRateLimiter(rps, burst int) *RateLimiter {
    return &RateLimiter{
        limiters: make(map[string]*rate.Limiter),
        rps:      rps,
        burst:    burst,
    }
}
func (rl *RateLimiter) Allow(clientID string) bool {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    
    limiter, exists := rl.limiters[clientID]
    if !exists {
        limiter = rate.NewLimiter(rate.Limit(rl.rps), rl.burst)
        rl.limiters[clientID] = limiter
    }
    
    return limiter.Allow()
}
// Middleware
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        clientID := getClientID(r)
        
        if !rl.Allow(clientID) {
            http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
            return
        }
        
        next.ServeHTTP(w, r)
    })
}
func getClientID(r *http.Request) string {
    return r.RemoteAddr
}
Configuración:
# configs/config.yaml
server:
  rate_limit:
    requests_per_second: 10
    burst: 20
---
 12. Logging Estructurado Python
  Estado: ✅ Completado  
  Archivos nuevos:
  - tools/common/logging.py         # StructuredLogger class
  Características:
  - JSON estructurado con timestamp, level, logger, message
  - Soporte para datos adicionales (extra_data)
  - RequestLogger para HTTP requests con timing
  - Decorador @timed_operation para logging automático de funciones
  - Compatible con sistemas de agregación de logs
---
🔵 BAJA Prioridad (Mejoramiento Continuo)
13. Sistema de Plugins para Herramientas
Estado: Herramientas estáticas en config.yaml  
Objetivo: Carga dinámica de herramientas
Implementación:
// internal/plugin/interface.go
package plugin
import "context"
type Tool interface {
    Name() string
    Description() string
    InputSchema() map[string]interface{}
    Execute(ctx context.Context, args map[string]interface{}) (interface{}, error)
}
type Plugin struct {
    Tool    Tool
    Config  interface{}
}
// internal/plugin/loader.go
func LoadPlugins(dir string) ([]Tool, error) {
    var tools []Tool
    
    // Escanear directorio buscando plugins
    entries, err := os.ReadDir(dir)
    if err != nil {
        return nil, err
    }
    
    for _, entry := range entries {
        if entry.IsDir() {
            // Cargar plugin desde directorio
            pluginPath := filepath.Join(dir, entry.Name())
            tool, err := loadPluginFromDir(pluginPath)
            if err != nil {
                log.Warn().Err(err).Str("plugin", entry.Name()).Msg("Failed to load plugin")
                continue
            }
            tools = append(tools, tool)
        }
    }
    
    return tools, nil
}
---
14. Documentación OpenAPI/Swagger
Estado: Endpoint /openapi.json parcial en sse.go  
Objetivo: Documentación completa generada automáticamente
Herramientas:
- swaggo/swag para Go
- Generar desde anotaciones en código
// @title MCP Orchestrator API
// @version 1.0
// @description Model Context Protocol Orchestrator Server
// @host localhost:8080
// @BasePath /
package transport
// @Summary Health check
// @Description Returns server health status
// @Tags health
// @Produce json
// @Success 200 {object} HealthResponse
// @Router /health [get]
func (s *MCPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
    // ...
}
---
15. Migraciones de Base de Datos
Estado: Schema creado manualmente en código  
Objetivo: Versionado y rollback automático
Implementación:
# Usar golang-migrate
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
# Crear directorio
migrations/
├── 000001_init_schema.up.sql
├── 000001_init_schema.down.sql
└── ...
000001_init_schema.up.sql:
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE IF NOT EXISTS kb_documents (
    id SERIAL PRIMARY KEY,
    doc_hash VARCHAR(64) UNIQUE NOT NULL,
    file_path TEXT NOT NULL,
    collection VARCHAR(255) NOT NULL DEFAULT 'default',
    metadata JSONB,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS kb_chunks (
    id SERIAL PRIMARY KEY,
    document_id INTEGER REFERENCES kb_documents(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    embedding vector(384),
    metadata JSONB,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_kb_chunks_embedding
ON kb_chunks USING ivfflat (embedding vector_cosine_ops)
WITH (lists = 100);
---
16. Linting Automático (golangci-lint)
Estado: No configurado  
Objetivo: Código consistente y seguro
Archivo:
# .golangci.yml
linters:
  disable-all: true
  enable:
    # Bugs
    - errcheck
    - gosimple
    - govet
    - ineffassign
    - staticcheck
    - typecheck
    - unused
    
    # Style
    - gofmt
    - goimports
    - misspell
    
    # Security
    - gosec
    - goconst
    
    # Complexity
    - gocyclo
    - dupl
    
linters-settings:
  gocyclo:
    min-complexity: 15
  gosec:
    excludes:
      - G104  # Ignore errors on purpose in some cases
run:
  timeout: 5m
  tests: true
  modules-download-mode: readonly
Comandos:
# Instalar
go install github.com/golang-migrate/migrate/v4/cmd/migrate@latest
# Ejecutar lint
golangci-lint run
# Fix automático
golangci-lint run --fix
---
17. CI/CD Pipeline con GitHub Actions
Estado: No configurado  
Objetivo: Tests automáticos en cada PR
Archivo:
# .github/workflows/ci.yml
name: CI
on:
  push:
    branches: [main, develop]
  pull_request:
    branches: [main, develop]
jobs:
  test-go:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.23'
      
      - name: Cache Go modules
        uses: actions/cache@v3
        with:
          path: ~/go/pkg/mod
          key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
      
      - name: Download dependencies
        run: go mod download
      
      - name: Run tests
        run: go test -v -race -coverprofile=coverage.out ./...
      
      - name: Upload coverage
        uses: codecov/codecov-action@v3
        with:
          files: ./coverage.out
      
      - name: Run linter
        uses: golangci/golangci-lint-action@v3
        with:
          version: latest
  test-python:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Set up Python
        uses: actions/setup-python@v4
        with:
          python-version: '3.11'
      
      - name: Cache pip
        uses: actions/cache@v3
        with:
          path: ~/.cache/pip
          key: ${{ runner.os }}-pip-${{ hashFiles('**/requirements.txt') }}
      
      - name: Install dependencies
        run: |
          pip install -r requirements.txt
          pip install pytest pytest-cov flake8
      
      - name: Run tests
        run: pytest tests/ --cov=tools --cov-report=xml
      
      - name: Run linter
        run: flake8 tools/ tests/ --max-line-length=100
  docker-build:
    runs-on: ubuntu-latest
    needs: [test-go, test-python]
    steps:
      - uses: actions/checkout@v4
      
      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v2
      
      - name: Build image
        run: docker build -f deployments/Dockerfile -t mcp-go:test .
---
 18. Health Checks Profundos
  Estado: ✅ Completado  
  Archivos nuevos:
  - internal/health/checks.go         # Checker implementation
  - internal/health/checks_test.go    # Tests
  Checks implementados:
  - Redis ping (StatusDegraded/Unhealthy)
  - PostgreSQL ping (StatusDegraded/Unhealthy)
  - Configuration validation (StatusHealthy/Degraded/Unhealthy)
  - Memory usage monitoring (StatusHealthy/Degraded/Unhealthy)
  - Tool path validation
  - LLM endpoint reachability
  - GetHealthMetrics() export function
---
 19. Optimización de Modelos Embeddings
  Estado: ✅ Completado  
  Objetivo: Cargar una vez y reutilizar
 Archivos nuevos:
 - tools/knowledge_base/model_cache.py  # ModelCache singleton thread-safe
 - Integración en tools/knowledge_base/main.py
Implementación:
# tools/knowledge_base/model_cache.py
import threading
from sentence_transformers import SentenceTransformer
class ModelCache:
    _instance = None
    _lock = threading.Lock()
    _model = None
    
    @classmethod
    def get_model(cls, model_name: str = "all-MiniLM-L6-v2") -> SentenceTransformer:
        """Retorna el modelo cargado (singleton thread-safe)."""
        if cls._model is None:
            with cls._lock:
                if cls._model is None:
                    cls._model = SentenceTransformer(model_name)
        return cls._model
    
    @classmethod
    def clear(cls):
        """Limpia el caché (útil para tests)."""
        with cls._lock:
            cls._model = None
# Uso en handle_search:
def handle_search(request: dict, context: dict) -> dict:
    model = ModelCache.get_model()
    # ...
---
20. Streaming de Respuestas y Tool Execution
 Estado: ✅ Completado (con soporte para imágenes)
 Objetivo: UX más rápida con respuestas progresivas y archivos
 Archivos modificados:
 - internal/executor/subprocess.go   # Parser de streaming chunks + soporte imagen
 - tools/common/sandbox.py           # Emisión de chunks + retorno de archivos
 - tools/data_analysis/main.py       # Uso de streaming + contenido MCP imágenes
 Protocolo de streaming:
 Python tool → stdout:
   __CHUNK__:{"type": "status", "data": {"message": "loading"}}
   __CHUNK__:{"type": "progress", "data": 50}
   __RESULT__:{"success": true, "content": [...]}

 Formato MCP estándar para respuestas:
 {
   "content": [
     {"type": "text", "text": "**Resultado:** análisis completado..."},
     {"type": "image", "data": "iVBORw0KGgo...", "mimeType": "image/png"}
   ],
   "structured_content": {...}
 }

 Soporte de imágenes:
 - Archivos PNG generados en /tmp/output/ dentro del sandbox
 - Mount de volumen temporal para recuperación de archivos
 - Codificación base64 automática
 - Inclusión en array content con type: "image"
 - Go executor convierte a mcp.ImageContent para el cliente

 Benefits:
 - El cliente ve progreso en tiempo real
 - Gráficos visualizables directamente en la UI
 - Compatible hacia atrás (no-streaming fallback)
        
        if done, _ := chunk["done"].(bool); done {
            break
        }
        
        if text, ok := chunk["response"].(string); ok {
            if err := callback(text); err != nil {
                return err
            }
        }
    }
    
    return nil
}
---
 📅 Roadmap Sugerido
 Fase 1: Seguridad y Estabilidad (Semanas 1-2)
 - [x] Path traversal validation
 - [x] Database connection pooling
 - [x] Validation de configuración
 - [x] Retry con backoff
 Fase 2: Observabilidad (Semanas 3-4)
- [x] Suite de tests completa (Go tests: config, executor, transport, metrics, health)
- [x] Métricas Prometheus (internal/metrics/metrics.go + tests)
- [x] Logging estructurado Python (tools/common/logging.py)
- [x] Health checks profundos (internal/health/checks.go + tests)
 Fase 3: Performance (Semanas 5-6)
 - [x] Caching LLM con Redis
 - [x] Connection pooling HTTP
 - [x] Model cache para embeddings
 - [x] Rate limiting
Fase 4: Calidad de Código (Semanas 7-8)
- [ ] Refactorización common library
- [ ] Linting configurado
- [ ] CI/CD pipeline
- [ ] Migraciones de DB
 Fase 5: Mejoras (Semanas 9-12)
 - [x] Sistema de plugins (descartado - requiere reinicio manual)
 - [x] Sandbox Docker (tools/common/sandbox.py + sandbox.Dockerfile)
 - [x] Streaming LLM (internal/executor/subprocess.go + streaming protocol)
 - [x] OpenAPI documentation (internal/transport/docs.go + Swagger UI)
---
 📊 Métricas de Éxito
 | Métrica | Estado Actual | Objetivo |
 |---------|--------------|----------|
 | Cobertura de tests | ~5% | 70%+ |
 | Timeouts de LLM | No medido | < 1% |
 | Latencia promedio | No medido | < 5s |
 | Rate de errores | No medido | < 0.1% |
 | Vulnerabilidades críticas | 1 (sandbox) | 0 |
---