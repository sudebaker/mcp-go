Moved from: ../USAGE.md 

# Guia de Uso - MCP-Go

## Descripcion general

`mcp-go` expone un servidor MCP en Go que orquesta herramientas externas definidas en `configs/config.yaml`.

Transporte disponible:

- Principal: `POST /mcp` (streamable HTTP)
- Legacy: `/sse` y `/message`

## Levantar el entorno

```bash
cd deployments
docker compose up -d

# alternativa
# docker-compose up -d
```

Verificacion minima:

```bash
docker compose ps
curl -s http://localhost:8080/health
```

## Servicios del compose local

- `mcp-server` (contenedor `mcp-orchestrator`): `http://localhost:8080`
- `postgres` (`mcp-postgres`): `localhost:5432`
- `rustfs` (`rustfs`): `http://localhost:9000`

Nota: Ollama puede estar en un stack externo y compartir red Docker.

## Uso por capacidades

### 1. Analisis de datos

Herramienta: `analyze_data`

Casos tipicos:

- Analizar Excel/CSV
- Calcular agregados
- Responder preguntas sobre tablas
- Generar salida `text`, `json`, `markdown` o `png`

Ejemplo de prompt:

```text
Analiza /data/ventas.xlsx y responde:
- Total de ventas
- Top 5 productos por importe
- Promedio por categoria
```

### 2. Vision y OCR

Herramienta: `analyze_image`

Tareas soportadas:

- `ocr`
- `describe`
- `extract_entities`
- `answer`

Ejemplo:

```text
Analiza /data/factura.png con task=extract_entities y extrae fechas e importes.
```

### 3. Generacion de reportes

Herramienta: `generate_report`

Tipos relevantes de `report_type`:

- `incident`, `meeting`, `audit`, `executive_summary`, `formal_report`, `corporate_email`, `llm_response`

Salida esperada:

- PDF en base64
- URL de descarga (válida 24h por defecto)

### 4. Knowledge base

Herramientas:

- `kb_ingest` para almacenar contenido
- `kb_search` para recuperar informacion (`semantic`, `keyword`, `hybrid`)

### 5. Herramientas adicionales

- `batch_summarize`
- `regulation_diff`
- `config_auditor`
- `document_classifier`
- `weather_forecast`
- `web_scraper`
- `rss_reader`
- `canvas_diagram`
- `rustfs_storage`

## Flujo de archivos

Rutas principales dentro del contenedor `mcp-orchestrator`:

- `/data/`: lectura y escritura (workspace operativo)
- `/data/uploads/`: archivos subidos por usuarios
- `/app/configs`, `/app/tools`, `/app/templates`: montajes de codigo/config

Scripts utiles:

- `tools/clean-workspace.sh [dias]`

## Ejemplos MCP con curl

```bash
# initialize
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"usage-client","version":"1.0.0"}}}'

# ping
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"ping"}'

# listar herramientas
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/list"}'
```

## Troubleshooting rapido

### El servidor no responde

```bash
docker compose ps
docker logs --tail 200 mcp-orchestrator
curl -v http://localhost:8080/health
```

### Problemas con una herramienta

```bash
docker logs --tail 200 mcp-orchestrator | grep -E "tool|error|timeout"
```

### Problemas con LLM

Verifica variables en `configs/config.yaml` o entorno:

- `LLM_API_URL`
- `LLM_MODEL`

## Referencias

- `README.md`
- `QUICKSTART.md`
- `TESTING.md`
- `docs/API.md`
- `docs/DEVELOPMENT.md`