# Plan de Pruebas MCP-Go - Suite Completa

## Estado Actual del Sistema

### ✅ Servicios Operacionales
- **Ollama**: Funcionando correctamente en puerto 11434
  - 4 modelos instalados: qwen3:8b, qwen3-vl:8b, qwen2.5-coder:7b, all-minilm:33m
  - Generación de texto verificada
- **Open WebUI**: Accesible en puerto 3000
- **MCP Server**: Funcionando en puerto 8080
  - Endpoint /health: ✅
  - Endpoint /mcp (SSE): ✅
- **PostgreSQL**: Running y saludable
- **Contenedores Docker**: 3 contenedores corriendo (mcp-orchestrator, mcpo-proxy, mcp-postgres)

### ⚠️ Problemas Identificados y Resueltos
1. **Dependencia `tenacity` faltante** → Solucionado ✅
2. **Conflicto con paquete `validators` de PyPI** → Removido, usando módulo local ✅
3. **Import path en `safe_file_ops.py`** → Corregido ✅
4. **Dockerfile actualizado** con tenacity (pendiente rebuild completo)

### 🔧 Problemas Pendientes
1. **MCPo Proxy**: Responde con 404 en raíz (esto puede ser normal, necesita ruta específica)
2. **Data Analysis Tool**: Requiere formato MCP específico y variables de entorno

## Suite de Pruebas Rápidas Creada

**Script**: `tests/test_quick.sh`

**Resultados**: 15/17 pruebas pasando (88% éxito)

### Categorías de Pruebas

#### 1. Health Checks de Servicios
```bash
# Verificar que todos los servicios estén accesibles
- Ollama API
- Open WebUI
- MCP Server (/health)
- MCP Server (/mcp SSE)
- MCPo Proxy
- PostgreSQL
```

#### 2. Verificación de Modelos
```bash
# Confirmar modelos disponibles y funcionales
- Listar modelos instalados
- Test de generación con qwen3:8b
```

#### 3. Dependencias Python
```bash
# Verificar módulos críticos
- tenacity
- pandas
- opencv-python-headless
- requests
- numpy
```

#### 4. Tests de Herramientas Individuales
```bash
# Test directo de cada tool Python
- echo tool
- data_analysis tool
- vision_ocr tool (creación de imagen)
```

#### 5. Docker Health
```bash
# Estado de contenedores
- Verificar health status
- Contar contenedores activos
```

## Pruebas Sugeridas por Herramienta

### 🔧 1. Echo Tool
**Estado**: ✅ Funcional

**Pruebas Básicas**:
```bash
# Test 1: Echo simple
echo '{"text":"Hello"}' | docker exec -i mcp-orchestrator python3 /app/tools/echo/main.py

# Resultado esperado:
{"success": true, "content": [{"type": "text", "text": "Echo: Hello"}]}
```

**Pruebas Adicionales Sugeridas**:
- Texto largo (>1000 caracteres)
- Caracteres especiales y unicode
- JSON vacío
- Múltiples llamadas en paralelo (load test)

---

### 📊 2. Data Analysis Tool  
**Estado**: ⚠️ Requiere configuración adicional

**Formato de Request**:
```json
{
  "request_id": "unique-id",
  "arguments": {
    "file_path": "/data/input/file.xlsx",
    "question": "What is the sum of column A?",
    "output_format": "text",
    "use_sandbox": true
  },
  "context": {
    "llm_api_url": "http://ollama:11434",
    "llm_model": "qwen3:8b"
  }
}
```

**Variables de Entorno Necesarias**:
```bash
INPUT_DIR=/data/input
OUTPUT_DIR=/data/output
MAX_FILE_SIZE_MB=100
```

**Pruebas Sugeridas**:
```bash
# Test 1: Análisis básico de Excel
# - Crear archivo test con datos simples
# - Preguntar por suma, promedio, máximo
# - Verificar respuesta correcta

# Test 2: Diferentes formatos de salida
# - output_format: "text", "json", "markdown"

# Test 3: Operaciones complejas
# - Filtrado de datos
# - Agrupaciones
# - Joins entre múltiples columnas

# Test 4: Manejo de errores
# - Archivo no existente
# - Archivo corrupto
# - Pregunta ambigua
# - Archivo muy grande (>100MB)

# Test 5: Sandbox vs No-Sandbox
# - Comparar ejecución con use_sandbox: true/false
# - Verificar isolación de seguridad

# Test 6: Streaming
# - Verificar chunks de progreso
# - Timeout handling
```

**Casos de Uso Reales**:
1. Análisis de ventas (sumas, promedios, tendencias)
2. Reportes de inventario
3. Análisis de logs (CSV)
4. Comparación de datasets

---

### 🖼️ 3. Vision OCR Tool
**Estado**: ⚠️ No probado completamente

**Formato de Request**:
```json
{
  "request_id": "unique-id",
  "arguments": {
    "image_path": "/data/input/image.png",
    "task": "ocr",
    "language": "eng"
  },
  "context": {
    "llm_api_url": "http://ollama:11434",
    "llm_model": "qwen3-vl:8b"
  }
}
```

**Tareas Soportadas**:
- `ocr`: Extracción de texto
- `describe`: Descripción general
- `analyze`: Análisis detallado
- `detect`: Detección de objetos

**Pruebas Sugeridas**:
```bash
# Test 1: OCR básico
# - Imagen con texto claro
# - Verificar extracción correcta

# Test 2: Múltiples idiomas
# - Inglés (eng)
# - Español (spa)
# - Texto mixto

# Test 3: Calidad de imagen
# - Alta resolución
# - Baja resolución
# - Imagen borrosa

# Test 4: Diferentes tareas
# - OCR vs describe vs analyze
# - Comparar resultados

# Test 5: Formatos de imagen
# - PNG, JPEG, BMP
# - Imágenes grandes
# - Transparencias (PNG)

# Test 6: Casos complejos
# - Documentos escaneados
# - Facturas
# - Capturas de pantalla
# - Diagramas
```

**Casos de Uso Reales**:
1. Digitalización de documentos
2. Extracción de datos de facturas
3. Análisis de capturas de pantalla
4. Lectura de placas vehiculares
5. Descripción de imágenes para accesibilidad

---

### 📄 4. PDF Reports Tool
**Estado**: ⚠️ No probado

**Formato de Request**:
```json
{
  "request_id": "unique-id",
  "arguments": {
    "template_name": "report_template",
    "output_path": "/data/output/report.pdf",
    "data": {
      "title": "Monthly Report",
      "date": "2026-01-26",
      "sections": [...]
    }
  }
}
```

**Pruebas Sugeridas**:
```bash
# Test 1: Reporte simple
# - Template básico
# - Datos mínimos
# - Verificar PDF generado

# Test 2: Templates complejos
# - Con imágenes
# - Con tablas
# - Con gráficos

# Test 3: Datos dinámicos
# - Integrar con data_analysis
# - Generar gráficos desde datos
# - Tablas automáticas

# Test 4: Estilos y formatos
# - CSS custom
# - Múltiples páginas
# - Headers/footers

# Test 5: Tamaño y rendimiento
# - PDFs grandes (>100 páginas)
# - Múltiples imágenes
# - Timeout handling
```

**Casos de Uso Reales**:
1. Reportes mensuales automatizados
2. Certificados
3. Facturas personalizadas
4. Documentación técnica
5. Presentaciones

---

### 📚 5. Knowledge Base Tool
**Estado**: ⚠️ No probado

**Operaciones**:
- `ingest`: Ingerir documentos
- `search`: Buscar en la base

**Formato Request Ingest**:
```json
{
  "request_id": "unique-id",
  "arguments": {
    "action": "ingest",
    "file_path": "/data/input/document.pdf",
    "metadata": {
      "title": "Document Title",
      "author": "Author Name",
      "category": "Technical"
    }
  },
  "context": {
    "db_host": "postgres",
    "db_name": "mcp_kb",
    "db_user": "mcpuser",
    "db_password": "mcppass"
  }
}
```

**Formato Request Search**:
```json
{
  "request_id": "unique-id",
  "arguments": {
    "action": "search",
    "query": "How to configure the system?",
    "limit": 5,
    "threshold": 0.7
  },
  "context": {
    "db_host": "postgres",
    "db_name": "mcp_kb",
    "db_user": "mcpuser",
    "db_password": "mcppass"
  }
}
```

**Pruebas Sugeridas**:
```bash
# Test 1: Ingestión básica
# - Ingerir PDF simple
# - Ingerir DOCX
# - Ingerir TXT
# - Verificar chunks en DB

# Test 2: Búsqueda semántica
# - Query exacto
# - Query con sinónimos
# - Query en diferente idioma
# - Verificar relevancia

# Test 3: Metadata filtering
# - Buscar por categoría
# - Buscar por autor
# - Buscar por fecha

# Test 4: Rendimiento
# - Ingestar múltiples documentos
# - Búsqueda en base grande
# - Concurrent searches

# Test 5: RAG completo
# - Integrar con LLM
# - Generar respuestas con contexto
# - Citar fuentes
```

**Casos de Uso Reales**:
1. Base de conocimiento técnico
2. FAQ automatizado
3. Búsqueda en documentación
4. Asistente de soporte
5. Análisis de contratos

---

## Pruebas de Integración

### 1. Integración MCP Server ↔ Tools
```bash
# Verificar que el servidor pueda ejecutar tools correctamente
# Test el flujo completo: Request → Execute → Response
```

### 2. Integración Open WebUI ↔ MCP
```bash
# Configurar Open WebUI para usar MCP tools
# Pasos:
# 1. En Open WebUI, ir a Settings → Tools
# 2. Agregar MCP server URL: http://mcpo-proxy:8001
# 3. Autenticar si es necesario
# 4. Verificar que tools aparezcan disponibles
# 5. Hacer llamadas desde chat
```

### 3. Integración Ollama ↔ Tools
```bash
# Verificar que tools puedan llamar a Ollama
# Test con diferentes modelos
# Medir tiempos de respuesta
```

### 4. Pipeline Completo
```bash
# Test end-to-end:
# 1. Usuario hace pregunta en Open WebUI
# 2. Open WebUI llama a MCP tool vía proxy
# 3. Tool ejecuta análisis con Ollama
# 4. Resultado se devuelve a usuario

# Ejemplo:
# "Analiza el archivo ventas.xlsx y genera un PDF con el resumen"
```

---

## Pruebas de Rendimiento

### Load Testing
```bash
# Test 1: Concurrent requests
# - 10 requests simultáneas
# - 50 requests simultáneas
# - 100 requests simultáneas

# Test 2: Rate limiting
# - Verificar límite de 10 RPS
# - Verificar burst de 20

# Test 3: Memory usage
# - Monitorear uso durante análisis de archivos grandes
# - Verificar garbage collection

# Test 4: Timeout handling
# - Requests que excedan timeout (60s)
# - Cancelación de requests
```

### Stress Testing
```bash
# Test 1: Archivos grandes
# - Excel con 1M+ filas
# - PDF con 1000+ páginas
# - Imágenes de alta resolución

# Test 2: Queries complejas
# - Análisis que requieren múltiples operaciones
# - Joins pesados
# - Agregaciones complejas
```

---

## Pruebas de Seguridad

### 1. Path Traversal
```bash
# Intentar acceder a archivos fuera de /data
# file_path: "../../etc/passwd"
# file_path: "/app/configs/config.yaml"
```

### 2. Code Injection
```bash
# En data_analysis, intentar código malicioso
# question: "import os; os.system('rm -rf /')"
# Verificar sandbox blocking
```

### 3. Resource Exhaustion
```bash
# Intentar consumir toda la memoria
# Intentar CPU infinito
# Verificar timeouts y límites
```

### 4. SQL Injection
```bash
# En knowledge_base queries
# Verificar parameterización
```

---

## Pruebas de Recuperación

### 1. Failover
```bash
# Test cuando Ollama está caído
# Test cuando PostgreSQL está caído
# Verificar error handling
```

### 2. Reinicio de Contenedores
```bash
# Reiniciar mcp-orchestrator
# Verificar que tools se re-registren
# Verificar que state se recupere
```

### 3. Network Issues
```bash
# Simular latencia de red
# Simular pérdida de paquetes
# Verificar retries y timeouts
```

---

## Documentación de Pruebas

### Scripts Creados
1. **`tests/test_quick.sh`** - Suite rápida de verificación (15-30s)
2. **`tests/test_suite_complete.sh`** - Suite completa (5-10min)
3. **`tests/test_excel_analysis.sh`** - Test específico de data_analysis

### Ejecutar Suite Completa
```bash
# Suite rápida
./tests/test_quick.sh

# Suite completa
./tests/test_suite_complete.sh

# Test específico
./tests/test_excel_analysis.sh
```

---

## Próximos Pasos Recomendados

### Prioridad Alta
1. ✅ Crear suite de pruebas básicas
2. ⏳ Completar test de data_analysis con env vars correcto
3. ⏳ Test completo de vision_ocr
4. ⏳ Configurar integración con Open WebUI
5. ⏳ Validar pipeline end-to-end

### Prioridad Media
6. Test de pdf_reports con templates
7. Test de knowledge_base completo
8. Pruebas de rendimiento básicas
9. Documentar casos de uso reales
10. Crear tests automatizados en CI/CD

### Prioridad Baja
11. Pruebas de seguridad exhaustivas
12. Stress testing
13. Chaos engineering
14. Performance profiling detallado

---

## Comandos Útiles para Debugging

```bash
# Ver logs del MCP server
docker logs -f mcp-orchestrator

# Ver logs de Ollama
docker logs -f ollama

# Inspeccionar contenedor
docker exec -it mcp-orchestrator bash

# Test manual de tool
echo '{"text":"test"}' | docker exec -i mcp-orchestrator python3 /app/tools/echo/main.py

# Verificar dependencias Python
docker exec mcp-orchestrator pip list

# Verificar archivos en /data
docker exec mcp-orchestrator ls -la /data/

# Check PostgreSQL
docker exec mcp-postgres psql -U mcpuser -d mcp_kb -c "\dt"

# Reiniciar servicios
docker-compose -f deployments/docker-compose.yml restart
```

---

## Resultados Esperados

### Criterios de Éxito
- ✅ Todos los servicios health checks pasan
- ✅ Todas las herramientas responden sin errores
- ✅ Integración Open WebUI funcional
- ✅ Rate limiting efectivo
- ✅ Tiempos de respuesta < 30s (queries simples)
- ✅ Sin memory leaks
- ✅ Error handling robusto

### Métricas Clave
- **Uptime**: >99.9%
- **Response time (p95)**: <5s
- **Error rate**: <1%
- **Throughput**: 10+ RPS sustained

---

## Contacto y Soporte

Para issues o preguntas sobre las pruebas, consultar:
- AGENTS.md - Guía del proyecto
- README.md - Documentación general
- configs/config.yaml - Configuración de tools

**Versión**: 0.1.0  
**Última actualización**: 2026-01-26
